package relay

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// PrepareResponsesRequest applies the same model, conversion and channel rules
// for HTTP and WebSocket requests. The caller closes closer after the attempt;
// passthrough bodies remain owned by the incoming request's BodyStorage.
// The returned adaptor retains route/conversion state for DoRequest/DoResponse.
func PrepareResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, req *dto.OpenAIResponsesRequest) (relaychannel.Adaptor, common.ReplayableBody, io.Closer, *types.NewAPIError) {
	info.InitChannelMeta(c)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact &&
		!common.SupportsResponsesCompact(info.ChannelType, info.ApiType) {
		return nil, nil, nil, types.NewErrorWithStatusCode(
			fmt.Errorf("unsupported endpoint %q for api type %d", "/v1/responses/compact", info.ApiType),
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	request, err := common.DeepCopy(req)
	if err != nil {
		return nil, nil, nil, types.NewError(fmt.Errorf("failed to copy responses request: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ModelMappedHelper(c, info, request); err != nil {
		return nil, nil, nil, types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ApplyReasoningModelSuffix(c, info, request); err != nil {
		return nil, nil, nil, newConvertRequestFailedError(c, info, err)
	}
	if !info.IsPassThroughEnabled() && !info.ChannelSetting.PassThroughBodyEnabled {
		if err := applyResponsesSystemPromptIfNeeded(c, info, request); err != nil {
			return nil, nil, nil, types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	prepareCodexCacheAffinity(c, info, request)

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return nil, nil, nil, types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	if info.IsPassThroughEnabled() || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return nil, nil, nil, types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
		}
		if info.ApiType == constant.APITypeCodex {
			bodyBytes, bodyErr := storage.Bytes()
			if bodyErr != nil {
				return nil, nil, nil, types.NewError(bodyErr, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
			}
			patchedBody, changed, patchErr := injectCodexPromptCacheKeyIfNeeded(c, request, bodyBytes)
			if patchErr != nil {
				return nil, nil, nil, types.NewError(fmt.Errorf("failed to inject codex prompt cache key: %w", patchErr), types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}
			if changed {
				relaycommon.CaptureRequestBodyForLog(c, info, patchedBody)
				body, closer, bodyErr := relaycommon.NewOutboundJSONBody(patchedBody)
				if bodyErr != nil {
					return nil, nil, nil, types.NewError(bodyErr, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
				}
				return adaptor, body, closer, nil
			}
			if info.ChannelSetting.RequestBodyLoggingEnabled {
				relaycommon.CaptureRequestBodyForLog(c, info, bodyBytes)
			}
			body := common.NewReplayableBodyReader(storage)
			return adaptor, body, io.NopCloser(body), nil
		}
		if info.ChannelSetting.RequestBodyLoggingEnabled {
			if bodyBytes, bodyErr := storage.Bytes(); bodyErr == nil {
				relaycommon.CaptureRequestBodyForLog(c, info, bodyBytes)
			}
		}
		body := common.NewReplayableBodyReader(storage)
		return adaptor, body, io.NopCloser(body), nil
	}

	convertedRequest, err := adaptor.ConvertOpenAIResponsesRequest(c, info, *request)
	if err != nil {
		return nil, nil, nil, newConvertRequestFailedError(c, info, err)
	}
	relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)
	jsonData, err := common.Marshal(convertedRequest)
	if err != nil {
		return nil, nil, nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting, info.ChannelId)
	if err != nil {
		return nil, nil, nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
		if err != nil {
			return nil, nil, nil, newAPIErrorFromParamOverride(err)
		}
	}

	relaycommon.CaptureRequestBodyForLog(c, info, jsonData)
	logger.LogDebug(c, "requestBody: %s", jsonData)
	body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	if err != nil {
		return nil, nil, nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	return adaptor, body, closer, nil
}

// applyResponsesSystemPromptIfNeeded maps channel SystemPrompt onto Responses
// `instructions`. It does not write `input`. Callers must skip this when
// Request Body Passthrough is active so the original body stays byte-identical.
func applyResponsesSystemPromptIfNeeded(c *gin.Context, info *relaycommon.RelayInfo, request *dto.OpenAIResponsesRequest) error {
	if info == nil || info.ChannelMeta == nil || request == nil {
		return nil
	}
	systemPrompt := info.ChannelSetting.SystemPrompt
	if systemPrompt == "" {
		return nil
	}

	merged := systemPrompt
	if len(request.Instructions) > 0 {
		var probe any
		if err := common.Unmarshal(request.Instructions, &probe); err != nil {
			return fmt.Errorf("invalid responses instructions: %w", err)
		}
		switch value := probe.(type) {
		case nil:
		case string:
			if value != "" {
				if !info.ChannelSetting.SystemPromptOverride {
					return nil
				}
				if c != nil {
					common.SetContextKey(c, constant.ContextKeySystemPromptOverride, true)
				}
				merged = systemPrompt + "\n" + value
			}
		default:
			if !info.ChannelSetting.SystemPromptOverride {
				return nil
			}
			return fmt.Errorf("invalid responses instructions: expected JSON string, got %s", common.GetJsonType(request.Instructions))
		}
	}

	encoded, err := common.Marshal(merged)
	if err != nil {
		return err
	}
	request.Instructions = encoded
	return nil
}
