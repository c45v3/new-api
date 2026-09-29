package common

import (
	hostcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

// CaptureRequestBodyForLog stores the final outbound request body for the
// currently selected channel when per-channel request logging is enabled.
// The value stays request-scoped until usage/error logging persists it under
// root_info, so user/admin log projections never expose the captured prompt.
func CaptureRequestBodyForLog(c *gin.Context, info *RelayInfo, body []byte) {
	if c == nil || info == nil || !info.ChannelSetting.RequestBodyLoggingEnabled || len(body) == 0 {
		return
	}
	hostcommon.SetContextKey(c, constant.ContextKeyRequestBodyLog, string(body))
}
