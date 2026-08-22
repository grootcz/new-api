package service

import (
	"net/http/httptest"
	"sync"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFunding 记录 Settle/Refund 调用，用于在无数据库的情况下断言计费不变量。
type fakeFunding struct {
	mu          sync.Mutex
	settleCalls []int
	refundCalls int
}

func (f *fakeFunding) Source() string              { return BillingSourceWallet }
func (f *fakeFunding) PreConsume(amount int) error { return nil }
func (f *fakeFunding) Settle(delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settleCalls = append(f.settleCalls, delta)
	return nil
}
func (f *fakeFunding) Refund() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refundCalls++
	return nil
}
func (f *fakeFunding) settleCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.settleCalls)
}

// newTestBillingSession 构造一个已预扣 preConsumed 的会话（token 也已扣，以便 Refund 有可退状态）。
func newTestBillingSession(funding FundingSource, preConsumed int) *BillingSession {
	return &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{IsPlayground: true}, // Playground 跳过真实 token DB 调用
		funding:          funding,
		preConsumedQuota: preConsumed,
		tokenConsumed:    preConsumed,
	}
}

// TestSettleAtPreConsumedQuotaIsNoOpAndBlocksRefund 锁定"客户端断开按预扣额结算"的核心不变量：
// 当实际结算额 == 预扣额时，funding.Settle 不被调用（delta=0），会话标记为 settled，
// 且后续 Refund 被短路——即既不补扣也不退款，净扣费恰为预扣额。
func TestSettleAtPreConsumedQuotaIsNoOpAndBlocksRefund(t *testing.T) {
	t.Parallel()

	const preConsumed = 3500
	funding := &fakeFunding{}
	s := newTestBillingSession(funding, preConsumed)

	require.True(t, s.NeedsRefund(), "结算前应存在可退预扣状态")

	// 模拟 controller 断开分支：SettleBilling 内部对 delta==0 调用 Settle(actualQuota)。
	err := s.Settle(preConsumed)
	require.NoError(t, err)

	assert.Zero(t, funding.settleCount(), "delta==0 时不应调整资金来源")
	assert.True(t, s.settled, "结算后会话应标记 settled")
	assert.False(t, s.NeedsRefund(), "结算后应阻止退款")

	// 断开分支之后即使再次触发 Refund，也不应真正退款（settled 会短路，不 spawn 退款 goroutine）。
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	s.Refund(c)
	assert.False(t, s.refunded, "已结算的会话不应进入退款流程")
}

// TestRefundStillWorksForNonCancelFailures 回归保护：非取消失败仍走全额退款语义。
func TestRefundStillWorksForNonCancelFailures(t *testing.T) {
	t.Parallel()

	const preConsumed = 3500
	funding := &fakeFunding{}
	s := newTestBillingSession(funding, preConsumed)

	require.True(t, s.NeedsRefund())

	// 未结算直接退款（对应 newAPIError 非客户端取消的普通失败路径）。
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	s.Refund(c)
	assert.True(t, s.refunded, "普通失败应进入退款流程")
	assert.Zero(t, funding.settleCount(), "退款路径不应结算资金来源")
}
