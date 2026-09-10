package claude

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func disguiseRelayInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:  7,
		TokenId: 11,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:      13,
			ChannelBaseUrl: "https://api.anthropic.com",
			ApiKey:         "sk-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				DisguiseAsClaudeCode: true,
			},
		},
	}
}

func disguiseGinContext(headers map[string]string) *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c
}

func TestConvertClaudeRequestDisguiseInjectsSystemAndMetadata(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "claude-sonnet-4-5",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
		System: "You are a helpful assistant.",
	}
	info := disguiseRelayInfo()

	out, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, req)
	require.NoError(t, err)
	converted, ok := out.(*dto.ClaudeRequest)
	require.True(t, ok)

	blocks := converted.ParseSystem()
	require.GreaterOrEqual(t, len(blocks), 2)
	assert.Equal(t, claudeCodeSystemPrompt, blocks[0].GetText())
	assert.Equal(t, "You are a helpful assistant.", blocks[1].GetText())

	var meta map[string]any
	require.NoError(t, common.Unmarshal(converted.Metadata, &meta))
	userID, ok := meta["user_id"].(string)
	require.True(t, ok)
	assert.True(t, validClaudeCodeUserID(userID))

	var parsed claudeCodeUserIDJSON
	require.NoError(t, common.Unmarshal([]byte(userID), &parsed))
	assert.Len(t, parsed.DeviceID, 64)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, parsed.SessionID)
}

func TestConvertClaudeRequestDisguiseDoesNotDuplicateIdentity(t *testing.T) {
	identity := dto.ClaudeMediaMessage{Type: dto.ContentTypeText}
	identity.SetText(claudeCodeSystemPrompt)
	follow := dto.ClaudeMediaMessage{Type: dto.ContentTypeText}
	follow.SetText("Keep answers short.")
	req := &dto.ClaudeRequest{
		Model:  "claude-sonnet-4-5",
		System: []dto.ClaudeMediaMessage{identity, follow},
		Metadata: json.RawMessage(
			`{"user_id":"{\"device_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"account_uuid\":\"\",\"session_id\":\"11111111-1111-4111-8111-111111111111\"}"}`,
		),
	}

	out, err := (&Adaptor{}).ConvertClaudeRequest(nil, disguiseRelayInfo(), req)
	require.NoError(t, err)
	converted := out.(*dto.ClaudeRequest)
	blocks := converted.ParseSystem()
	require.Len(t, blocks, 2)
	assert.Equal(t, claudeCodeSystemPrompt, blocks[0].GetText())

	var meta map[string]any
	require.NoError(t, common.Unmarshal(converted.Metadata, &meta))
	assert.Contains(t, meta["user_id"], "11111111-1111-4111-8111-111111111111")
}

func TestConvertClaudeRequestDisguiseConvertsMatchingStringSystemToArray(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:  "claude-sonnet-4-5",
		System: claudeCodeSystemPrompt,
	}

	out, err := (&Adaptor{}).ConvertClaudeRequest(nil, disguiseRelayInfo(), req)
	require.NoError(t, err)
	converted := out.(*dto.ClaudeRequest)
	require.False(t, converted.IsStringSystem())
	blocks := converted.ParseSystem()
	require.Len(t, blocks, 1)
	assert.Equal(t, claudeCodeSystemPrompt, blocks[0].GetText())
}

func TestConvertClaudeRequestDisguiseOffLeavesBodyUnchanged(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:  "claude-sonnet-4-5",
		System: "plain",
	}
	info := disguiseRelayInfo()
	info.ChannelOtherSettings.DisguiseAsClaudeCode = false

	out, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, req)
	require.NoError(t, err)
	converted := out.(*dto.ClaudeRequest)
	assert.Equal(t, "plain", converted.GetStringSystem())
	assert.Empty(t, converted.Metadata)
}

func TestGenerateClaudeCodeUserIDIsStablePerUserTokenChannel(t *testing.T) {
	info := disguiseRelayInfo()
	first := generateClaudeCodeUserID(info)
	second := generateClaudeCodeUserID(info)
	assert.Equal(t, first, second)

	other := disguiseRelayInfo()
	other.UserId = 99
	assert.NotEqual(t, first, generateClaudeCodeUserID(other))
}

func TestSetupRequestHeaderDisguiseInjectsClaudeCodeFingerprint(t *testing.T) {
	info := disguiseRelayInfo()
	c := disguiseGinContext(map[string]string{
		"User-Agent": "OpenAI/Python 1.40.0",
		"Accept":     "application/json",
	})
	header := http.Header{}

	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	assert.True(t, strings.HasPrefix(header.Get("User-Agent"), "claude-cli/"))
	assert.Equal(t, "cli", header.Get("X-App"))
	assert.Equal(t, claudeCodeAnthropicBeta, header.Get("anthropic-beta"))
	assert.Equal(t, "2023-06-01", header.Get("anthropic-version"))
	assert.Equal(t, "js", header.Get("X-Stainless-Lang"))
	assert.Equal(t, "node", header.Get("X-Stainless-Runtime"))
	assert.NotEmpty(t, header.Get("X-Stainless-Os"))
	assert.NotEmpty(t, header.Get("X-Stainless-Arch"))
}

func TestSetupRequestHeaderDisguiseKeepsIncomingClaudeCLIUserAgent(t *testing.T) {
	incomingUA := "claude-cli/2.1.90 (external, claude-vscode)"
	info := disguiseRelayInfo()
	c := disguiseGinContext(map[string]string{
		"User-Agent":        incomingUA,
		"X-App":             "cli",
		"anthropic-beta":    "interleaved-thinking-2025-05-14",
		"anthropic-version": "2023-06-01",
	})
	header := http.Header{}

	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	assert.Equal(t, incomingUA, header.Get("User-Agent"))
	assert.Equal(t, "interleaved-thinking-2025-05-14", header.Get("anthropic-beta"))
}

func TestSetupRequestHeaderDisguiseDoesNotOverrideAdminHeaderOverride(t *testing.T) {
	info := disguiseRelayInfo()
	info.HeadersOverride = map[string]any{
		"User-Agent": "admin-ua",
		"X-App":      "admin-app",
	}
	c := disguiseGinContext(nil)
	header := http.Header{}

	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	overrides, err := channel.ResolveHeaderOverride(info, c)
	require.NoError(t, err)
	assert.Equal(t, "admin-ua", overrides["user-agent"])
	assert.Equal(t, "admin-app", overrides["x-app"])
}

func TestSetupRequestHeaderDisguiseReplacesRuntimePassthroughUserAgent(t *testing.T) {
	info := disguiseRelayInfo()
	info.UseRuntimeHeadersOverride = true
	info.RuntimeHeadersOverride = map[string]any{
		"user-agent": "OpenAI/Python 1.40.0",
		"x-app":      "cli",
	}
	c := disguiseGinContext(map[string]string{
		"User-Agent": "OpenAI/Python 1.40.0",
	})
	header := http.Header{}

	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	overrides, err := channel.ResolveHeaderOverride(info, c)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(overrides["user-agent"], "claude-cli/"))
	assert.Equal(t, "cli", overrides["x-app"])
}

func TestGetRequestURLAppendsBetaWhenDisguiseEnabled(t *testing.T) {
	url, err := (&Adaptor{}).GetRequestURL(disguiseRelayInfo())
	require.NoError(t, err)
	assert.Contains(t, url, "beta=true")
}

func TestSetupRequestHeaderDisguiseOffLeavesClaudeCodeHeadersUnset(t *testing.T) {
	info := disguiseRelayInfo()
	info.ChannelOtherSettings.DisguiseAsClaudeCode = false
	c := disguiseGinContext(map[string]string{
		"User-Agent": "OpenAI/Python 1.40.0",
	})
	header := http.Header{}

	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	assert.Empty(t, header.Get("User-Agent"))
	assert.Empty(t, header.Get("X-App"))
}

func TestValidClaudeCodeUserIDAcceptsLegacyAndJSON(t *testing.T) {
	assert.True(t, validClaudeCodeUserID(
		"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_123e4567-e89b-12d3-a456-426614174000",
	))
	assert.True(t, validClaudeCodeUserID(
		`{"device_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account_uuid":"","session_id":"123e4567-e89b-12d3-a456-426614174000"}`,
	))
	assert.False(t, validClaudeCodeUserID("user-1"))
	assert.False(t, validClaudeCodeUserID(`{"device_id":"","session_id":"x"}`))
}
