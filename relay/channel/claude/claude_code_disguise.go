package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

// Fingerprint aligned with current Claude Code CLI (npm 2.1.267) and the
// checks used by Anthropic-compatible gateways that only accept that client:
// User-Agent claude-cli/x.y.z, X-App, anthropic-beta, anthropic-version,
// a Claude Code system identity block, and metadata.user_id.
const (
	claudeCodeCLIVersion           = "2.1.267"
	claudeCodeUserAgent            = "claude-cli/" + claudeCodeCLIVersion + " (external, cli)"
	claudeCodeXApp                 = "cli"
	claudeCodeAnthropicBeta        = "claude-code-20250219,interleaved-thinking-2025-05-14"
	claudeCodeSystemPrompt         = "You are Claude Code, Anthropic's official CLI for Claude."
	claudeCodeStainlessLang        = "js"
	claudeCodeStainlessRuntime     = "node"
	claudeCodeStainlessRuntimeVer  = "v24.20.0"
	claudeCodeStainlessPackageVer  = "0.75.0"
	claudeCodeStainlessRetryCount  = "0"
	claudeCodeStainlessTimeout     = "600"
	claudeCodeBillingHeaderPrefix  = "x-anthropic-billing-header"
	claudeCodeEntrypointMarker     = "cc_entrypoint="
	claudeCodeAgentSDKPromptPrefix = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
)

var (
	claudeCodeUAPattern = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+`)
	legacyUserIDPattern = regexp.MustCompile(`^user_[a-fA-F0-9]{64}_account_[a-fA-F0-9-]*_session_[a-fA-F0-9-]{36}$`)
)

type claudeCodeUserIDJSON struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
	SessionID   string `json:"session_id"`
}

func claudeCodeDisguiseEnabled(info *relaycommon.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil {
		return false
	}
	return info.ChannelOtherSettings.DisguiseAsClaudeCode
}

func applyClaudeCodeDisguiseConverted(info *relaycommon.RelayInfo, converted any) {
	req, ok := converted.(*dto.ClaudeRequest)
	if !ok {
		return
	}
	applyClaudeCodeDisguise(info, req)
}

func applyClaudeCodeDisguise(info *relaycommon.RelayInfo, request *dto.ClaudeRequest) {
	if !claudeCodeDisguiseEnabled(info) || request == nil {
		return
	}
	ensureClaudeCodeSystem(request)
	ensureClaudeCodeMetadata(info, request)
}

func applyClaudeCodeHeaders(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) {
	if !claudeCodeDisguiseEnabled(info) || req == nil {
		return
	}

	headers := resolveClaudeCodeHeaders(c, req)
	for name, value := range headers {
		current := req.Get(name)
		if current == "" || !keepExistingClaudeCodeHeader(name, current) {
			req.Set(name, value)
		}
	}
	mergeClaudeCodeHeaderOverrides(info, headers)
}

func resolveClaudeCodeHeaders(c *gin.Context, req *http.Header) map[string]string {
	headers := map[string]string{
		"User-Agent":                  pickClaudeCodeUserAgent(c, req),
		"X-App":                       firstNonEmpty(req.Get("X-App"), clientHeader(c, "X-App"), claudeCodeXApp),
		"anthropic-beta":              firstNonEmpty(req.Get("anthropic-beta"), clientHeader(c, "anthropic-beta"), claudeCodeAnthropicBeta),
		"anthropic-version":           firstNonEmpty(req.Get("anthropic-version"), clientHeader(c, "anthropic-version"), "2023-06-01"),
		"X-Stainless-Lang":            firstNonEmpty(req.Get("X-Stainless-Lang"), clientHeader(c, "X-Stainless-Lang"), claudeCodeStainlessLang),
		"X-Stainless-Runtime":         firstNonEmpty(req.Get("X-Stainless-Runtime"), clientHeader(c, "X-Stainless-Runtime"), claudeCodeStainlessRuntime),
		"X-Stainless-Runtime-Version": firstNonEmpty(req.Get("X-Stainless-Runtime-Version"), clientHeader(c, "X-Stainless-Runtime-Version"), claudeCodeStainlessRuntimeVer),
		"X-Stainless-Package-Version": firstNonEmpty(req.Get("X-Stainless-Package-Version"), clientHeader(c, "X-Stainless-Package-Version"), claudeCodeStainlessPackageVer),
		"X-Stainless-Retry-Count":     firstNonEmpty(req.Get("X-Stainless-Retry-Count"), clientHeader(c, "X-Stainless-Retry-Count"), claudeCodeStainlessRetryCount),
		"X-Stainless-Timeout":         firstNonEmpty(req.Get("X-Stainless-Timeout"), clientHeader(c, "X-Stainless-Timeout"), claudeCodeStainlessTimeout),
		"X-Stainless-Os":              firstNonEmpty(req.Get("X-Stainless-Os"), clientHeader(c, "X-Stainless-Os"), claudeCodeStainlessOS()),
		"X-Stainless-Arch":            firstNonEmpty(req.Get("X-Stainless-Arch"), clientHeader(c, "X-Stainless-Arch"), claudeCodeStainlessArch()),
	}
	return headers
}

func pickClaudeCodeUserAgent(c *gin.Context, req *http.Header) string {
	for _, candidate := range []string{req.Get("User-Agent"), clientHeader(c, "User-Agent")} {
		if claudeCodeUAPattern.MatchString(candidate) {
			return candidate
		}
	}
	return claudeCodeUserAgent
}

func keepExistingClaudeCodeHeader(name, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.EqualFold(name, "User-Agent") {
		return claudeCodeUAPattern.MatchString(value)
	}
	return true
}

func mergeClaudeCodeHeaderOverrides(info *relaycommon.RelayInfo, headers map[string]string) {
	if info == nil || info.ChannelMeta == nil {
		return
	}
	if info.UseRuntimeHeadersOverride {
		if info.RuntimeHeadersOverride == nil {
			info.RuntimeHeadersOverride = make(map[string]any, len(headers))
		}
		mergeClaudeCodeHeadersIntoMap(info.RuntimeHeadersOverride, headers, false)
		return
	}
	if info.HeadersOverride == nil {
		info.HeadersOverride = make(map[string]any, len(headers))
	}
	mergeClaudeCodeHeadersIntoMap(info.HeadersOverride, headers, true)
}

func mergeClaudeCodeHeadersIntoMap(dst map[string]any, headers map[string]string, adminWins bool) {
	for name, value := range headers {
		existing, ok := lookupHeaderMap(dst, name)
		if ok && adminWins {
			continue
		}
		if ok && keepExistingClaudeCodeHeader(name, existing) {
			continue
		}
		deleteHeaderMapKey(dst, name)
		dst[strings.ToLower(name)] = value
	}
}

func ensureClaudeCodeSystem(request *dto.ClaudeRequest) {
	identity := dto.ClaudeMediaMessage{Type: dto.ContentTypeText}
	identity.SetText(claudeCodeSystemPrompt)

	if request.System == nil {
		request.System = []dto.ClaudeMediaMessage{identity}
		return
	}

	if request.IsStringSystem() {
		existing := request.GetStringSystem()
		if strings.TrimSpace(existing) == "" {
			request.System = []dto.ClaudeMediaMessage{identity}
			return
		}
		existingBlock := dto.ClaudeMediaMessage{Type: dto.ContentTypeText}
		existingBlock.SetText(existing)
		if isClaudeCodeSystemText(existing) {
			request.System = []dto.ClaudeMediaMessage{existingBlock}
			return
		}
		request.System = []dto.ClaudeMediaMessage{identity, existingBlock}
		return
	}

	blocks := request.ParseSystem()
	for _, block := range blocks {
		if isClaudeCodeSystemText(block.GetText()) {
			return
		}
	}
	request.System = append([]dto.ClaudeMediaMessage{identity}, blocks...)
}

func isClaudeCodeSystemText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, claudeCodeSystemPrompt) ||
		strings.HasPrefix(trimmed, claudeCodeAgentSDKPromptPrefix) {
		return true
	}
	return strings.HasPrefix(trimmed, claudeCodeBillingHeaderPrefix) &&
		strings.Contains(trimmed, claudeCodeEntrypointMarker)
}

func ensureClaudeCodeMetadata(info *relaycommon.RelayInfo, request *dto.ClaudeRequest) {
	meta := map[string]any{}
	if len(request.Metadata) > 0 {
		if err := common.Unmarshal(request.Metadata, &meta); err != nil {
			meta = map[string]any{}
		}
	}
	if userID, ok := meta["user_id"].(string); ok && validClaudeCodeUserID(userID) {
		return
	}
	meta["user_id"] = generateClaudeCodeUserID(info)
	raw, err := common.Marshal(meta)
	if err != nil {
		return
	}
	request.Metadata = raw
}

func validClaudeCodeUserID(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	if userID[0] == '{' {
		var parsed claudeCodeUserIDJSON
		if err := common.Unmarshal([]byte(userID), &parsed); err != nil {
			return false
		}
		return parsed.DeviceID != "" && parsed.SessionID != ""
	}
	return legacyUserIDPattern.MatchString(userID)
}

func generateClaudeCodeUserID(info *relaycommon.RelayInfo) string {
	userID, tokenID, channelID := 0, 0, 0
	if info != nil {
		userID = info.UserId
		tokenID = info.TokenId
		if info.ChannelMeta != nil {
			channelID = info.ChannelId
		}
	}
	seed := fmt.Sprintf("new-api:claude-code:%d:%d:%d", userID, tokenID, channelID)
	sum := sha256.Sum256([]byte(seed))
	deviceID := hex.EncodeToString(sum[:])
	sessionSum := sha256.Sum256([]byte(seed + ":session"))
	payload := claudeCodeUserIDJSON{
		DeviceID:  deviceID,
		SessionID: uuidFromBytes(sessionSum[:16]),
	}
	raw, err := common.Marshal(payload)
	if err != nil {
		return `{"device_id":"` + payload.DeviceID + `","account_uuid":"","session_id":"` + payload.SessionID + `"}`
	}
	return string(raw)
}

func uuidFromBytes(b []byte) string {
	u := make([]byte, 16)
	copy(u, b)
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func claudeCodeStainlessOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	default:
		return "Linux"
	}
}

func claudeCodeStainlessArch() string {
	if runtime.GOARCH == "amd64" {
		return "x64"
	}
	return runtime.GOARCH
}

func clientHeader(c *gin.Context, name string) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.Request.Header.Get(name))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func lookupHeaderMap(m map[string]any, name string) (string, bool) {
	for k, v := range m {
		if !strings.EqualFold(k, name) {
			continue
		}
		str, ok := v.(string)
		if !ok {
			return fmt.Sprintf("%v", v), true
		}
		return str, true
	}
	return "", false
}

func deleteHeaderMapKey(m map[string]any, name string) {
	for k := range m {
		if strings.EqualFold(k, name) {
			delete(m, k)
		}
	}
}
