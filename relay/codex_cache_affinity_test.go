package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relay/channel/codex"
	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

func newCodexAffinityTestContext(headers map[string]string) *gin.Context {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

func TestResolveCodexCacheAffinityPriority(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{PromptCacheKey: json.RawMessage(`"prompt-key"`)}
	c := newCodexAffinityTestContext(map[string]string{
		"session-id": "session-key",
		"thread-id":  "thread-key",
	})

	key, source := resolveCodexCacheAffinity(c, &common.RelayInfo{}, request)
	if key != "session-key" || source != "session-id" {
		t.Fatalf("got key=%q source=%q", key, source)
	}

	c = newCodexAffinityTestContext(map[string]string{"thread-id": "thread-key"})
	key, source = resolveCodexCacheAffinity(c, &common.RelayInfo{}, request)
	if key != "prompt-key" || source != "prompt_cache_key" {
		t.Fatalf("got key=%q source=%q", key, source)
	}

	request.PromptCacheKey = nil
	key, source = resolveCodexCacheAffinity(c, &common.RelayInfo{}, request)
	if key != "thread-key" || source != "thread-id" {
		t.Fatalf("got key=%q source=%q", key, source)
	}
}

func TestDeriveCodexCacheAffinityStableAcrossAppendedHistory(t *testing.T) {
	first := json.RawMessage(`[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
	]`)
	later := json.RawMessage(`[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
	]`)

	base := dto.OpenAIResponsesRequest{
		Model:        "gpt-test",
		Instructions: json.RawMessage(`"system"`),
		Tools:        json.RawMessage(`[{"type":"function","name":"tool"}]`),
		Input:        first,
	}
	next := base
	next.Input = later

	firstKey := deriveCodexCacheAffinity(&base)
	laterKey := deriveCodexCacheAffinity(&next)
	if firstKey == "" || firstKey != laterKey {
		t.Fatalf("expected stable affinity, first=%q later=%q", firstKey, laterKey)
	}
}

func TestDeriveCodexCacheAffinitySeparatesFirstUserInput(t *testing.T) {
	left := &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[{"type":"message","role":"user","content":"alpha"}]`),
	}
	right := &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[{"type":"message","role":"user","content":"beta"}]`),
	}

	leftKey := deriveCodexCacheAffinity(left)
	rightKey := deriveCodexCacheAffinity(right)
	if leftKey == "" || rightKey == "" || leftKey == rightKey {
		t.Fatalf("expected distinct affinity keys, left=%q right=%q", leftKey, rightKey)
	}
}

func TestDeriveCodexCacheAffinityRequiresStableInput(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{
		Model:        "gpt-test",
		Instructions: json.RawMessage(`"system"`),
		Tools:        json.RawMessage(`[{"type":"function","name":"tool"}]`),
		Input:        json.RawMessage(`[{"type":"function_call_output","call_id":"1","output":"ok"}]`),
	}

	if key := deriveCodexCacheAffinity(request); key != "" {
		t.Fatalf("expected no affinity key, got %q", key)
	}
}

func TestResolveCodexCacheAffinityFallsBackToClientScope(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[{"type":"function_call_output","call_id":"1","output":"ok"}]`),
	}
	info := &common.RelayInfo{UserId: 7, TokenId: 11}
	c := newCodexAffinityTestContext(map[string]string{"User-Agent": "bare-client/1.0"})

	key, source := resolveCodexCacheAffinity(c, info, request)
	if key == "" || source != "client-scope" {
		t.Fatalf("got key=%q source=%q", key, source)
	}
	key2, source2 := resolveCodexCacheAffinity(c, info, request)
	if key2 != key || source2 != source {
		t.Fatalf("expected stable client-scope affinity, first=%q/%q second=%q/%q", key, source, key2, source2)
	}
}

func TestInjectCodexPromptCacheKeyIfNeeded(t *testing.T) {
	c := newCodexAffinityTestContext(nil)
	c.Set(codex.CacheAffinityContextKey, "cache-key")
	request := &dto.OpenAIResponsesRequest{}
	body := []byte(`{"model":"gpt-test","input":"hello"}`)

	patched, changed, err := injectCodexPromptCacheKeyIfNeeded(c, request, body)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected passthrough body to be patched")
	}
	var decoded map[string]any
	if err := json.Unmarshal(patched, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["prompt_cache_key"] != "cache-key" {
		t.Fatalf("prompt_cache_key=%v", decoded["prompt_cache_key"])
	}
}

func TestInjectCodexPromptCacheKeyPreservesExplicitValue(t *testing.T) {
	c := newCodexAffinityTestContext(nil)
	c.Set(codex.CacheAffinityContextKey, "derived-key")
	request := &dto.OpenAIResponsesRequest{PromptCacheKey: json.RawMessage(`"explicit-key"`)}
	body := []byte(`{"model":"gpt-test","prompt_cache_key":"explicit-key"}`)

	patched, changed, err := injectCodexPromptCacheKeyIfNeeded(c, request, body)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("explicit prompt_cache_key must not be overwritten")
	}
	if string(patched) != string(body) {
		t.Fatalf("body changed unexpectedly: %s", patched)
	}
}

func TestApplyCodexFallbackMetadata(t *testing.T) {
	c := newCodexAffinityTestContext(map[string]string{"User-Agent": "bare-client/1.0"})
	info := &common.RelayInfo{UserId: 7, TokenId: 11}
	request := &dto.OpenAIResponsesRequest{
		Model:     "gpt-6-luna",
		Reasoning: &dto.Reasoning{Effort: "low"},
	}

	if err := applyCodexFallbackMetadata(c, info, request, "affinity-key"); err != nil {
		t.Fatal(err)
	}

	var metadata map[string]string
	if err := json.Unmarshal(request.ClientMetadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["session_id"] != "affinity-key" || metadata["thread_id"] != "affinity-key" {
		t.Fatalf("unexpected session/thread metadata: %v", metadata)
	}
	if metadata["x-codex-window-id"] == "" || metadata["turn_id"] == "" {
		t.Fatalf("missing window/turn metadata: %v", metadata)
	}
	if c.GetString(codex.ResponsesWindowIDContextKey) != metadata["x-codex-window-id"] {
		t.Fatal("window compatibility header context does not match body metadata")
	}

	var turn map[string]any
	if err := json.Unmarshal([]byte(metadata["x-codex-turn-metadata"]), &turn); err != nil {
		t.Fatal(err)
	}
	if turn["request_kind"] != "turn" || turn["model"] != "gpt-6-luna" {
		t.Fatalf("unexpected turn metadata: %v", turn)
	}
	if turn["turn_id"] != metadata["turn_id"] {
		t.Fatalf("turn id mismatch: body=%v top=%v", turn["turn_id"], metadata["turn_id"])
	}
}

func TestApplyCodexFallbackMetadataKeepsWindowStableAndTurnFresh(t *testing.T) {
	info := &common.RelayInfo{UserId: 7, TokenId: 11}
	makeRequest := func() (map[string]string, error) {
		c := newCodexAffinityTestContext(map[string]string{"User-Agent": "bare-client/1.0"})
		request := &dto.OpenAIResponsesRequest{Model: "gpt-6-luna"}
		if err := applyCodexFallbackMetadata(c, info, request, "same-affinity"); err != nil {
			return nil, err
		}
		var metadata map[string]string
		if err := json.Unmarshal(request.ClientMetadata, &metadata); err != nil {
			return nil, err
		}
		return metadata, nil
	}

	first, err := makeRequest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := makeRequest()
	if err != nil {
		t.Fatal(err)
	}
	if first["x-codex-installation-id"] != second["x-codex-installation-id"] {
		t.Fatal("installation id must stay stable")
	}
	if first["x-codex-window-id"] != second["x-codex-window-id"] {
		t.Fatal("window id must stay stable for one affinity")
	}
	if first["turn_id"] == second["turn_id"] {
		t.Fatal("turn id must change per request")
	}
}
