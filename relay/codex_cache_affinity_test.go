package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

	key, source := resolveCodexCacheAffinity(c, request)
	if key != "session-key" || source != "session-id" {
		t.Fatalf("got key=%q source=%q", key, source)
	}

	c = newCodexAffinityTestContext(map[string]string{"thread-id": "thread-key"})
	key, source = resolveCodexCacheAffinity(c, request)
	if key != "prompt-key" || source != "prompt_cache_key" {
		t.Fatalf("got key=%q source=%q", key, source)
	}

	request.PromptCacheKey = nil
	key, source = resolveCodexCacheAffinity(c, request)
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
