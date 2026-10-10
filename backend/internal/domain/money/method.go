package money

// PayMethod 是中央库 payment_order.pay_method 列的支付方式。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （wechat/balance/mixed/coupon）。
//
// 该列没有 DEFAULT；每个写入点必须显式赋值。mixed 表示组合支付的实际落账方式，
// coupon 表示以券抵扣部分金额，两者在当前 Go 代码中没有独立写入路径，
// 保留以完整覆盖列约束。
type PayMethod string

const (
	// PayMethodWechat 表示微信支付。
	PayMethodWechat PayMethod = "wechat"
	// PayMethodBalance 表示余额支付。
	PayMethodBalance PayMethod = "balance"
	// PayMethodMixed 表示组合支付；当前无独立写入路径。
	PayMethodMixed PayMethod = "mixed"
	// PayMethodCoupon 表示券抵扣；当前无独立写入路径。
	PayMethodCoupon PayMethod = "coupon"
)

// Known 判断是否属于该列的数据库合法集合（四值）。
func (m PayMethod) Known() bool {
	switch m {
	case PayMethodWechat, PayMethodBalance, PayMethodMixed, PayMethodCoupon:
		return true
	default:
		return false
	}
}

// AllPayMethods 是 PayMethod 的全量取值串，供入参白名单校验直接引用。
const AllPayMethods = "wechat balance mixed coupon"

// PaymentIntentStatus 是中央库 charge_payment_intent.status 列的支付意图状态，
// 用于预下单到账确认与意图关闭。取值与 migrations/central_db/0001_init.sql
// 中该 ENUM 列一致（initiated/paid/expired/closed/refund_required）。
//
// 注意与 PaymentStatus 分立：后者是 payment_order.status 的六值词表。
// 两者共享 initiated/paid 但含义不同（意图已创建 vs 支付单已到账），
// 且 payment_order.status 有 closed 而意图的 closed 表示意图作废。
// 不得因字面量相同互相赋值。
type PaymentIntentStatus string

const (
	// IntentInitiated 表示意图已创建、尚未收到渠道成功结果。
	IntentInitiated PaymentIntentStatus = "initiated"
	// IntentPaid 表示渠道已确认到账。
	IntentPaid PaymentIntentStatus = "paid"
	// IntentExpired 表示意图超时失效。
	IntentExpired PaymentIntentStatus = "expired"
	// IntentClosed 表示意图主动关闭。
	IntentClosed PaymentIntentStatus = "closed"
	// IntentRefundRequired 表示到账后需要发起退款。
	IntentRefundRequired PaymentIntentStatus = "refund_required"
)

// Known 判断是否属于该列的数据库合法集合（五值）。
func (s PaymentIntentStatus) Known() bool {
	switch s {
	case IntentInitiated, IntentPaid, IntentExpired, IntentClosed, IntentRefundRequired:
		return true
	default:
		return false
	}
}

// AllPaymentIntentStatuses 是该列的全量取值串，顺序与上方常量声明一致。
const AllPaymentIntentStatuses = "initiated paid expired closed refund_required"

// ExecutionPolicy 是中央库 refund_record.execution_policy 列的退款执行策略，
// 决定退款单进入自动执行还是人工双签复核。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （manual_review/automatic），数据库默认值为 manual_review。
//
// 与 wallet 侧的 wallet_refund.status 分立：后者虽然也有 manual_review 取值，
// 但那是退款分笔的状态词表（pending/processing/needs_review/success/
// manual_review/rejected），与本策略无类型交互，不得共用常量。
type ExecutionPolicy string

const (
	// ExecutionManualReview 表示退款须人工复核并双签后执行。
	ExecutionManualReview ExecutionPolicy = "manual_review"
	// ExecutionAutomatic 表示退款自动执行，无需人工复核。
	ExecutionAutomatic ExecutionPolicy = "automatic"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
func (p ExecutionPolicy) Known() bool {
	return p == ExecutionManualReview || p == ExecutionAutomatic
}

// AllExecutionPolicies 是该列的全量取值串，供入参白名单校验直接引用。
const AllExecutionPolicies = "manual_review automatic"
