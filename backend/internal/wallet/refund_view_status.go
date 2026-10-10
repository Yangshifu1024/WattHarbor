package wallet

// RefundViewStatus 是一笔退款申请面向用户端的展示聚合标签，
// 由 refundView 从分笔渠道结果与风险审核结论两条独立链派生，
// **数据库中没有对应列**，取值不来自任何写入路径，也不是任何持久化列的投影。
//
// 派生输入（见 QueryService.Refunds 及其批量补齐的 RefundDetails）：
//   - 分笔状态来自 refund_record.status（经 WalletRefundPartRow.Status）；
//   - 审核结论来自 wallet_risk_review.approved；
//   - 是否存在关联冻结来自 wallet_risk_freeze_link 的计数。
//
// 注意与 domain/money.RefundStatus 分立：后者是 refund_record.status 的持久化词表
// （pending／processing／success／failed／rejected），用于退款单的申请与执行流转；
// 本类型是跨两张表聚合出的展示结论，与之取值有交集但不是同一层语义，
// 不得互相赋值，也不得复用两者的判定方法。
// 特别是 needs_review：它只出现在本词表，**不属于 money.RefundStatus 的取值集合**，
// money.RefundStatus.Valid() 对它返回 false，这是刻意的，不是漏登记。
type RefundViewStatus string

const (
	// RefundViewPending 表示尚无分笔进展，或仅有在途分笔但未进入渠道处理。
	RefundViewPending RefundViewStatus = "pending"
	// RefundViewProcessing 表示已有分笔处于渠道处理中，且没有需要复核的分笔。
	RefundViewProcessing RefundViewStatus = "processing"
	// RefundViewNeedsReview 表示存在终态失败或需复核的分笔，等待人工处理。
	RefundViewNeedsReview RefundViewStatus = "needs_review"
	// RefundViewSuccess 表示已确认退回到账的金额等于申请金额。
	RefundViewSuccess RefundViewStatus = "success"
	// RefundViewManualReview 表示存在关联冻结且尚无风控审核结论。
	RefundViewManualReview RefundViewStatus = "manual_review"
	// RefundViewRejected 表示风控审核结论为拒绝。
	RefundViewRejected RefundViewStatus = "rejected"
)

// Known 判断是否属于该派生值的取值集合。
// 仅用于识别；本类型只由 refundView 内部赋值，读路径不存在外部输入，
// 未知值不得据此拒绝、替换或降级，仍按原值透传。
func (s RefundViewStatus) Known() bool {
	switch s {
	case RefundViewPending, RefundViewProcessing, RefundViewNeedsReview,
		RefundViewSuccess, RefundViewManualReview, RefundViewRejected:
		return true
	default:
		return false
	}
}
