package types

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsClientCanceled 保护"客户端取消判定"契约：context.Canceled（含被 NewAPIError 包裹、
// 多层 fmt.Errorf 包裹）判为 true；DeadlineExceeded 与普通错误判为 false；显式的
// ErrorCodeClientClosedRequest 即使底层 err 已被替换也判为 true。
func TestIsClientCanceled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "raw context.Canceled", err: context.Canceled, want: true},
		{name: "wrapped context.Canceled", err: fmt.Errorf("read failed: %w", context.Canceled), want: true},
		{
			name: "NewAPIError wrapping context.Canceled unwraps",
			err:  NewError(context.Canceled, ErrorCodeReadResponseBodyFailed),
			want: true,
		},
		{
			name: "NewAPIError with client-closed code but replaced err",
			err: NewError(errors.New("upstream error: do request failed"), ErrorCodeClientClosedRequest,
				ErrOptionWithHideErrMsg("upstream error: do request failed")),
			want: true,
		},
		{name: "context.DeadlineExceeded is not client cancel", err: context.DeadlineExceeded, want: false},
		{name: "unrelated error", err: errors.New("boom"), want: false},
		{
			name: "NewAPIError with unrelated code and err",
			err:  NewError(errors.New("boom"), ErrorCodeBadResponseBody),
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsClientCanceled(tt.err))
		})
	}
}

// TestClientClosedRequestErrorOptions 确认为客户端取消构造的错误同时具备
// SkipRetry（不换渠道重试）与 499 状态码，这是上层拦截重试/禁用的前提。
func TestClientClosedRequestErrorOptions(t *testing.T) {
	t.Parallel()

	err := NewError(context.Canceled, ErrorCodeClientClosedRequest,
		ErrOptionWithStatusCode(499),
		ErrOptionWithSkipRetry(),
		ErrOptionWithNoRecordErrorLog())

	require.NotNil(t, err)
	assert.Equal(t, ErrorCodeClientClosedRequest, err.GetErrorCode())
	assert.Equal(t, 499, err.StatusCode)
	assert.True(t, IsSkipRetryError(err))
	assert.False(t, IsRecordErrorLog(err))
	assert.True(t, IsClientCanceled(err))
	// http 标准库未定义 499，这里确认我们用的就是它。
	assert.NotEqual(t, http.StatusInternalServerError, err.StatusCode)
}
