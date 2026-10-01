package codex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRequestURLAlphaSearch(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeCodex,
			ChannelBaseUrl: "https://chatgpt.com",
		},
		RelayMode: relayconstant.RelayModeAlphaSearch,
	}

	url, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://chatgpt.com/backend-api/codex/alpha/search", url)
}

// The Codex backend rejects these fields, so the adaptor clears them rather
// than forwarding what the client sent.
func TestConvertOpenAIResponsesRequestDropsPenalties(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCodex},
		RelayMode:   relayconstant.RelayModeResponses,
	}

	converted, err := adaptor.ConvertOpenAIResponsesRequest(nil, info, dto.OpenAIResponsesRequest{
		Model:            "gpt-5-codex",
		Input:            json.RawMessage(`"hello"`),
		MaxOutputTokens:  lo.ToPtr(uint(128)),
		Temperature:      lo.ToPtr(1.0),
		FrequencyPenalty: json.RawMessage(`1.5`),
		PresencePenalty:  json.RawMessage(`1.5`),
	})
	require.NoError(t, err)

	request, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok)
	assert.Nil(t, request.MaxOutputTokens)
	assert.Nil(t, request.Temperature)
	assert.Nil(t, request.FrequencyPenalty)
	assert.Nil(t, request.PresencePenalty)
}

func TestConvertOpenAIResponsesRequestDoesNotReapplySystemPrompt(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelSetting: dto.ChannelSettings{
				SystemPrompt:         "CHANNEL SYSTEM",
				SystemPromptOverride: true,
			},
		},
		RelayMode: relayconstant.RelayModeResponses,
	}

	converted, err := adaptor.ConvertOpenAIResponsesRequest(nil, info, dto.OpenAIResponsesRequest{
		Model:        "gpt-5-codex",
		Input:        json.RawMessage(`"hello"`),
		Instructions: json.RawMessage(`"CLIENT SYSTEM"`),
	})
	require.NoError(t, err)
	request, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok)
	assert.Equal(t, json.RawMessage(`"CLIENT SYSTEM"`), request.Instructions)
}

func TestSetupRequestHeaderFillsThreadIDFromAffinity(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(CacheAffinityContextKey, "affinity-key")

	headers := http.Header{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: `{"access_token":"token","account_id":"account"}`,
		},
	}

	err := (&Adaptor{}).SetupRequestHeader(c, &headers, info)
	require.NoError(t, err)
	assert.Equal(t, "affinity-key", headers.Get("session-id"))
	assert.Equal(t, "affinity-key", headers.Get("thread-id"))
}

func TestSetupRequestHeaderPreservesExplicitThreadID(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(CacheAffinityContextKey, "affinity-key")

	headers := http.Header{}
	headers.Set("thread-id", "client-thread")
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: `{"access_token":"token","account_id":"account"}`,
		},
	}

	err := (&Adaptor{}).SetupRequestHeader(c, &headers, info)
	require.NoError(t, err)
	assert.Equal(t, "affinity-key", headers.Get("session-id"))
	assert.Equal(t, "client-thread", headers.Get("thread-id"))
}

func TestSetupRequestHeaderEnablesResponsesLiteForLunaOnly(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected string
	}{
		{name: "luna", model: "gpt-6-luna", expected: "true"},
		{name: "sol", model: "gpt-6.1-sol", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

			headers := http.Header{}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ApiKey:            `{"access_token":"token","account_id":"account"}`,
					UpstreamModelName: tt.model,
				},
			}

			err := (&Adaptor{}).SetupRequestHeader(c, &headers, info)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, headers.Get(responsesLiteHeader))
		})
	}
}
