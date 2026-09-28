package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/types"
)

func MidjourneyErrorWrapper(code int, desc string) *dto.MidjourneyResponse {
	return &dto.MidjourneyResponse{
		Code:        code,
		Description: desc,
	}
}

func MidjourneyErrorWithStatusCodeWrapper(code int, desc string, statusCode int) *dto.MidjourneyResponseWithStatusCode {
	return &dto.MidjourneyResponseWithStatusCode{
		StatusCode: statusCode,
		Response:   *MidjourneyErrorWrapper(code, desc),
	}
}

//// OpenAIErrorWrapper wraps an error into an OpenAIErrorWithStatusCode
//func OpenAIErrorWrapper(err error, code string, statusCode int) *dto.OpenAIErrorWithStatusCode {
//	text := err.Error()
//	lowerText := strings.ToLower(text)
//	if !strings.HasPrefix(lowerText, "get file base64 from url") && !strings.HasPrefix(lowerText, "mime type is not supported") {
//		if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
//			common.SysLog(fmt.Sprintf("error: %s", text))
//			text = "请求上游地址失败"
//		}
//	}
//	openAIError := dto.OpenAIError{
//		Message: text,
//		Type:    "new_api_error",
//		Code:    code,
//	}
//	return &dto.OpenAIErrorWithStatusCode{
//		Error:      openAIError,
//		StatusCode: statusCode,
//	}
//}
//
//func OpenAIErrorWrapperLocal(err error, code string, statusCode int) *dto.OpenAIErrorWithStatusCode {
//	openaiErr := OpenAIErrorWrapper(err, code, statusCode)
//	openaiErr.LocalError = true
//	return openaiErr
//}

func ClaudeErrorWrapper(err error, code string, statusCode int) *dto.ClaudeErrorWithStatusCode {
	text := err.Error()
	lowerText := strings.ToLower(text)
	if !strings.HasPrefix(lowerText, "get file base64 from url") {
		if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
			common.SysLog(fmt.Sprintf("error: %s", text))
			text = "请求上游地址失败"
		}
	}
	claudeError := types.ClaudeError{
		Message: text,
		Type:    "new_api_error",
	}
	return &dto.ClaudeErrorWithStatusCode{
		Error:      claudeError,
		StatusCode: statusCode,
	}
}

func ClaudeErrorWrapperLocal(err error, code string, statusCode int) *dto.ClaudeErrorWithStatusCode {
	claudeErr := ClaudeErrorWrapper(err, code, statusCode)
	claudeErr.LocalError = true
	return claudeErr
}

// NoAvailableAccountsMessage 上游额度耗尽 / 限流类错误对外统一返回的固定文案。
const NoAvailableAccountsMessage = "bad response status code 503, message: No available accounts: no available accounts"

// isUpstreamQuotaOrRateLimitStatus 判断状态码是否属于上游额度耗尽 / 限流类错误。
func isUpstreamQuotaOrRateLimitStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusPaymentRequired,
		http.StatusTooManyRequests,
		http.StatusForbidden,
		http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

// NoAvailableAccountsError 构造「上游账号池不可用」统一错误：HTTP 503 + 固定文案。
func NoAvailableAccountsError() *types.NewAPIError {
	return types.NewOpenAIError(
		errors.New(NoAvailableAccountsMessage),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusServiceUnavailable,
	)
}

// IsNoAvailableAccountsError 判断错误是否已经是统一改写后的「No available accounts」错误。
func IsNoAvailableAccountsError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "No available accounts")
}

// IsLocalQuotaInsufficientError 判断错误是否为本站（而非上游）产生的额度不足错误。
//
// 只有本站自己生成的错误才算本站额度不足：
// 上游站点返回的 429 / 402 即使 code 也写作 insufficient_user_quota，也属于「上游额度不足」，
// 必须继续按上游账号池不可用处理，不能暴露给本站用户。
//
// 覆盖场景：本站用户钱包余额不足、订阅额度不足、令牌额度不足。
func IsLocalQuotaInsufficientError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.GetErrorType() != types.ErrorTypeNewAPIError {
		return false
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeInsufficientUserQuota, types.ErrorCodePreConsumeTokenQuotaFailed:
		return true
	default:
		return false
	}
}

// remapPaymentRequiredError 将上游返回的额度耗尽类报错（402 / 429 / 403 / 503）统一改写为
// 503，并替换为固定提示。返回新的错误（命中时）或原错误（未命中时）。
func remapPaymentRequiredError(newApiErr *types.NewAPIError) *types.NewAPIError {
	if newApiErr == nil {
		return newApiErr
	}
	if !isUpstreamQuotaOrRateLimitStatus(newApiErr.StatusCode) {
		return newApiErr
	}
	return NoAvailableAccountsError()
}

// FinalizeRelayError 决定最终返回给客户端的错误，是 relay 出口唯一的错误改写入口：
//
//  1. 本站用户额度不足（钱包 / 订阅 / 令牌）-> 保留原始提示，状态码 429；
//  2. 其余上游额度耗尽 / 限流类错误（402 / 429 / 403 / 503，或已是固定文案）-> 503 + 固定文案；
//  3. 其他错误 -> 原样返回。
//
// 这样调用方可以区分「自己的额度用完，需要充值」（429）与「本站上游账号池不可用」（503）。
func FinalizeRelayError(err *types.NewAPIError) *types.NewAPIError {
	if err == nil {
		return nil
	}
	if IsLocalQuotaInsufficientError(err) {
		err.StatusCode = http.StatusTooManyRequests
		return err
	}
	if IsNoAvailableAccountsError(err) || isUpstreamQuotaOrRateLimitStatus(err.StatusCode) {
		return NoAvailableAccountsError()
	}
	return err
}

func RelayErrorHandler(ctx context.Context, resp *http.Response, showBodyWhenFail bool) (newApiErr *types.NewAPIError) {
	newApiErr = types.InitOpenAIError(types.ErrorCodeBadResponseStatusCode, resp.StatusCode)

	// 覆盖所有返回路径：上游额度耗尽类 429 统一改写为 503 账号封禁
	defer func() {
		newApiErr = remapPaymentRequiredError(newApiErr)
	}()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	CloseResponseBodyGracefully(resp)
	var errResponse dto.GeneralErrorResponse
	responseBodyText := string(responseBody)
	responseBodyPreview := common.LocalLogPreview(responseBodyText)
	buildErrWithBody := func(message string) error {
		if message == "" {
			return fmt.Errorf("bad response status code %d, body: %s", resp.StatusCode, responseBodyText)
		}
		return fmt.Errorf("bad response status code %d, message: %s, body: %s", resp.StatusCode, message, responseBodyText)
	}

	err = common.Unmarshal(responseBody, &errResponse)
	if err != nil {
		if showBodyWhenFail {
			newApiErr.Err = buildErrWithBody("")
		} else {
			logger.LogError(ctx, fmt.Sprintf("bad response status code %d, body: %s", resp.StatusCode, responseBodyPreview))
			newApiErr.Err = fmt.Errorf("bad response status code %d", resp.StatusCode)
		}
		return
	}

	if common.GetJsonType(errResponse.Error) == "object" {
		// General format error (OpenAI, Anthropic, Gemini, etc.)
		oaiError := errResponse.TryToOpenAIError()
		if oaiError != nil {
			newApiErr = types.WithOpenAIError(*oaiError, resp.StatusCode)
			if showBodyWhenFail {
				newApiErr.Err = buildErrWithBody(newApiErr.Error())
			}
			return
		}
	}
	newApiErr = types.NewOpenAIError(errors.New(errResponse.ToMessage()), types.ErrorCodeBadResponseStatusCode, resp.StatusCode)
	if showBodyWhenFail {
		newApiErr.Err = buildErrWithBody(newApiErr.Error())
	}
	return
}

func ResetStatusCode(newApiErr *types.NewAPIError, statusCodeMappingStr string) {
	if newApiErr == nil {
		return
	}
	if statusCodeMappingStr == "" || statusCodeMappingStr == "{}" {
		return
	}
	statusCodeMapping := make(map[string]any)
	err := common.Unmarshal([]byte(statusCodeMappingStr), &statusCodeMapping)
	if err != nil {
		return
	}
	if newApiErr.StatusCode == http.StatusOK {
		return
	}
	codeStr := strconv.Itoa(newApiErr.StatusCode)
	if value, ok := statusCodeMapping[codeStr]; ok {
		intCode, ok := parseStatusCodeMappingValue(value)
		if !ok {
			return
		}
		newApiErr.StatusCode = intCode
	}
}

func parseStatusCodeMappingValue(value any) (int, bool) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return 0, false
		}
		statusCode, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		return statusCode, true
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case json.Number:
		statusCode, err := strconv.Atoi(v.String())
		if err != nil {
			return 0, false
		}
		return statusCode, true
	default:
		return 0, false
	}
}

func TaskErrorWrapperLocal(err error, code string, statusCode int) *dto.TaskError {
	openaiErr := TaskErrorWrapper(err, code, statusCode)
	openaiErr.LocalError = true
	return openaiErr
}

func TaskErrorWrapper(err error, code string, statusCode int) *dto.TaskError {
	text := err.Error()
	lowerText := strings.ToLower(text)
	if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
		common.SysLog(fmt.Sprintf("error: %s", text))
		//text = "请求上游地址失败"
		text = common.MaskSensitiveInfo(text)
	}
	//避免暴露内部错误
	taskError := &dto.TaskError{
		Code:       code,
		Message:    text,
		StatusCode: statusCode,
		Error:      err,
	}

	return taskError
}

// TaskErrorFromAPIError 将 PreConsumeBilling 返回的 NewAPIError 转换为 TaskError。
func TaskErrorFromAPIError(apiErr *types.NewAPIError) *dto.TaskError {
	if apiErr == nil {
		return nil
	}
	return &dto.TaskError{
		Code:       string(apiErr.GetErrorCode()),
		Message:    apiErr.Err.Error(),
		StatusCode: apiErr.StatusCode,
		Error:      apiErr.Err,
	}
}
