package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func prepareCodexCacheAffinity(c *gin.Context, info *relaycommon.RelayInfo, request *dto.OpenAIResponsesRequest) {
	if c == nil || info == nil || request == nil || info.ApiType != constant.APITypeCodex {
		return
	}

	affinityKey, source := resolveCodexCacheAffinity(c, info, request)
	if affinityKey == "" {
		logger.LogDebug(c, "[codex-cache] no stable affinity key available")
		return
	}

	c.Set(codex.CacheAffinityContextKey, affinityKey)

	if !info.IsPassThroughEnabled() && codex.UsesResponsesLite(info) &&
		(source == "stable-prefix" || source == "client-scope") {
		if patchedInput, changed, err := addCodexStablePrefixBreakpoint(request.Input); err == nil && changed {
			request.Input = patchedInput
			logger.LogDebug(c, "[codex-cache] added explicit breakpoint to stable user prefix")
		}
	}

	promptCacheKey := decodePromptCacheKey(request.PromptCacheKey)
	if promptCacheKey == "" && !info.IsPassThroughEnabled() {
		encoded, err := json.Marshal(affinityKey)
		if err == nil {
			request.PromptCacheKey = encoded
		}
	}

	logger.LogDebug(c,
		"[codex-cache] affinity source=%s key=%q prompt_cache_key=%q session-id=%q thread-id=%q passthrough=%t",
		source,
		affinityKey,
		promptCacheKey,
		c.GetHeader("session-id"),
		c.GetHeader("thread-id"),
		info.IsPassThroughEnabled(),
	)
}

func resolveCodexCacheAffinity(c *gin.Context, info *relaycommon.RelayInfo, request *dto.OpenAIResponsesRequest) (string, string) {
	if c != nil {
		if sessionID := strings.TrimSpace(c.GetHeader("session-id")); sessionID != "" {
			return sessionID, "session-id"
		}
	}

	if promptCacheKey := decodePromptCacheKey(request.PromptCacheKey); promptCacheKey != "" {
		return promptCacheKey, "prompt_cache_key"
	}

	if c != nil {
		if threadID := strings.TrimSpace(c.GetHeader("thread-id")); threadID != "" {
			return threadID, "thread-id"
		}
	}

	if derived := deriveCodexCacheAffinity(request); derived != "" {
		return derived, "stable-prefix"
	}

	if derived := deriveCodexClientScopeAffinity(c, info, request); derived != "" {
		return derived, "client-scope"
	}

	return "", ""
}

func deriveCodexClientScopeAffinity(c *gin.Context, info *relaycommon.RelayInfo, request *dto.OpenAIResponsesRequest) string {
	if info == nil || request == nil {
		return ""
	}
	ua := ""
	if c != nil {
		ua = strings.TrimSpace(c.GetHeader("User-Agent"))
	}
	if info.UserId == 0 && info.TokenId == 0 && ua == "" {
		return ""
	}
	h := sha256.New()
	writeAffinityPart(h, []byte("new-api-codex-client-scope-v1"))
	writeAffinityPart(h, []byte(fmt.Sprintf("%d:%d", info.UserId, info.TokenId)))
	writeAffinityPart(h, []byte(request.Model))
	writeAffinityPart(h, []byte(ua))
	sum := h.Sum(nil)
	return "na-client-" + hex.EncodeToString(sum[:16])
}

func injectCodexPromptCacheKeyIfNeeded(c *gin.Context, request *dto.OpenAIResponsesRequest, body []byte) ([]byte, bool, error) {
	if request == nil || decodePromptCacheKey(request.PromptCacheKey) != "" || c == nil {
		return body, false, nil
	}
	key := strings.TrimSpace(c.GetString(codex.CacheAffinityContextKey))
	if key == "" {
		return body, false, nil
	}
	patched, err := sjson.SetBytes(body, "prompt_cache_key", key)
	if err != nil {
		return nil, false, err
	}
	logger.LogDebug(c, "[codex-cache] injected prompt_cache_key into passthrough body")
	return patched, true, nil
}

func addCodexStablePrefixBreakpoint(raw json.RawMessage) (json.RawMessage, bool, error) {
	if len(raw) == 0 {
		return raw, false, nil
	}
	items := gjson.ParseBytes(raw)
	if !items.IsArray() {
		return raw, false, nil
	}
	for i, item := range items.Array() {
		if !strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "user") {
			continue
		}
		content := item.Get("content")
		if !content.IsArray() {
			return raw, false, nil
		}
		blocks := content.Array()
		for j := len(blocks) - 1; j >= 0; j-- {
			if blocks[j].Get("type").String() != "input_text" {
				continue
			}
			if strings.TrimSpace(blocks[j].Get("text").String()) == "" {
				continue
			}
			path := fmt.Sprintf("%d.content.%d.prompt_cache_breakpoint.mode", i, j)
			patched, err := sjson.SetBytes(raw, path, "explicit")
			if err != nil {
				return nil, false, err
			}
			return json.RawMessage(patched), true, nil
		}
		return raw, false, nil
	}
	return raw, false, nil
}

func decodePromptCacheKey(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func deriveCodexCacheAffinity(request *dto.OpenAIResponsesRequest) string {
	if request == nil {
		return ""
	}

	seed := firstUserInputSeed(request.Input)
	if len(seed) == 0 {
		return ""
	}

	h := sha256.New()
	writeAffinityPart(h, []byte("new-api-codex-affinity-v1"))
	writeAffinityPart(h, []byte(request.Model))
	writeAffinityPart(h, request.Instructions)
	writeAffinityPart(h, request.Tools)
	if request.Reasoning != nil {
		if reasoning, err := json.Marshal(request.Reasoning); err == nil {
			writeAffinityPart(h, reasoning)
		}
	}
	writeAffinityPart(h, seed)

	sum := h.Sum(nil)
	return "na-" + hex.EncodeToString(sum[:16])
}

type affinityHashWriter interface {
	Write([]byte) (int, error)
}

func writeAffinityPart(w affinityHashWriter, part []byte) {
	_, _ = fmt.Fprintf(w, "%d:", len(part))
	_, _ = w.Write(part)
	_, _ = w.Write([]byte{0})
}

func firstUserInputSeed(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}

	var direct string
	if err := json.Unmarshal(raw, &direct); err == nil {
		direct = strings.TrimSpace(direct)
		if direct == "" {
			return nil
		}
		encoded, _ := json.Marshal(direct)
		return encoded
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}

	for _, item := range items {
		var envelope struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(item, &envelope); err != nil || !strings.EqualFold(strings.TrimSpace(envelope.Role), "user") {
			continue
		}

		var canonical any
		if err := json.Unmarshal(item, &canonical); err == nil {
			if encoded, err := json.Marshal(canonical); err == nil {
				return encoded
			}
		}
		return append([]byte(nil), item...)
	}

	return nil
}
