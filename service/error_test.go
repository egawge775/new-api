package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResetStatusCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		statusCode       int
		statusCodeConfig string
		expectedCode     int
	}{
		{
			name:             "map string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"503"}`,
			expectedCode:     503,
		},
		{
			name:             "map int value",
			statusCode:       429,
			statusCodeConfig: `{"429":503}`,
			expectedCode:     503,
		},
		{
			name:             "skip invalid string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"bad-code"}`,
			expectedCode:     429,
		},
		{
			name:             "skip status code 200",
			statusCode:       200,
			statusCodeConfig: `{"200":503}`,
			expectedCode:     200,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			newAPIError := &types.NewAPIError{
				StatusCode: tc.statusCode,
			}
			ResetStatusCode(newAPIError, tc.statusCodeConfig)
			require.Equal(t, tc.expectedCode, newAPIError.StatusCode)
		})
	}
}

func TestRelayErrorHandlerTruncatesInvalidJSONBodyInLog(t *testing.T) {
	withDebugEnabled(t, false)

	body := strings.Repeat("b", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, "bad response status code 500", newAPIError.Error())
	require.Contains(t, logBuffer.String(), "[truncated")
	require.Contains(t, logBuffer.String(), fmt.Sprintf("original_length=%d", len(body)))
	require.NotContains(t, logBuffer.String(), strings.Repeat("b", common.LocalLogContentLimit+1))
}

func TestRelayErrorHandlerKeepsStructuredErrorMessage(t *testing.T) {
	message := strings.Repeat("c", common.LocalLogContentLimit+256)
	body := `{"message":"` + message + `"}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerKeepsOpenAIErrorMessage(t *testing.T) {
	message := strings.Repeat("d", common.LocalLogContentLimit+256)
	body := `{"error":{"message":"` + message + `","type":"server_error","code":"server_error"}}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerKeepsInvalidJSONBodyInDebugLog(t *testing.T) {
	withDebugEnabled(t, true)

	body := strings.Repeat("e", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.NotContains(t, logBuffer.String(), "[truncated")
	require.Contains(t, logBuffer.String(), body)
}

func withDebugEnabled(t *testing.T, enabled bool) {
	t.Helper()

	oldDebug := common.DebugEnabled
	common.DebugEnabled = enabled
	t.Cleanup(func() {
		common.DebugEnabled = oldDebug
	})
}

// 本站用户额度不足：必须保留原始提示并返回 429，不能被改写成「上游账号池不可用」。
func TestFinalizeRelayErrorKeepsLocalQuotaInsufficientAs429(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		errCode  types.ErrorCode
		message  string
		statusCd int
	}{
		{
			name:     "wallet quota insufficient",
			errCode:  types.ErrorCodeInsufficientUserQuota,
			message:  "用户额度不足, 剩余额度: ＄0.000000",
			statusCd: http.StatusForbidden,
		},
		{
			name:     "pre consume token quota insufficient",
			errCode:  types.ErrorCodePreConsumeTokenQuotaFailed,
			message:  "token quota is not enough, token remain quota: ＄0.000000, need quota: ＄0.010000",
			statusCd: http.StatusForbidden,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			localErr := types.NewErrorWithStatusCode(
				errors.New(tc.message),
				tc.errCode,
				tc.statusCd,
				types.ErrOptionWithSkipRetry(),
			)

			final := FinalizeRelayError(localErr)

			require.Equal(t, http.StatusTooManyRequests, final.StatusCode)
			require.Equal(t, tc.message, final.Error())
			require.Equal(t, tc.errCode, final.GetErrorCode())
			require.IsType(t, &types.NewAPIError{}, final)
		})
	}
}

// 上游的额度耗尽 / 限流类 429 仍统一改写为 503 + 固定文案；
// 即使上游返回的错误 code 恰好也叫 insufficient_user_quota，也不能当成本站额度不足。
func TestFinalizeRelayErrorRemapsUpstreamQuotaErrorsTo503(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		err        *types.NewAPIError
		wantStatus int
	}{
		{
			name: "upstream 429 with insufficient_user_quota code",
			err: types.WithOpenAIError(types.OpenAIError{
				Message: "该令牌额度已用完",
				Type:    "upstream_error",
				Code:    string(types.ErrorCodeInsufficientUserQuota),
			}, http.StatusTooManyRequests),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "upstream 429 rate limited",
			err: types.NewOpenAIError(
				errors.New("rate limit exceeded"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusTooManyRequests,
			),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "upstream 402 payment required",
			err: types.NewOpenAIError(
				errors.New("insufficient balance"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusPaymentRequired,
			),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "upstream 403 forbidden",
			err: types.NewOpenAIError(
				errors.New("account disabled"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusForbidden,
			),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "upstream 503 already remapped",
			err:        NoAvailableAccountsError(),
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			final := FinalizeRelayError(tc.err)

			require.Equal(t, tc.wantStatus, final.StatusCode)
			require.Equal(t, NoAvailableAccountsMessage, final.Error())
		})
	}
}

// 与额度 / 限流无关的错误必须原样透出，不能被误判为上游账号池不可用。
func TestFinalizeRelayErrorKeepsOtherErrors(t *testing.T) {
	t.Parallel()

	badRequest := types.NewOpenAIError(
		errors.New("invalid request body"),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
	)
	final := FinalizeRelayError(badRequest)
	require.Equal(t, http.StatusBadRequest, final.StatusCode)
	require.Equal(t, "invalid request body", final.Error())

	internal := types.NewErrorWithStatusCode(
		errors.New("count token failed"),
		types.ErrorCodeCountTokenFailed,
		http.StatusInternalServerError,
	)
	final = FinalizeRelayError(internal)
	require.Equal(t, http.StatusInternalServerError, final.StatusCode)
	require.Equal(t, "count token failed", final.Error())

	require.Nil(t, FinalizeRelayError(nil))
}

// RelayErrorHandler（上游响应）仍需把上游 429 改写为 503 固定文案。
func TestRelayErrorHandlerRemapsUpstream429(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"quota exceeded","type":"insufficient_quota"}}`)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, http.StatusServiceUnavailable, newAPIError.StatusCode)
	require.Equal(t, NoAvailableAccountsMessage, newAPIError.Error())
}
