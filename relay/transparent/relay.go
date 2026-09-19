// Package transparent forwards HTTP representations without protocol adaptation.
// Authentication and channel selection must have completed before Relay is called.
package transparent

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// Relay makes one application-level attempt. Billing belongs to the deployment's
// external accounting system, never to an inferred or fabricated usage record.
func Relay(c *gin.Context) {
	fail := func(status int, message string) {
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "transparent_relay_error", "message": message}})
	}
	settings, ok := common.GetContextKeyType[dto.ChannelSettings](c, constant.ContextKeyChannelSetting)
	if !ok || settings.TransportMode != dto.TransportModeTransparent {
		fail(http.StatusInternalServerError, "transparent relay requires selected channel settings")
		return
	}
	if err := settings.ValidateTransportMode(); err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	other, _ := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting)
	mapping := common.GetContextKeyString(c, constant.ContextKeyChannelModelMapping)
	var modelMapping map[string]string
	if mapping != "" {
		if err := common.UnmarshalJsonStr(mapping, &modelMapping); err != nil {
			fail(http.StatusBadRequest, "invalid channel model mapping")
			return
		}
	}
	if err := settings.ValidateTransparentMutations(other, len(common.GetContextKeyStringMap(c, constant.ContextKeyChannelParamOverride)) > 0, len(modelMapping) > 0); err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/pg/") || c.Request.Method == http.MethodConnect || c.GetHeader("Upgrade") != "" || c.GetHeader("Sec-WebSocket-Key") != "" || c.Request.URL.Path == "/v1/realtime" {
		fail(http.StatusBadRequest, "transparent relay supports HTTP API requests, not playground or protocol upgrades")
		return
	}
	credentialHeader := "Authorization"
	credentialPrefix := "Bearer "
	switch common.GetContextKeyInt(c, constant.ContextKeyChannelType) {
	case constant.ChannelTypeAnthropic:
		credentialHeader, credentialPrefix = "X-Api-Key", ""
	case constant.ChannelTypeGemini:
		credentialHeader, credentialPrefix = "X-Goog-Api-Key", ""
	case constant.ChannelTypeAzure:
		credentialHeader, credentialPrefix = "Api-Key", ""
	case constant.ChannelTypeOpenAI, constant.ChannelTypeOpenRouter, constant.ChannelTypeCustom,
		constant.ChannelTypeNewAPI, constant.ChannelTypeDeepSeek, constant.ChannelTypeMistral,
		constant.ChannelTypeXai, constant.ChannelTypeMoonshot, constant.ChannelTypeSiliconFlow,
		constant.ChannelTypeVolcEngine, constant.ChannelTypeJina, constant.ChannelTypeCohere:
	default:
		fail(http.StatusBadRequest, "transparent relay does not support this channel credential scheme")
		return
	}
	target, err := url.Parse(common.GetContextKeyString(c, constant.ContextKeyChannelBaseUrl))
	if err != nil || target.Host == "" || (target.Scheme != "https" && target.Scheme != "http") || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.ForceQuery || target.Opaque != "" {
		fail(http.StatusBadRequest, "transparent relay requires an HTTP(S) base URL without credentials, query or fragment")
		return
	}
	// Do not path.Clean, ResolveReference, or re-encode the incoming query. The
	// operator-owned base path is a mount prefix, not an endpoint template.
	escapedPath := strings.TrimSuffix(target.EscapedPath(), "/") + c.Request.URL.EscapedPath()
	target.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		fail(http.StatusBadRequest, "invalid request path")
		return
	}
	target.RawPath = escapedPath
	target.RawQuery, err = safeQuery(c.Request.URL.RawQuery)
	if err != nil {
		fail(http.StatusBadRequest, "invalid query encoding")
		return
	}
	target.ForceQuery = c.Request.URL.ForceQuery

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		status := http.StatusBadRequest
		if common.IsRequestBodyTooLargeError(err) {
			status = http.StatusRequestEntityTooLarge
		}
		fail(status, "cannot read request body")
		return
	}
	if raw, exists := c.Get(common.KeyOriginalBodyStorage); exists {
		storage, ok = raw.(common.BodyStorage)
		if !ok {
			fail(http.StatusInternalServerError, "invalid original body storage")
			return
		}
	}
	body, err := storage.NewReader()
	if err != nil {
		fail(http.StatusBadRequest, "cannot open request body")
		return
	}
	defer body.Close()
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target.String(), body)
	if err != nil {
		fail(http.StatusBadRequest, "cannot construct upstream request")
		return
	}
	req.ContentLength = storage.Size()
	// No GetBody: do not authorize transparent replay of a possibly executed body.
	// The shared transport may still retry a connection known not to have sent it.
	if storage.Size() == 0 {
		req.Body = http.NoBody
	}
	req.Header = c.Request.Header.Clone()
	stripHopHeaders(req.Header)
	req.Header.Del("Host")
	req.Header.Del("Content-Length")
	for name := range req.Header {
		if clientCredentialHeader(name) {
			req.Header.Del(name)
		}
	}
	if encoding := c.GetString(common.KeyOriginalContentEncoding); encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	// Resolve placeholders against the sanitized client view, never its token or
	// session. Wildcard rules are redundant here and would collapse multi-values.
	overrideContext := c.Copy()
	overrideContext.Request = c.Request.Clone(c.Request.Context())
	overrideContext.Request.Header = req.Header.Clone()
	explicit := make(map[string]any)
	for name, value := range common.GetContextKeyStringMap(c, constant.ContextKeyChannelHeaderOverride) {
		if !channel.IsHeaderPassthroughRuleKey(name) {
			explicit[name] = value
		}
	}
	apiKey := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: apiKey, HeadersOverride: explicit}}
	overrides, err := channel.ResolveHeaderOverride(info, overrideContext)
	if err != nil {
		fail(http.StatusBadRequest, "invalid transparent header override")
		return
	}
	req.Header.Set(credentialHeader, credentialPrefix+apiKey)
	if organization := common.GetContextKeyString(c, constant.ContextKeyChannelOrganization); organization != "" {
		req.Header.Set("OpenAI-Organization", organization)
	}
	for name, value := range overrides {
		req.Header.Set(name, value)
	}
	// Overrides can replace upstream authentication, but cannot reintroduce
	// cookies, transport framing, handshake fields, or hop-by-hop headers.
	stripHopHeaders(req.Header)
	req.Header.Del("Host")
	req.Header.Del("Content-Length")
	for name := range req.Header {
		if strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Mj-Api-Secret") || strings.HasPrefix(strings.ToLower(name), "sec-websocket-") {
			req.Header.Del(name)
		}
	}
	// Explicit Accept-Encoding disables Go's automatic response decompression.
	// With no client preference, identity prevents hidden gzip negotiation.
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "identity")
	}
	if _, exists := req.Header["User-Agent"]; !exists {
		req.Header["User-Agent"] = []string{""}
	}
	client, err := service.GetHttpClientWithProxySettings(settings.Proxy, settings)
	if err != nil {
		fail(http.StatusBadRequest, "invalid channel HTTP transport configuration")
		return
	}
	forwardingClient := *client
	forwardingClient.Jar = nil
	forwardingClient.CheckRedirect = keepRedirect
	response, err := forwardingClient.Do(req)
	if err != nil {
		// Do not log URL/query, credentials or provider-controlled error strings.
		fail(http.StatusBadGateway, "upstream transport failed")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusSwitchingProtocols {
		fail(http.StatusBadGateway, "upstream protocol upgrade is not supported")
		return
	}
	stripHopHeaders(response.Header)
	response.Header.Del("Set-Cookie")
	for name, values := range response.Header {
		c.Writer.Header()[name] = values
	}
	// Suppress net/http content sniffing when upstream supplied no Content-Type.
	if _, exists := response.Header["Content-Type"]; !exists {
		c.Writer.Header()["Content-Type"] = nil
	}
	c.Status(response.StatusCode)
	c.Writer.WriteHeaderNow()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			helper.ExtendWriteDeadline(c)
			if _, writeErr := c.Writer.Write(buffer[:n]); writeErr != nil {
				return
			}
			c.Writer.Flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				logger.LogWarn(c, "transparent relay upstream body interrupted; response not retried")
				// net/http aborts the HTTP/1 connection or HTTP/2 stream; do not append an
				// error document to a partially forwarded representation.
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}

func keepRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func clientCredentialHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "x-api-key", "api-key", "x-goog-api-key", "mj-api-secret", "cookie":
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), "sec-websocket-")
}

func stripHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for name := range strings.SplitSeq(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

// safeQuery removes authentication fields without normalizing unrelated bytes.
// Malformed escapes and semicolons are rejected rather than risking a different
// credential interpretation by the upstream query parser.
func safeQuery(raw string) (string, error) {
	if strings.Contains(raw, ";") {
		return "", errors.New("ambiguous query separator")
	}
	parts := strings.Split(raw, "&")
	kept := parts[:0]
	changed := false
	for _, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			return "", err
		}
		if _, err := url.QueryUnescape(part); err != nil {
			return "", err
		}
		switch strings.ToLower(name) {
		case "key", "api_key", "api-key", "access_token", "authorization", "x-api-key", "x-goog-api-key":
			changed = true
		default:
			kept = append(kept, part)
		}
	}
	if !changed {
		return raw, nil
	}
	return strings.Join(kept, "&"), nil
}
