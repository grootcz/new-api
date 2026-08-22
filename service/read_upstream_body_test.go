package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canceledReader 在读取时返回 context.Canceled，模拟客户端断开后绑定的 request context
// 被取消、transport 令 io.ReadAll 失败的情形。
type canceledReader struct{}

func (canceledReader) Read(p []byte) (int, error) { return 0, context.Canceled }

// TestReadUpstreamBody 保护"读取上游响应体并归一化取消语义"的契约。
func TestReadUpstreamBody(t *testing.T) {
	t.Parallel()

	t.Run("client cancel maps to 499 skip-retry no-log error", func(t *testing.T) {
		t.Parallel()
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(canceledReader{}),
		}

		body, apiErr := ReadUpstreamBody(resp)

		require.Nil(t, body)
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCodeClientClosedRequest, apiErr.GetErrorCode())
		assert.Equal(t, StatusClientClosedRequest, apiErr.StatusCode)
		assert.True(t, types.IsSkipRetryError(apiErr), "客户端取消不应触发换渠道重试")
		assert.False(t, types.IsRecordErrorLog(apiErr), "客户端取消不应记录错误日志")
		assert.True(t, types.IsClientCanceled(apiErr))
	})

	t.Run("successful read returns body and nil error", func(t *testing.T) {
		t.Parallel()
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}

		body, apiErr := ReadUpstreamBody(resp)

		require.Nil(t, apiErr)
		assert.Equal(t, `{"ok":true}`, string(body))
	})

	t.Run("non-cancel read error maps to read-body-failed 500", func(t *testing.T) {
		t.Parallel()
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(errReader{err: errors.New("connection reset")}),
		}

		body, apiErr := ReadUpstreamBody(resp)

		require.Nil(t, body)
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCodeReadResponseBodyFailed, apiErr.GetErrorCode())
		assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
		assert.False(t, types.IsClientCanceled(apiErr))
		assert.False(t, types.IsSkipRetryError(apiErr), "普通读取错误仍可按原有策略重试")
	})

	t.Run("nil body returns read-body-failed", func(t *testing.T) {
		t.Parallel()
		body, apiErr := ReadUpstreamBody(&http.Response{StatusCode: http.StatusOK})

		require.Nil(t, body)
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCodeReadResponseBodyFailed, apiErr.GetErrorCode())
	})
}

type errReader struct{ err error }

func (r errReader) Read(p []byte) (int, error) { return 0, r.err }
