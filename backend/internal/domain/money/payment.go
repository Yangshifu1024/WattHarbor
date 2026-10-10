package money

// PaymentBizType 是支付与退款关联的业务类型：payment_order.biz_type 与
// refund_record.biz_type 共享同一词表。取值与 migrations/central_db/0001_init.sql
// 中这两个 ENUM 列一致。
//
// 与 wallet_txn.biz_type 是两组不同的词表（recharge/pay/refund/gift/freeze/
// unfreeze/adjust），禁止取并集：钱包流水记录的是账户资金方向，支付关联记录的是
// 本次收付款挂在哪笔业务上。同理，coupon_redemption.biz_type 是无 ENUM 约束的
// varchar，其可写子集只有 PaymentBizCharge。
type PaymentBizType string

const (
	// PaymentBizCharge 表示关联充电订单。
	PaymentBizCharge PaymentBizType = "charge"
	// PaymentBizWalletRecharge 表示关联钱包充值。
	PaymentBizWalletRecharge PaymentBizType = "wallet_recharge"
)

// Valid 判断是否为该词表已定义的取值。
func (b PaymentBizType) Valid() bool {
	switch b {
	case PaymentBizCharge, PaymentBizWalletRecharge:
		return true
	default:
		return false
	}
}

// AllPaymentBizTypes 是该词表的全量取值串，供入参白名单校验直接引用。
const AllPaymentBizTypes = "charge wallet_recharge"

// PaymentStatus 是 payment_order.status 列的支付单业务状态，用于到账确认、
// 渠道退款累计与充值来源追溯。取值与 migrations/central_db/0001_init.sql 中
// 该 ENUM 列一致。
//
// 注意与 order 包内 OrderPaymentStatus 区分：后者是 charge_order.payment_status，
// 描述订单侧到账与退款结果，由金额推导得出；本类型描述支付单自身的履约状态。
type PaymentStatus string

const (
	// PayStatusInitiated 表示支付单已创建、尚未收到渠道成功结果。
	PayStatusInitiated PaymentStatus = "initiated"
	// PayStatusPaid 表示渠道已确认全额到账。
	PayStatusPaid PaymentStatus = "paid"
	// PayStatusClosed 表示支付单在到账前关闭。
	PayStatusClosed PaymentStatus = "closed"
	// PayStatusRefunded 表示支付单对应款项已全额退回。
	PayStatusRefunded PaymentStatus = "refunded"
	// PayStatusPartialRefunded 表示支付单对应款项已退回一部分。
	PayStatusPartialRefunded PaymentStatus = "partial_refunded"
	// PayStatusFailed 表示支付单以失败结束。
	PayStatusFailed PaymentStatus = "failed"
)

// Valid 判断是否为该列已定义的取值。仅用于识别未知值，不决定任何状态能否流转；
// 读取路径遇到未知值时保持原值透传，不得据此拒绝或替换。
func (s PaymentStatus) Valid() bool {
	switch s {
	case PayStatusInitiated, PayStatusPaid, PayStatusClosed,
		PayStatusRefunded, PayStatusPartialRefunded, PayStatusFailed:
		return true
	default:
		return false
	}
}

// PaidLikeStatuses 是到账或已退回的取值集合：订单侧支付状态推导按此集合判定。
// 该集合是业务判定规则，不是全量取值。
const PaidLikeStatuses = "paid refunded partial_refunded"

// RefundablePaymentStatuses 是仍可发起渠道退款的支付单状态集合。
// 它比 PaidLikeStatuses 少 refunded——已全额退回的支付单不能再发起退款，
// 因此退款准入与可退额度校验必须用本集合，不得复用 PaidLikeStatuses。
const RefundablePaymentStatuses = "paid partial_refunded"

// RefundStatus 是 refund_record.status 列的退款单状态，用于申请、执行、重试与
// 人工复核的流转。取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
//
// 注意与 refund_review 的复核结论、wallet.RefundView.Status 的派生展示状态区分：
// 后两者是面向运营的聚合标签，取值不属于本词表。
type RefundStatus string

const (
	// RefundPending 表示退款已登记、等待执行。
	RefundPending RefundStatus = "pending"
	// RefundProcessing 表示退款已提交渠道、处理中。
	RefundProcessing RefundStatus = "processing"
	// RefundSuccess 表示退款已成功到账。
	RefundSuccess RefundStatus = "success"
	// RefundFailed 表示渠道执行失败，可按重试策略再次发起。
	RefundFailed RefundStatus = "failed"
	// RefundRejected 表示人工复核未通过，不再重试。
	RefundRejected RefundStatus = "rejected"
)

// Valid 判断是否为该列已定义的取值。
func (s RefundStatus) Valid() bool {
	switch s {
	case RefundPending, RefundProcessing, RefundSuccess, RefundFailed, RefundRejected:
		return true
	default:
		return false
	}
}

// AllRefundStatuses 是该列的全量取值串，供入参白名单校验直接引用。
const AllRefundStatuses = "pending processing success failed rejected"

// UnsettledRefundStatuses 是在途退款占用的取值集合：可退额度校验须扣除这些记录，
// 否则同一笔款项会被重复预占。该集合是额度判定规则，不是全量取值。
const UnsettledRefundStatuses = "pending processing"
