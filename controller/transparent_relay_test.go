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
	hostdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/klauspost/compress/zstd"
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
	return &model.Channel{Id: 71, Type: kind, Key: "channel-secret", BaseURL: &base, Setting: transparentTestJSON(t, dto.ChannelSettings{TransparentRelay: true, TransparentBilling: "external"})}
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

func transparentTestEncode(t *testing.T, encoding string, raw []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var writer io.WriteCloser
	switch encoding {
	case "gzip":
		var err error
		writer, err = gzip.NewWriterLevel(&buffer, gzip.NoCompression)
		require.NoError(t, err)
	case "br":
		writer = brotli.NewWriterLevel(&buffer, 0)
	case "zstd":
		var err error
		writer, err = zstd.NewWriter(&buffer, zstd.WithEncoderLevel(zstd.SpeedFastest))
		require.NoError(t, err)
	default:
		return raw
	}
	_, err := writer.Write(raw)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buffer.Bytes()
}

func TestTransparentRelayDiskBodyAndCompressedWireBytes(t *testing.T) {
	previous := common.GetDiskCacheConfig()
	common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: true, ThresholdMB: 1, MaxSizeMB: 64, Path: t.TempDir()})
	defer common.SetDiskCacheConfig(previous)
	raw := bytes.Repeat([]byte("opaque payload, not a typed DTO\n"), 80000)
	for _, encoding := range []string{"", "gzip", "br", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			wire := transparentTestEncode(t, encoding, raw)
			type observation struct {
				body     []byte
				encoding string
				err      error
			}
			observed := make(chan observation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if encoding == "" || encoding == "gzip" {
					files, diskErr := os.ReadDir(common.GetDiskCacheDir())
					assert.NoError(t, diskErr)
					assert.NotEmpty(t, files, "large wire representation must use disk storage")
				}
				observed <- observation{body, r.Header.Get("Content-Encoding"), err}
				w.Header().Set("Content-Type", "application/octet-stream")
				if encoding != "" {
					w.Header().Set("Content-Encoding", encoding)
				}
				_, _ = w.Write(wire)
			}))
			defer upstream.Close()
			gateway := transparentTestGateway(t, transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI))
			req, err := http.NewRequest(http.MethodPost, gateway.URL+"/opaque", bytes.NewReader(wire))
			require.NoError(t, err)
			if encoding != "" {
				req.Header.Set("Content-Encoding", encoding)
			}
			response, err := transparentTestClient(t).Do(req)
			require.NoError(t, err)
			responseWire, err := io.ReadAll(response.Body)
			response.Body.Close()
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			assert.Equal(t, encoding, response.Header.Get("Content-Encoding"))
			assert.Equal(t, wire, responseWire)
			got := <-observed
			require.NoError(t, got.err)
			assert.Equal(t, wire, got.body)
			assert.Equal(t, encoding, got.encoding)
			gateway.Close()
			files, err := os.ReadDir(common.GetDiskCacheDir())
			require.NoError(t, err)
			assert.Empty(t, files, "body cleanup must release disk files")
		})
	}
}

func TestTransparentRelayBehaviorPrecedence(t *testing.T) {
	cases := []struct {
		name                                            string
		global, excluded, legacy, transparent, disguise bool
		want                                            model_setting.RelayBehavior
	}{
		{"standard", false, false, false, false, false, model_setting.RelayBehaviorStandard},
		{"global body", true, false, false, false, false, model_setting.RelayBehaviorBodyPassthrough},
		{"excluded global", true, true, false, false, false, model_setting.RelayBehaviorStandard},
		{"legacy boolean ignored", false, false, true, false, false, model_setting.RelayBehaviorStandard},
		{"legacy cannot override exclusion", true, true, true, false, false, model_setting.RelayBehaviorStandard},
		{"transparent overrides global", true, false, false, true, false, model_setting.RelayBehaviorTransparent},
		{"transparent overrides exclusion", true, true, false, true, false, model_setting.RelayBehaviorTransparent},
		{"disguise overrides global", true, false, false, false, true, model_setting.RelayBehaviorClaudeCode},
		{"disguise overrides transparent", true, true, true, true, true, model_setting.RelayBehaviorClaudeCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			global := model_setting.GlobalSettings{PassThroughRequestEnabled: tc.global}
			if tc.excluded {
				global.PassThroughRequestExcludedChannels = []int{71}
			}
			settings := dto.ChannelSettings{TransparentRelay: tc.transparent, PassThroughBodyEnabled: tc.legacy}
			other := dto.ChannelOtherSettings{DisguiseAsClaudeCode: tc.disguise}
			assert.Equal(t, tc.want, model_setting.ResolveRelayBehavior(71, settings, other, &global))
		})
	}
}

func TestTransparentRelayRejectsIncompatibleConfigurationBeforeUpstream(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.Channel)
	}{
		{"billing acknowledgement", func(c *model.Channel) {
			c.Setting = transparentTestJSON(t, dto.ChannelSettings{TransparentRelay: true})
		}},
		{"format", func(c *model.Channel) {
			c.Setting = transparentTestJSON(t, dto.ChannelSettings{TransparentRelay: true, TransparentBilling: "external", ForceFormat: true})
		}},
		{"system prompt", func(c *model.Channel) {
			c.Setting = transparentTestJSON(t, dto.ChannelSettings{TransparentRelay: true, TransparentBilling: "external", SystemPrompt: "mutate"})
		}},
		{"param override", func(c *model.Channel) { c.ParamOverride = transparentTestJSON(t, map[string]any{"temperature": 1}) }},
		{"model mapping", func(c *model.Channel) { c.ModelMapping = transparentTestJSON(t, map[string]string{"a": "b"}) }},
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

func TestTransparentRelayDoesNotBypassMutableValidation(t *testing.T) {
	global := model_setting.GetGlobalSettings()
	previous := *global
	t.Cleanup(func() { *global = previous })
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			*global = model_setting.GlobalSettings{PassThroughRequestEnabled: passthrough}
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
			defer upstream.Close()
			selected := transparentTestChannel(t, upstream.URL, constant.ChannelTypeOpenAI)
			selected.Setting = transparentTestJSON(t, dto.ChannelSettings{})
			gateway := transparentTestGateway(t, selected)
			response, err := transparentTestClient(t).Post(gateway.URL+"/v1/chat/completions", "application/json", strings.NewReader("not JSON"))
			require.NoError(t, err)
			response.Body.Close()
			assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			assert.Zero(t, calls.Load())
		})
	}
}

func TestTransparentRelayKeepsLegacyRequestConversion(t *testing.T) {
	global := model_setting.GetGlobalSettings()
	previous := *global
	t.Cleanup(func() { *global = previous })
	payload := "{ \"model\":\"foo\", \"messages\":[{\"role\":\"user\",\"content\":\"hi\"}], \"future_field\":true }\n"
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			*global = model_setting.GlobalSettings{PassThroughRequestEnabled: passthrough}
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
			selected.Setting = transparentTestJSON(t, dto.ChannelSettings{})
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
			if passthrough {
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

func TestTransparentRelaySaveValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     int
		disguise bool
		valid    bool
	}{
		{"supported", constant.ChannelTypeOpenRouter, false, true},
		{"unsupported", constant.ChannelTypeAws, false, false},
		{"custom", constant.ChannelTypeCustom, false, false},
		{"conflict", constant.ChannelTypeAnthropic, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := transparentTestChannel(t, "https://example.com", tc.kind)
			if tc.disguise {
				channel.OtherSettings = `{"disguise_as_claude_code":true}`
			}
			if tc.valid {
				require.NoError(t, channel.ValidateSettings())
			} else {
				require.Error(t, channel.ValidateSettings())
			}
		})
	}
}

func TestTransparentRelayLegacySettingsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, raw   string
		transparent bool
	}{
		{"transparent", `{"transport_mode":"transparent"}`, true},
		{"explicit false wins", `{"transport_mode":"transparent","transparent_relay":false}`, false},
		{"convert", `{"transport_mode":"convert"}`, false},
		{"inherit", `{"transport_mode":"inherit"}`, false},
		{"body", `{"transport_mode":"body_passthrough","pass_through_body_enabled":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var settings map[string]any
			require.NoError(t, common.UnmarshalJsonStr(tc.raw, &settings))
			settings["transparent_billing"] = "external"
			settings["future_setting"] = map[string]any{"enabled": true, "value": "preserved"}
			channel := &model.Channel{Type: constant.ChannelTypeOpenRouter, Setting: transparentTestJSON(t, settings)}
			assert.Equal(t, tc.transparent, channel.GetSetting().TransparentRelay)
			for _, enabled := range []bool{false, true} {
				global := model_setting.GlobalSettings{PassThroughRequestEnabled: enabled}
				want := model_setting.RelayBehaviorStandard
				if enabled {
					want = model_setting.RelayBehaviorBodyPassthrough
				}
				if tc.transparent {
					want = model_setting.RelayBehaviorTransparent
				}
				assert.Equal(t, want, model_setting.ResolveRelayBehavior(71, channel.GetSetting(), dto.ChannelOtherSettings{}, &global))
			}
			require.NoError(t, channel.ValidateSettings())
			var saved map[string]any
			require.NoError(t, common.UnmarshalJsonStr(*channel.Setting, &saved))
			assert.NotContains(t, saved, "transport_mode")
			assert.Equal(t, settings["future_setting"], saved["future_setting"])
			assert.Equal(t, tc.transparent, channel.GetSetting().TransparentRelay)
		})
	}
}

func TestMutableRelayReleasesCompressedStorageBeforeProcessing(t *testing.T) {
	previousDB, previousCache := model.DB, common.MemoryCacheEnabled
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/storage.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB, common.MemoryCacheEnabled = db, false
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	global := model_setting.GetGlobalSettings()
	previousGlobal, previousDisk := *global, common.GetDiskCacheConfig()
	t.Cleanup(func() {
		model.DB, common.MemoryCacheEnabled = previousDB, previousCache
		common.SetMainDatabaseType(previousType)
		*global = previousGlobal
		common.SetDiskCacheConfig(previousDisk)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})
	common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: true, ThresholdMB: 1, MaxSizeMB: 64, Path: t.TempDir()})
	channel := transparentTestChannel(t, "https://example.com", constant.ChannelTypeOpenAI)
	channel.Setting = transparentTestJSON(t, dto.ChannelSettings{})
	channel.Status = common.ChannelStatusEnabled
	channel.Models = "test-model"
	channel.Group = "default"
	require.NoError(t, db.Create(channel).Error)
	// Valid routing JSON but invalid typed messages: Relay must reject it without
	// reaching billing, after Distributor has already released the wire storage.
	payload := []byte(`{"model":"test-model","messages":false,"padding":"` + strings.Repeat("x", 2<<20) + `"}`)
	for _, passthrough := range []bool{false, true} {
		*global = model_setting.GlobalSettings{PassThroughRequestEnabled: passthrough}
		for _, encoding := range []string{"gzip", "br", "zstd"} {
			t.Run(fmt.Sprintf("%t/%s", passthrough, encoding), func(t *testing.T) {
				var original common.BodyStorage
				var originalFiles []os.DirEntry
				router := gin.New()
				router.Use(middleware.BodyStorageCleanup(), middleware.DecompressRequestMiddleware())
				router.Use(func(c *gin.Context) {
					value, exists := c.Get(common.KeyOriginalBodyStorage)
					require.True(t, exists)
					var ok bool
					original, ok = value.(common.BodyStorage)
					require.True(t, ok)
					originalFiles, err = os.ReadDir(common.GetDiskCacheDir())
					require.NoError(t, err)
					if encoding == "gzip" {
						require.True(t, original.IsDisk())
						require.NotEmpty(t, originalFiles)
					}
					service.GetChannelConstraints(c).AddPin(hostdto.ChannelPin{ChannelId: channel.Id, Source: hostdto.PinSourceToken, Rank: hostdto.PinRankToken, RetryMode: hostdto.PinRetrySingleAttempt})
					c.Next()
				})
				reached := false
				router.POST("/v1/chat/completions", middleware.Distribute(), func(c *gin.Context) {
					reached = true
					_, err := original.NewReader()
					require.ErrorIs(t, err, common.ErrStorageClosed, "release must happen before mutable Relay starts")
					value, _ := c.Get(common.KeyOriginalBodyStorage)
					assert.Nil(t, value)
					assert.Empty(t, c.GetString(common.KeyOriginalContentEncoding))
					for _, file := range originalFiles {
						_, err := os.Stat(common.GetDiskCacheDir() + "/" + file.Name())
						assert.ErrorIs(t, err, os.ErrNotExist)
					}
					decoded, err := common.GetBodyStorage(c)
					require.NoError(t, err)
					body, err := decoded.Bytes()
					require.NoError(t, err)
					assert.Equal(t, payload, body)
					Relay(c, types.RelayFormatOpenAI)
				})
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(transparentTestEncode(t, encoding, payload)))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Content-Encoding", encoding)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				require.True(t, reached, response.Body.String())
				assert.Equal(t, http.StatusBadRequest, response.Code)
				files, err := os.ReadDir(common.GetDiskCacheDir())
				require.NoError(t, err)
				assert.Empty(t, files)
			})
		}
	}
}

func TestMutableRelayRetriesPastTransparentChannel(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousCache, previousRedis, previousMaster := common.MemoryCacheEnabled, common.RedisEnabled, common.IsMasterNode
	previousRetry, previousCount, previousErrorLog := common.RetryTimes, constant.CountToken, constant.ErrorLogEnabled
	previousRatios := ratio_setting.ModelRatio2JSONString()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	previousFree := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	previousRetryCodes := operation_setting.AutomaticRetryStatusCodeRanges
	global := model_setting.GetGlobalSettings()
	previousGlobal := *global
	t.Setenv("LOG_SQL_DSN", "")
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/retry.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled, common.IsMasterNode = false, false
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() {
		model.DB, common.MemoryCacheEnabled = previousDB, previousCache
		common.SetMainDatabaseType(previousType)
		require.NoError(t, model.InitLogDB())
		model.LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogType)
		common.RedisEnabled, common.IsMasterNode = previousRedis, previousMaster
		common.RetryTimes, constant.CountToken, constant.ErrorLogEnabled = previousRetry, previousCount, previousErrorLog
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = previousFree
		operation_setting.AutomaticRetryStatusCodeRanges = previousRetryCodes
		*global = previousGlobal
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		if previousCache && previousDB != nil {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})
	common.RetryTimes, constant.CountToken, constant.ErrorLogEnabled = 1, false, false
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"transparent-retry":0}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	var calls [3]atomic.Int32
	var channels [3]*model.Channel
	for index, name := range []string{"standard-a", "transparent-b", "standard-c"} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls[index].Add(1)
			w.Header().Set("Content-Type", "application/json")
			status := http.StatusBadRequest
			if index == 0 {
				status = http.StatusServiceUnavailable
			}
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":"upstream_error"}}`, name)
		}))
		t.Cleanup(upstream.Close)
		channel := &model.Channel{
			Id: index + 1, Type: constant.ChannelTypeOpenAI, Name: name,
			Key: "channel-secret", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled,
			Models: "transparent-retry", Group: "default", AutoBan: common.GetPointer(0),
			Priority: common.GetPointer(int64(30 - index*10)), Weight: common.GetPointer(uint(100)),
			Setting: transparentTestJSON(t, dto.ChannelSettings{TransparentRelay: index == 1, TransparentBilling: "external"}),
		}
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group: "default", Model: "transparent-retry", ChannelId: channel.Id,
			Enabled: true, Priority: channel.Priority, Weight: 100,
		}).Error)
		channels[index] = channel
	}
	for _, cached := range []bool{false, true} {
		common.MemoryCacheEnabled = cached
		if cached {
			model.InitChannelCache()
		}
		for _, passthrough := range []bool{false, true} {
			t.Run(fmt.Sprintf("cache=%t/body=%t", cached, passthrough), func(t *testing.T) {
				*global = model_setting.GlobalSettings{PassThroughRequestEnabled: passthrough}
				for i := range calls {
					calls[i].Store(0)
				}
				router := gin.New()
				router.Use(middleware.BodyStorageCleanup())
				router.POST("/v1/chat/completions", func(c *gin.Context) {
					c.Set(string(constant.ContextKeyUserGroup), "default")
					c.Set(string(constant.ContextKeyUsingGroup), "default")
					c.Set(string(constant.ContextKeyTokenGroup), "default")
					require.Nil(t, middleware.SetupContextForSelectedChannel(c, channels[0], "transparent-retry"))
					Relay(c, types.RelayFormatOpenAI)
				})
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"transparent-retry","messages":[{"role":"user","content":"hello"}]}`))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
				assert.Contains(t, response.Body.String(), "standard-c")
				assert.EqualValues(t, 1, calls[0].Load())
				assert.Zero(t, calls[1].Load(), "transparent channel must not consume the only retry")
				assert.EqualValues(t, 1, calls[2].Load())
			})
		}
	}
}
