package payment

import (
	"gorm.io/gorm"
	"wattharbor/internal/domain/money"
)

// LateRefundRecorder 在支付回调事务内保存迟到付款的退款申请，由 settlement 实现。
type LateRefundRecorder interface {
	// RecordLateInTx 在调用方已锁支付与意图的 tx 内创建自动退款及 Outbox。
	// in 沿用支付和退款单号，eventID 由通知摘要生成；失败须回滚整个支付回调事务。
	// nil 只表示待退款记录已写入，不表示渠道资金已退回；调用方负责提交。
	RecordLateInTx(tx *gorm.DB, in money.RefundDraft, eventID string) error
}
