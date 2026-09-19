package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTransparentRelayPreservesAuthenticationAndRouting(t *testing.T) {
	require.NoError(t, i18n.Init())
	previousRateLimit := setting.ModelRequestRateLimitEnabled
	previousPerformance := common.GetPerformanceMonitorConfig()
	setting.ModelRequestRateLimitEnabled = false
	performance := previousPerformance
	performance.Enabled = false
	common.SetPerformanceMonitorConfig(performance)
	t.Cleanup(func() {
		setting.ModelRequestRateLimitEnabled = previousRateLimit
		common.SetPerformanceMonitorConfig(previousPerformance)
	})
	previousDB, previousRedis, previousCache := model.DB, common.RedisEnabled, common.MemoryCacheEnabled
	previousType := common.MainDatabaseType()
	previousLogDB, previousLogType, previousMaster := model.LOG_DB, common.LogDatabaseType(), common.IsMasterNode
	t.Setenv("LOG_SQL_DSN", "")
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/relay.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}))
	model.DB, common.RedisEnabled, common.MemoryCacheEnabled = db, false, false
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.IsMasterNode = false
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() {
		model.DB, common.RedisEnabled, common.MemoryCacheEnabled = previousDB, previousRedis, previousCache
		common.SetMainDatabaseType(previousType)
		require.NoError(t, model.InitLogDB())
		model.LOG_DB, common.IsMasterNode = previousLogDB, previousMaster
		common.SetLogDatabaseType(previousLogType)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})
	user := model.User{Username: "transparent-owner", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", Quota: 12345, AffCode: "transparent-owner"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "transparentclientsecret", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 12345}
	require.NoError(t, db.Create(&token).Error)
	payload := "{ \"model\":\"test-model\", \"messages\":false, \"unknown\":123 }\n"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, readErr := io.ReadAll(r.Body)
		assert.NoError(t, readErr)
		assert.Equal(t, payload, string(body))
		assert.Equal(t, "Bearer channel-secret", r.Header.Get("Authorization"))
		assert.Equal(t, "OpenCode", r.Header.Get("X-Title"))
		assert.Equal(t, "https://opencode.ai/", r.Header.Get("HTTP-Referer"))
		assert.Equal(t, "/v1/chat/completions?unknown=%2f&unknown=a+b", r.RequestURI)
		w.Header().Set("X-Provider", "raw-response")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "unparsed provider response")
	}))
	defer upstream.Close()
	settings := `{"transparent_relay":true,"transparent_billing":"external"}`
	selected := &model.Channel{Id: 71, Type: constant.ChannelTypeOpenRouter, Key: "channel-secret", BaseURL: &upstream.URL, Setting: &settings}
	selected.Status, selected.Models, selected.Group = common.ChannelStatusEnabled, "test-model", "default"
	require.NoError(t, db.Create(selected).Error)
	router := gin.New()
	SetRelayRouter(router)
	for _, tc := range []struct {
		name, credential string
		status           int
	}{
		{"missing credential", "", http.StatusUnauthorized},
		{"invalid credential", "Bearer invalid", http.StatusUnauthorized},
		{"authorized selected channel", "Bearer sk-transparentclientsecret-71", http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?unknown=%2f&unknown=a+b", strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", tc.credential)
			request.Header.Set("X-Title", "OpenCode")
			request.Header.Set("HTTP-Referer", "https://opencode.ai/")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			if tc.status == http.StatusCreated {
				assert.Equal(t, "unparsed provider response", response.Body.String())
				assert.Equal(t, "raw-response", response.Header().Get("X-Provider"))
			}
		})
	}
	unknown := httptest.NewRequest(http.MethodPost, "/v1/unknown-endpoint", strings.NewReader(payload))
	unknown.Header.Set("Authorization", "Bearer sk-transparentclientsecret-71")
	unknown.Header.Set("Content-Type", "application/json")
	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, unknown)
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.EqualValues(t, 1, calls.Load())
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 12345, user.Quota)
	assert.Equal(t, 12345, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}
