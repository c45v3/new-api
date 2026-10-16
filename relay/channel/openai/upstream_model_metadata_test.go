package openai

import (
    "strings"
    "testing"

    relaycommon "github.com/QuantumNous/new-api/relay/common"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestCaptureUpstreamModelMetadata(t *testing.T) {
    info := &relaycommon.RelayInfo{OriginModelName: "logical"}
    captureUpstreamModelMetadata(info, []byte(`{"model":"logical","upstream_sent_model":"gemini-3.8-flash-high","upstream_response_model":"gemini-3.8-flash-exp-a"}`))
    require.Equal(t, "gemini-3.8-flash-high", info.UpstreamSentModel)
    require.Equal(t, "gemini-3.8-flash-exp-a", info.UpstreamResponseModel)
    assert.Equal(t, "logical", info.OriginModelName)

    captureUpstreamModelMetadata(info, []byte(`{"choices":[{"delta":{"content":"hello"}}]}`))
    assert.Equal(t, "gemini-3.8-flash-exp-a", info.UpstreamResponseModel)

    captureUpstreamModelMetadata(info, []byte(`{"upstream_response_model":42}`))
    captureUpstreamModelMetadata(info, []byte(`{"upstream_response_model":"bad"`))
    assert.Equal(t, "gemini-3.8-flash-exp-a", info.UpstreamResponseModel)

    captureUpstreamModelMetadata(info, []byte(`{"upstream_response_model":"gemini-3.8-flash-exp-b"}`))
    assert.Equal(t, "gemini-3.8-flash-exp-b", info.UpstreamResponseModel)
}

func TestCaptureUpstreamModelMetadataLengthLimit(t *testing.T) {
    info := &relaycommon.RelayInfo{}
    payload := `{"upstream_response_model":"` + strings.Repeat("x", 250) + `"}`
    captureUpstreamModelMetadata(info, []byte(payload))
    assert.Len(t, info.UpstreamResponseModel, 200)
}
