package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyResponsesSystemPromptIfNeeded(t *testing.T) {
	gin.SetMode(gin.TestMode)

	structuredInput := json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]`)
	tools := json.RawMessage(`[{"type":"web_search_preview"}]`)

	tests := []struct {
		name         string
		prompt       string
		override     bool
		instructions json.RawMessage
		want         json.RawMessage
		wantOverride bool
		wantErr      string
	}{
		{
			name:         "empty prompt is a no-op",
			instructions: json.RawMessage(`"CLIENT SYSTEM"`),
			want:         json.RawMessage(`"CLIENT SYSTEM"`),
		},
		{
			name:         "missing instructions writes channel prompt",
			prompt:       "CHANNEL SYSTEM",
			instructions: nil,
			want:         json.RawMessage(`"CHANNEL SYSTEM"`),
		},
		{
			name:         "null instructions writes channel prompt",
			prompt:       "CHANNEL SYSTEM",
			instructions: json.RawMessage(`null`),
			want:         json.RawMessage(`"CHANNEL SYSTEM"`),
		},
		{
			name:         "empty string instructions writes channel prompt",
			prompt:       "CHANNEL SYSTEM",
			instructions: json.RawMessage(`""`),
			want:         json.RawMessage(`"CHANNEL SYSTEM"`),
		},
		{
			name:         "existing instructions kept without override",
			prompt:       "CHANNEL SYSTEM",
			instructions: json.RawMessage(`"CLIENT SYSTEM"`),
			want:         json.RawMessage(`"CLIENT SYSTEM"`),
		},
		{
			name:         "override prepends channel prompt",
			prompt:       "CHANNEL SYSTEM",
			override:     true,
			instructions: json.RawMessage(`"CLIENT SYSTEM"`),
			want:         json.RawMessage(`"CHANNEL SYSTEM\nCLIENT SYSTEM"`),
			wantOverride: true,
		},
		{
			name:         "malformed instructions return an explicit error",
			prompt:       "CHANNEL SYSTEM",
			instructions: json.RawMessage(`{"oops"`),
			want:         json.RawMessage(`{"oops"`),
			wantErr:      "invalid responses instructions",
		},
		{
			name:         "structured instructions kept without override",
			prompt:       "CHANNEL SYSTEM",
			instructions: json.RawMessage(`[{"type":"text","text":"CLIENT SYSTEM"}]`),
			want:         json.RawMessage(`[{"type":"text","text":"CLIENT SYSTEM"}]`),
		},
		{
			name:         "structured instructions error on override",
			prompt:       "CHANNEL SYSTEM",
			override:     true,
			instructions: json.RawMessage(`[{"type":"text","text":"CLIENT SYSTEM"}]`),
			want:         json.RawMessage(`[{"type":"text","text":"CLIENT SYSTEM"}]`),
			wantErr:      "expected JSON string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			inputCopy := append(json.RawMessage(nil), structuredInput...)
			toolsCopy := append(json.RawMessage(nil), tools...)
			request := &dto.OpenAIResponsesRequest{
				Model:              "gpt-4.1",
				Input:              inputCopy,
				Instructions:       append(json.RawMessage(nil), tt.instructions...),
				Tools:              toolsCopy,
				PreviousResponseID: "resp_123",
				Reasoning:          &dto.Reasoning{Effort: "medium"},
			}
			if tt.instructions == nil {
				request.Instructions = nil
			}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelSetting: dto.ChannelSettings{
						SystemPrompt:         tt.prompt,
						SystemPromptOverride: tt.override,
					},
				},
			}

			err := applyResponsesSystemPromptIfNeeded(c, info, request)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, request.Instructions)
			assert.Equal(t, structuredInput, request.Input)
			assert.Equal(t, tools, request.Tools)
			assert.Equal(t, "resp_123", request.PreviousResponseID)
			require.NotNil(t, request.Reasoning)
			assert.Equal(t, "medium", request.Reasoning.Effort)
			assert.Equal(t, tt.wantOverride, common.GetContextKeyBool(c, constant.ContextKeySystemPromptOverride))
		})
	}

	t.Run("nil info or request is a no-op", func(t *testing.T) {
		require.NoError(t, applyResponsesSystemPromptIfNeeded(nil, nil, nil))
		request := &dto.OpenAIResponsesRequest{Instructions: json.RawMessage(`"CLIENT SYSTEM"`)}
		require.NoError(t, applyResponsesSystemPromptIfNeeded(nil, &relaycommon.RelayInfo{}, request))
		assert.Equal(t, json.RawMessage(`"CLIENT SYSTEM"`), request.Instructions)
	})
}

func TestApplyResponsesSystemPromptIfNeededRetryDoesNotAccumulate(t *testing.T) {
	original := &dto.OpenAIResponsesRequest{
		Model:        "gpt-4.1",
		Input:        json.RawMessage(`"hello"`),
		Instructions: json.RawMessage(`"CLIENT SYSTEM"`),
	}

	applyWithPrompt := func(prompt string) json.RawMessage {
		t.Helper()
		request, err := common.DeepCopy(original)
		require.NoError(t, err)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{SystemPrompt: prompt, SystemPromptOverride: true},
			},
		}
		require.NoError(t, applyResponsesSystemPromptIfNeeded(c, info, request))
		assert.Equal(t, json.RawMessage(`"CLIENT SYSTEM"`), original.Instructions)
		assert.Equal(t, json.RawMessage(`"hello"`), original.Input)
		return request.Instructions
	}

	assert.Equal(t, json.RawMessage(`"CHANNEL A\nCLIENT SYSTEM"`), applyWithPrompt("CHANNEL A"))
	assert.Equal(t, json.RawMessage(`"CHANNEL B\nCLIENT SYSTEM"`), applyWithPrompt("CHANNEL B"))
}

func TestResponsesHelperSystemPromptPassthroughAndExclusion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const channelID = 71
	rawBody := `{"model":"gpt-4.1","input":"hello"}`

	tests := []struct {
		name         string
		passthrough  bool
		excluded     []int
		wantInjected bool
		wantRawBody  bool
		wantBehavior model_setting.RelayBehavior
	}{
		{
			name:         "passthrough off injects instructions",
			wantInjected: true,
			wantBehavior: model_setting.RelayBehaviorStandard,
		},
		{
			name:         "passthrough on without exclusion keeps raw body",
			passthrough:  true,
			wantRawBody:  true,
			wantBehavior: model_setting.RelayBehaviorBodyPassthrough,
		},
		{
			name:         "passthrough on with excluded channel injects instructions",
			passthrough:  true,
			excluded:     []int{channelID},
			wantInjected: true,
			wantBehavior: model_setting.RelayBehaviorStandard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withGlobalPassthrough(t, tt.passthrough, tt.excluded)
			settings := dto.ChannelSettings{SystemPrompt: "CHANNEL SYSTEM"}
			assert.Equal(t, tt.wantBehavior, model_setting.ResolveRelayBehavior(channelID, settings, dto.ChannelOtherSettings{}, model_setting.GetGlobalSettings()))

			upstream, captured := captureResponsesUpstream(t)
			c, info := newResponsesHelperFixture(t, responsesHelperFixture{
				path:      "/v1/responses",
				rawBody:   rawBody,
				channelID: channelID,
				settings:  settings,
				baseURL:   upstream.URL,
			})
			originalInstructions := append(json.RawMessage(nil), info.Request.(*dto.OpenAIResponsesRequest).Instructions...)

			apiErr := ResponsesHelper(c, info)
			require.NotNil(t, apiErr)
			require.EqualValues(t, 1, captured.calls())
			assert.Equal(t, originalInstructions, info.Request.(*dto.OpenAIResponsesRequest).Instructions)

			if tt.wantRawBody {
				assert.Equal(t, rawBody, string(captured.body()))
				return
			}

			var outbound map[string]any
			require.NoError(t, common.Unmarshal(captured.body(), &outbound))
			if tt.wantInjected {
				assert.Equal(t, "CHANNEL SYSTEM", outbound["instructions"])
			} else {
				_, exists := outbound["instructions"]
				assert.False(t, exists)
			}
			assert.Equal(t, "hello", outbound["input"])
			assert.Equal(t, "gpt-4.1", outbound["model"])
		})
	}
}

func TestResponsesHelperSystemPromptPreservesFieldsAndParamOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withGlobalPassthrough(t, false, nil)

	input := json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]`)
	tools := json.RawMessage(`[{"type":"web_search_preview"}]`)
	rawBody := `{"model":"gpt-4.1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[{"type":"web_search_preview"}],"reasoning":{"effort":"medium"},"previous_response_id":"resp_123","temperature":1}`

	upstream, captured := captureResponsesUpstream(t)
	c, info := newResponsesHelperFixture(t, responsesHelperFixture{
		path:          "/v1/responses",
		rawBody:       rawBody,
		channelID:     71,
		settings:      dto.ChannelSettings{SystemPrompt: "CHANNEL SYSTEM"},
		paramOverride: map[string]any{"temperature": 0.2},
		baseURL:       upstream.URL,
	})
	original := info.Request.(*dto.OpenAIResponsesRequest)
	require.Equal(t, input, original.Input)
	require.Equal(t, tools, original.Tools)

	apiErr := ResponsesHelper(c, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, input, original.Input)
	assert.Equal(t, tools, original.Tools)
	assert.Equal(t, "resp_123", original.PreviousResponseID)
	require.NotNil(t, original.Reasoning)
	assert.Equal(t, "medium", original.Reasoning.Effort)
	assert.Nil(t, original.Instructions)

	var outbound map[string]any
	require.NoError(t, common.Unmarshal(captured.body(), &outbound))
	assert.Equal(t, "CHANNEL SYSTEM", outbound["instructions"])
	assert.Equal(t, 0.2, outbound["temperature"])
	assert.Equal(t, "resp_123", outbound["previous_response_id"])
	assert.JSONEq(t, string(original.Input), string(mustRawJSON(t, outbound["input"])))
	assert.JSONEq(t, string(original.Tools), string(mustRawJSON(t, outbound["tools"])))
	reasoning, ok := outbound["reasoning"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "medium", reasoning["effort"])
}

func TestResponsesHelperSystemPromptRetryUsesOriginalRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withGlobalPassthrough(t, true, []int{8, 9})

	rawBody := `{"model":"gpt-4.1","input":"hello","instructions":"CLIENT SYSTEM"}`
	original := &dto.OpenAIResponsesRequest{
		Model:        "gpt-4.1",
		Input:        json.RawMessage(`"hello"`),
		Instructions: json.RawMessage(`"CLIENT SYSTEM"`),
	}
	info := &relaycommon.RelayInfo{
		Request:         original,
		OriginModelName: "gpt-4.1",
		RelayMode:       relayconstant.RelayModeResponses,
		RequestURLPath:  "/v1/responses",
	}

	attempt := func(channelID int, prompt string) map[string]any {
		t.Helper()
		upstream, captured := captureResponsesUpstream(t)
		c, _ := newResponsesHelperFixture(t, responsesHelperFixture{
			path:      "/v1/responses",
			rawBody:   rawBody,
			channelID: channelID,
			settings:  dto.ChannelSettings{SystemPrompt: prompt, SystemPromptOverride: true},
			baseURL:   upstream.URL,
		})
		apiErr := ResponsesHelper(c, info)
		require.NotNil(t, apiErr)
		assert.Equal(t, json.RawMessage(`"CLIENT SYSTEM"`), original.Instructions)
		var outbound map[string]any
		require.NoError(t, common.Unmarshal(captured.body(), &outbound))
		return outbound
	}

	first := attempt(8, "CHANNEL A")
	second := attempt(9, "CHANNEL B")
	assert.Equal(t, "CHANNEL A\nCLIENT SYSTEM", first["instructions"])
	assert.Equal(t, "CHANNEL B\nCLIENT SYSTEM", second["instructions"])
	assert.NotContains(t, second["instructions"], "CHANNEL A")
	assert.Equal(t, "hello", second["input"])
}

func TestResponsesHelperSystemPromptMalformedInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withGlobalPassthrough(t, false, nil)

	upstream, captured := captureResponsesUpstream(t)
	c, info := newResponsesHelperFixture(t, responsesHelperFixture{
		path:      "/v1/responses",
		rawBody:   `{"model":"gpt-4.1","input":"hello"}`,
		channelID: 71,
		settings:  dto.ChannelSettings{SystemPrompt: "CHANNEL SYSTEM"},
		baseURL:   upstream.URL,
	})
	original := info.Request.(*dto.OpenAIResponsesRequest)
	original.Instructions = json.RawMessage(`{"oops"`)

	apiErr := ResponsesHelper(c, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeConvertRequestFailed, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Contains(t, apiErr.Error(), "invalid responses instructions")
	assert.EqualValues(t, 0, captured.calls())
	assert.Equal(t, json.RawMessage(`{"oops"`), original.Instructions)
	assert.NotContains(t, apiErr.Error(), `{"oops"`+"CHANNEL")
}

func TestResponsesHelperCompactAppliesSystemPromptToInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withGlobalPassthrough(t, false, nil)

	rawBody := `{"model":"gpt-4.1","input":"hello","instructions":"CLIENT SYSTEM"}`
	upstream, captured := captureResponsesUpstream(t)
	c, info := newResponsesHelperFixture(t, responsesHelperFixture{
		path:      "/v1/responses/compact",
		rawBody:   rawBody,
		channelID: 71,
		settings:  dto.ChannelSettings{SystemPrompt: "CHANNEL SYSTEM", SystemPromptOverride: true},
		baseURL:   upstream.URL,
		compact:   true,
	})
	original := info.Request.(*dto.OpenAIResponsesCompactionRequest)

	apiErr := ResponsesHelper(c, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, json.RawMessage(`"CLIENT SYSTEM"`), original.Instructions)
	assert.Equal(t, json.RawMessage(`"hello"`), original.Input)

	var outbound map[string]any
	require.NoError(t, common.Unmarshal(captured.body(), &outbound))
	assert.Equal(t, "CHANNEL SYSTEM\nCLIENT SYSTEM", outbound["instructions"])
	assert.Equal(t, "hello", outbound["input"])
	assert.NotContains(t, outbound, "tools")
}

func TestTransparentRelayStillRejectsResponsesSystemPrompt(t *testing.T) {
	settings := dto.ChannelSettings{
		TransparentRelay:   true,
		TransparentBilling: "external",
		SystemPrompt:       "CHANNEL SYSTEM",
	}
	require.Error(t, settings.ValidateTransparentRelay())
	assert.Equal(
		t,
		model_setting.RelayBehaviorTransparent,
		model_setting.ResolveRelayBehavior(71, settings, dto.ChannelOtherSettings{}, model_setting.GetGlobalSettings()),
	)
}

type responsesHelperFixture struct {
	path          string
	rawBody       string
	channelID     int
	settings      dto.ChannelSettings
	paramOverride map[string]any
	baseURL       string
	compact       bool
}

func newResponsesHelperFixture(t *testing.T, fixture responsesHelperFixture) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.rawBody))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyChannelId, fixture.channelID)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, fixture.settings)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, fixture.baseURL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "sk-test")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-4.1")
	if fixture.paramOverride != nil {
		common.SetContextKey(c, constant.ContextKeyChannelParamOverride, fixture.paramOverride)
	}

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-4.1",
		RequestURLPath:  fixture.path,
		RelayMode:       relayconstant.RelayModeResponses,
	}
	if fixture.compact {
		var request dto.OpenAIResponsesCompactionRequest
		require.NoError(t, common.Unmarshal([]byte(fixture.rawBody), &request))
		info.Request = &request
		info.RelayMode = relayconstant.RelayModeResponsesCompact
		return c, info
	}
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal([]byte(fixture.rawBody), &request))
	info.Request = &request
	return c, info
}

func withGlobalPassthrough(t *testing.T, enabled bool, excluded []int) {
	t.Helper()
	global := model_setting.GetGlobalSettings()
	previous := *global
	t.Cleanup(func() { *global = previous })
	global.PassThroughRequestEnabled = enabled
	global.PassThroughRequestExcludedChannels = excluded
}

type capturedUpstream struct {
	mu      sync.Mutex
	payload []byte
	n       int
}

func (c *capturedUpstream) body() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.payload...)
}

func (c *capturedUpstream) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func captureResponsesUpstream(t *testing.T) (*httptest.Server, *capturedUpstream) {
	t.Helper()
	captured := &capturedUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		captured.mu.Lock()
		captured.payload = body
		captured.n++
		captured.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"provider rejected","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := common.Marshal(value)
	require.NoError(t, err)
	return raw
}
