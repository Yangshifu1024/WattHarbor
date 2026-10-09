package payment

import (
	"chargepilot/internal/domain/money"
	"gorm.io/gorm"
)

// LateRefundRecorder 由 settlement 实现，保持 payment 对结算实现无反向引用。
type LateRefundRecorder interface {
	// RecordLateInTx 在调用方已锁支付与意图的 tx 内创建自动退款及原 Outbox。
	// in 沿用原支付和退款身份，eventID 为通知摘要派生身份；错误要求整个回调回滚。
	// nil 只表示待退款记录已写入，不表示渠道资金已退回；调用方负责提交。
	RecordLateInTx(tx *gorm.DB, in money.RefundDraft, eventID string) error
}
