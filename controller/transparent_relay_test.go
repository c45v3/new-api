package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func transparentTestJSON(t *testing.T, value any) *string {
	t.Helper()
	b, err := common.Marshal(value)
	require.NoError(t, err)
	s := string(b)
	return &s
}

func transparentTestChannel(t *testing.T, base string, kind int) *model.Channel {
	t.Helper()
	return &model.Channel{Id: 71, Type: kind, Key: "channel-secret", BaseURL: &base, Setting: transparentTestJSON(t, dto.ChannelSettings{TransportMode: dto.TransportModeTransparent, TransparentBilling: "external"})}
}

func transparentTestGateway(t *testing.T, selected *model.Channel) *httptest.Server {
	t.Helper()
	router := gin.New()
	router.Use(middleware.BodyStorageCleanup(), middleware.DecompressRequestMiddleware())
	router.NoRoute(func(c *gin.Context) {
		if err := middleware.SetupContextForSelectedChannel(c, selected, "unknown-provider-model"); err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		Relay(c, types.RelayFormatOpenAI)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

func transparentTestClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestTransparentRelayRepresentationAndCredentials(t *testing.T) {
	payload := []byte("{\n \"model\": [false], \"unknown\": {\"duplicate\":1,\"duplicate\":2}, \"bytes\": \"\\u0061\"\n}\n")
	type observed struct {
		body   []byte
		header http.Header
		uri    string
		err    error
	}
	requests := make(chan observed, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		requests <- observed{body, r.Header.Clone(), r.RequestURI, err}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("opaque-success"))
	}))
	defer upstream.Close()
	gateway := transparentTestGateway(t, transparentTestChannel(t, upstream.URL+"/mount%2Ftenant/", constant.ChannelTypeOpenRouter))
	req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/unknown%2Foperation?x=%2f&key=one&%6bey=two&KEY=three&api_key=four&api-key=five&access_token=six&authorization=seven&x-api-key=eight&x-goog-api-key=nine&x=a+b&x=%20", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header = http.Header{
		"Content-Type": {"application/json"}, "X-Unknown": {"one", "two"},
		"Http-Referer": {"https://opencode.ai/"}, "X-Title": {"OpenCode"},
		"Authorization": {"Bearer client-secret"}, "X-Api-Key": {"client-secret"}, "Api-Key": {"client-secret"}, "X-Goog-Api-Key": {"client-secret"},
		"Mj-Api-Secret": {"client-secret"}, "Cookie": {"session=client-secret"}, "Sec-Websocket-Protocol": {"bearer.client-secret"},
		"Connection": {"X-Connection-Secret"}, "X-Connection-Secret": {"client-secret"},
	}
	response, err := transparentTestClient(t).Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, response.StatusCode)
	assert.Equal(t, "opaque-success", string(result))
	got := <-requests
	require.NoError(t, got.err)
	assert.Equal(t, payload, got.body)
	assert.Equal(t, "/mount%2Ftenant/v1/unknown%2Foperation?x=%2f&x=a+b&x=%20", got.uri)
	assert.Equal(t, []string{"one", "two"}, got.header.Values("X-Unknown"))
	assert.Equal(t, "https://opencode.ai/", got.header.Get("HTTP-Referer"))
	assert.Equal(t, "OpenCode", got.header.Get("X-Title"))
	assert.Equal(t, "Bearer channel-secret", got.header.Get("Authorization"))
	for _, name := range []string{"X-Api-Key", "Api-Key", "X-Goog-Api-Key", "Mj-Api-Secret", "Cookie", "Sec-WebSocket-Protocol", "Connection", "X-Connection-Secret"} {
		assert.Empty(t, got.header.Values(name), name)
	}
}

func TestTransparentRelayChannelAuthentication(t *testing.T) {
	cases := []struct {
		name          string
		kind          int
		header, value string
	}{
		{"openai", constant.ChannelTypeOpenAI, "Authorization", "Bearer channel-secret"},
		{"openrouter", constant.ChannelTypeOpenRouter, "Authorization", "Bearer channel-secret"},
		{"anthropic", constant.ChannelTypeAnthropic, "X-Api-Key", "channel-secret"},
		{"gemini", constant.ChannelTypeGemini, "X-Goog-Api-Key", "channel-secret"},
		{"azure", constant.ChannelTypeAzure, "Api-Key", "channel-secret"},
		{"custom", constant.ChannelTypeCustom, "Authorization", "Bearer channel-secret"},
		{"newapi", constant.ChannelTypeNewAPI, "Authorization", "Bearer channel-secret"},
		{"deepseek", constant.ChannelTypeDeepSeek, "Authorization", "Bearer channel-secret"},
		{"mistral", constant.ChannelTypeMistral, "Authorization", "Bearer channel-secret"},
		{"xai", constant.ChannelTypeXai, "Authorization", "Bearer channel-secret"},
		{"moonshot", constant.ChannelTypeMoonshot, "Authorization", "Bearer channel-secret"},
		{"siliconflow", constant.ChannelTypeSiliconFlow, "Authorization", "Bearer channel-secret"},
		{"volcengine", constant.ChannelTypeVolcEngine, "Authorization", "Bearer channel-secret"},
		{"jina", constant.ChannelTypeJina, "Authorization", "Bearer channel-secret"},
		{"cohere", constant.ChannelTypeCohere, "Authorization", "Bearer channel-secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := make(chan http.Header, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers <- r.Header.Clone()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			gateway := transparentTestGateway(t, transparentTestChannel(t, upstream.URL, tc.kind))
			req, err := http.NewRequest(http.MethodPost, gateway.URL+"/arbitrary", strings.NewReader("not JSON"))
			require.NoError(t, err)
			for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"} {
				req.Header.Set(name, "client-secret")
			}
			response, err := transparentTestClient(t).Do(req)
			require.NoError(t, err)
			response.Body.Close()
			require.Equal(t, http.StatusNoContent, response.StatusCode)
			got := <-headers
			assert.Equal(t, tc.value, got.Get(tc.header))
			for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"} {
				if name != tc.header {
					assert.Empty(t, got.Get(name))
				}
			}
		})
	}
}

func TestTransparentRelaySafeExplicitOverrides(t *testing.T) {
	for _, credential := range []string{"none", "Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "Cookie", "Mj-Api-Secret", "Sec-WebSocket-Protocol"} {
		t.Run(credential, func(t *testing.T) {
			var calls atomic.Int32
			headers := make(chan http.Header, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				headers <- r.Header.Clone()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			selected := transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI)
			overrides := map[string]any{"*": true, "Authorization": "Token {api_key}", "X-Title": "operator-title", "X-Copied": "{client_header:X-Visible}"}
			if credential != "none" {
				overrides["X-Leak"] = "{client_header:" + credential + "}"
			}
			selected.HeaderOverride = transparentTestJSON(t, overrides)
			gateway := transparentTestGateway(t, selected)
			req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/unknown", strings.NewReader("opaque"))
			require.NoError(t, err)
			if credential != "none" {
				req.Header.Set(credential, "client-secret")
			}
			req.Header.Set("X-Visible", "visible")
			req.Header.Set("X-Title", "client-title")
			response, err := transparentTestClient(t).Do(req)
			require.NoError(t, err)
			response.Body.Close()
			require.Equal(t, http.StatusNoContent, response.StatusCode)
			got := <-headers
			assert.Equal(t, "Token channel-secret", got.Get("Authorization"))
			assert.Equal(t, "operator-title", got.Get("X-Title"))
			assert.Equal(t, "visible", got.Get("X-Copied"))
			assert.Empty(t, got.Get("X-Leak"))
		})
	}
}

func TestTransparentRelayUpstreamRepresentations(t *testing.T) {
	for _, status := range []int{307, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			body := []byte("provider-owned\x00\xff\r\nnot-an-error-DTO")
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/must-not-follow")
				w.Header()["X-Provider"] = []string{"a", "b"}
				w.Header().Set("Content-Type", "application/x-provider")
				w.Header().Set("Connection", "X-Hop")
				w.Header().Set("X-Hop", "private")
				w.WriteHeader(status)
				_, _ = w.Write(body)
			}))
			defer upstream.Close()
			gateway := transparentTestGateway(t, transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI))
			response, err := transparentTestClient(t).Post(gateway.URL+"/future", "application/octet-stream", strings.NewReader("raw"))
			require.NoError(t, err)
			defer response.Body.Close()
			got, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Equal(t, status, response.StatusCode)
			assert.Equal(t, body, got)
			assert.Equal(t, []string{"a", "b"}, response.Header.Values("X-Provider"))
			assert.Equal(t, "/must-not-follow", response.Header.Get("Location"))
			assert.Empty(t, response.Header.Get("X-Hop"))
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestTransparentRelaySSEFlushesWithoutParsing(t *testing.T) {
	first := ": comment\r\nevent: vendor.unknown\r\ndata: not json\r\n\r\n"
	last := "event: finish\r\ndata: [not-DONE]\r\n\r\n"
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, first)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, last)
	}))
	defer upstream.Close()
	defer unblock()
	gateway := transparentTestGateway(t, transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI))
	response, err := transparentTestClient(t).Post(gateway.URL+"/stream", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer response.Body.Close()
	got := make([]byte, len(first))
	_, err = io.ReadFull(response.Body, got)
	require.NoError(t, err)
	require.Equal(t, first, string(got)) // Upstream cannot finish until this assertion succeeds.
	unblock()
	rest, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, last, string(rest))
}

func TestTransparentRelayPartialFailureDoesNotAppendOrRetry(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, "partial-provider-body")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		panic(http.ErrAbortHandler)
	}))
	defer upstream.Close()
	defer unblock()
	gateway := transparentTestGateway(t, transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI))
	response, err := transparentTestClient(t).Post(gateway.URL+"/future", "text/plain", strings.NewReader("raw"))
	require.NoError(t, err)
	defer response.Body.Close()
	prefix := make([]byte, len("partial-provider-body"))
	_, err = io.ReadFull(response.Body, prefix)
	require.NoError(t, err)
	unblock()
	rest, err := io.ReadAll(response.Body)
	require.Error(t, err)
	assert.Equal(t, "partial-provider-body", string(append(prefix, rest...)))
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.EqualValues(t, 1, calls.Load())
}

func TestTransparentRelayPropagatesCancellation(t *testing.T) {
	canceled := make(chan struct{})
	stop := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": ready\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-stop:
		}
	}))
	defer upstream.Close()
	defer close(stop)
	gateway := transparentTestGateway(t, transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/stream", strings.NewReader("raw"))
	require.NoError(t, err)
	response, err := transparentTestClient(t).Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	cancel()
	select {
	case <-canceled:
	case <-time.After(10 * time.Second):
		t.Fatal("upstream request was not canceled")
	}
}

func TestTransparentRelayDiskBodyAndGzipWireBytes(t *testing.T) {
	previous := common.GetDiskCacheConfig()
	common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: true, ThresholdMB: 1, MaxSizeMB: 64, Path: t.TempDir()})
	defer common.SetDiskCacheConfig(previous)
	raw := bytes.Repeat([]byte("opaque payload, not a typed DTO\n"), 80000)
	var compressed bytes.Buffer
	compressor, err := gzip.NewWriterLevel(&compressed, gzip.NoCompression)
	require.NoError(t, err)
	_, err = compressor.Write(raw)
	require.NoError(t, err)
	require.NoError(t, compressor.Close())
	for _, encoded := range []bool{false, true} {
		t.Run(fmt.Sprint(encoded), func(t *testing.T) {
			wire := raw
			if encoded {
				wire = compressed.Bytes()
			}
			type observation struct {
				body     []byte
				encoding string
				files    int
				err      error
			}
			observed := make(chan observation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				files, dirErr := os.ReadDir(common.GetDiskCacheDir())
				if err == nil {
					err = dirErr
				}
				observed <- observation{body, r.Header.Get("Content-Encoding"), len(files), err}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = w.Write(compressed.Bytes())
			}))
			defer upstream.Close()
			gateway := transparentTestGateway(t, transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI))
			req, err := http.NewRequest(http.MethodPost, gateway.URL+"/opaque", bytes.NewReader(wire))
			require.NoError(t, err)
			if encoded {
				req.Header.Set("Content-Encoding", "gzip")
			}
			response, err := transparentTestClient(t).Do(req)
			require.NoError(t, err)
			responseWire, err := io.ReadAll(response.Body)
			response.Body.Close()
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			assert.Equal(t, "gzip", response.Header.Get("Content-Encoding"))
			assert.Equal(t, compressed.Bytes(), responseWire)
			got := <-observed
			require.NoError(t, got.err)
			assert.Equal(t, wire, got.body)
			if encoded {
				assert.Equal(t, "gzip", got.encoding)
			} else {
				assert.Empty(t, got.encoding)
			}
			assert.Positive(t, got.files, "large request must actually use disk storage")
			gateway.Close() // Wait for middleware cleanup, not merely the last response bytes.
			files, err := os.ReadDir(common.GetDiskCacheDir())
			require.NoError(t, err)
			assert.Empty(t, files, "body cleanup must release disk files")
		})
	}
}

func TestTransparentRelayModePrecedence(t *testing.T) {
	cases := []struct {
		name                     string
		global, excluded, legacy bool
		mode, want               dto.TransportMode
	}{
		{"legacy default", false, false, false, "", dto.TransportModeConvert},
		{"global inherited", true, false, false, dto.TransportModeInherit, dto.TransportModeBodyPassthrough},
		{"channel legacy", false, false, true, "", dto.TransportModeBodyPassthrough},
		{"excluded global", true, true, false, "", dto.TransportModeConvert},
		{"excluded channel legacy", false, true, true, dto.TransportModeInherit, dto.TransportModeConvert},
		{"explicit convert", true, false, true, dto.TransportModeConvert, dto.TransportModeConvert},
		{"explicit body beats exclusion", false, true, false, dto.TransportModeBodyPassthrough, dto.TransportModeBodyPassthrough},
		{"transparent beats exclusion", true, true, true, dto.TransportModeTransparent, dto.TransportModeTransparent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := model_setting.GlobalSettings{PassThroughRequestEnabled: tc.global}
			if tc.excluded {
				settings.PassThroughRequestExcludedChannels = []int{71}
			}
			assert.Equal(t, tc.want, settings.EffectiveTransportMode(71, dto.ChannelSettings{TransportMode: tc.mode, PassThroughBodyEnabled: tc.legacy}))
		})
	}
}

func TestTransparentRelayRejectsIncompatibleConfigurationBeforeUpstream(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.Channel)
	}{
		{"billing acknowledgement", func(c *model.Channel) {
			c.Setting = transparentTestJSON(t, dto.ChannelSettings{TransportMode: dto.TransportModeTransparent})
		}},
		{"format", func(c *model.Channel) {
			c.Setting = transparentTestJSON(t, dto.ChannelSettings{TransportMode: dto.TransportModeTransparent, TransparentBilling: "external", ForceFormat: true})
		}},
		{"system prompt", func(c *model.Channel) {
			c.Setting = transparentTestJSON(t, dto.ChannelSettings{TransportMode: dto.TransportModeTransparent, TransparentBilling: "external", SystemPrompt: "mutate"})
		}},
		{"param override", func(c *model.Channel) { c.ParamOverride = transparentTestJSON(t, map[string]any{"temperature": 1}) }},
		{"model mapping", func(c *model.Channel) { c.ModelMapping = transparentTestJSON(t, map[string]string{"a": "b"}) }},
		{"disguise", func(c *model.Channel) { c.OtherSettings = `{"disguise_as_claude_code":true}` }},
		{"disable store", func(c *model.Channel) { c.OtherSettings = `{"disable_store":true}` }},
		{"advanced custom", func(c *model.Channel) { c.OtherSettings = `{"advanced_custom":{}}` }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
			defer upstream.Close()
			selected := transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI)
			tc.mutate(selected)
			gateway := transparentTestGateway(t, selected)
			response, err := transparentTestClient(t).Post(gateway.URL+"/future", "application/octet-stream", strings.NewReader("raw"))
			require.NoError(t, err)
			response.Body.Close()
			assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			assert.Zero(t, calls.Load())
		})
	}
}

// Legacy modes still use DTO validation; only transparent mode may forward
// opaque representations. Successful legacy billing paths have separate suites.
func TestTransparentRelayDoesNotBypassLegacyValidation(t *testing.T) {
	for _, mode := range []dto.TransportMode{dto.TransportModeConvert, dto.TransportModeBodyPassthrough} {
		t.Run(string(mode), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
			defer upstream.Close()
			selected := transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI)
			selected.Setting = transparentTestJSON(t, dto.ChannelSettings{TransportMode: mode})
			gateway := transparentTestGateway(t, selected)
			response, err := transparentTestClient(t).Post(gateway.URL+"/v1/chat/completions", "application/json", strings.NewReader("not JSON"))
			require.NoError(t, err)
			response.Body.Close()
			assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			assert.Zero(t, calls.Load())
		})
	}
}

func TestTransparentRelayPreservesAuthenticationAndRouting(t *testing.T) {
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
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "unparsed provider response")
	}))
	defer upstream.Close()
	selected := transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenRouter)
	selected.Status, selected.Models, selected.Group = common.ChannelStatusEnabled, "test-model", "default"
	require.NoError(t, db.Create(selected).Error)
	router := gin.New()
	router.Use(middleware.BodyStorageCleanup(), middleware.DecompressRequestMiddleware())
	router.POST("/v1/chat/completions", middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAI) })
	for _, tc := range []struct {
		name, credential string
		status           int
	}{
		{"missing credential", "", http.StatusUnauthorized},
		{"invalid credential", "Bearer invalid", http.StatusUnauthorized},
		{"authorized selected channel", "Bearer sk-transparentclientsecret-71", http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", tc.credential)
			request.Header.Set("X-Title", "OpenCode")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			if tc.status == http.StatusCreated {
				assert.Equal(t, "unparsed provider response", response.Body.String())
			}
		})
	}
	assert.EqualValues(t, 1, calls.Load())
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 12345, user.Quota)
	assert.Equal(t, 12345, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestTransparentRelayKeepsLegacyRequestConversion(t *testing.T) {
	payload := "{ \"model\":\"foo\", \"messages\":[{\"role\":\"user\",\"content\":\"hi\"}], \"future_field\":true }\n"
	for _, mode := range []dto.TransportMode{dto.TransportModeConvert, dto.TransportModeBodyPassthrough} {
		t.Run(string(mode), func(t *testing.T) {
			upstreamBody := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				upstreamBody <- body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"provider rejected","type":"invalid_request_error"}}`)
			}))
			defer upstream.Close()
			selected := transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI)
			selected.Setting = transparentTestJSON(t, dto.ChannelSettings{TransportMode: mode})
			selected.ModelMapping = transparentTestJSON(t, map[string]string{"foo": "bar"})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
			c.Request.Header.Set("Content-Type", "application/json")
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, selected, "foo"))
			request := &dto.GeneralOpenAIRequest{}
			require.NoError(t, common.Unmarshal([]byte(payload), request))
			info, err := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAI, request, nil)
			require.NoError(t, err)
			apiErr := relay.TextHelper(c, info)
			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			body := <-upstreamBody
			if mode == dto.TransportModeBodyPassthrough {
				assert.Equal(t, payload, string(body))
			} else {
				var converted map[string]any
				require.NoError(t, common.Unmarshal(body, &converted))
				assert.Equal(t, "bar", converted["model"])
			}
			assert.Equal(t, "bar", info.UpstreamModelName)
		})
	}
}
