package openai

import (
    "strings"

    relaycommon "github.com/QuantumNous/new-api/relay/common"
    "github.com/tidwall/gjson"
)

// captureUpstreamModelMetadata observes optional metadata from trusted compatible
// upstreams. Missing or invalid values leave existing observations untouched.
func captureUpstreamModelMetadata(info *relaycommon.RelayInfo, payload []byte) {
    if info == nil || len(payload) == 0 || !gjson.ValidBytes(payload) {
        return
    }
    responseModel := gjson.GetBytes(payload, "upstream_response_model")
    if responseModel.Type == gjson.String {
        if value := strings.TrimSpace(responseModel.String()); value != "" {
            info.UpstreamResponseModel = truncateModelMetadata(value)
        }
    }
    sentModel := gjson.GetBytes(payload, "upstream_sent_model")
    if sentModel.Type == gjson.String {
        if value := strings.TrimSpace(sentModel.String()); value != "" {
            info.UpstreamSentModel = truncateModelMetadata(value)
        }
    }
}

func truncateModelMetadata(value string) string {
    runes := []rune(value)
    if len(runes) > 200 {
        return string(runes[:200])
    }
    return value
}
