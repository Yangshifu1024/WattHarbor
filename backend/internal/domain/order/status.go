// Package order 定义订单状态、派生业务状态与支付状态的取值词汇，供订单用例、
// 充电用例与查询组合包共用。状态推进、资金处理与退款判定仍由所属用例负责，
// 本包不引用数据库模型、HTTP 框架或服务实现。
package order

// OrderStatus 是 charge_order.status 列的订单内部状态，用于启动、结束、退款与展示。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
type OrderStatus string

const (
	// StatusPendingPayment 表示订单已创建但尚未收到支付成功结果。
	StatusPendingPayment OrderStatus = "pending_payment"
	// StatusPaid 表示支付成功、尚未开始充电。
	StatusPaid OrderStatus = "paid"
	// StatusCharging 表示设备已启动并处于充电中。
	StatusCharging OrderStatus = "charging"
	// StatusCompleted 表示充电正常结束。
	StatusCompleted OrderStatus = "completed"
	// StatusCancelled 表示订单在开始充电前被取消。
	StatusCancelled OrderStatus = "cancelled"
	// StatusFailed 表示订单因启动或计量原因未能完成。
	StatusFailed OrderStatus = "failed"
	// StatusRefunding 表示退款流程已受理，不表示款项已退回。
	StatusRefunding OrderStatus = "refunding"
	// StatusRefunded 表示款项已退回原路或余额。
	StatusRefunded OrderStatus = "refunded"
)

// Valid 判断是否为该列已定义的取值。仅用于识别未知值，不决定任何状态能否推进；
// 读取路径遇到未知值时保持原值透传，不得据此拒绝或替换。
func (s OrderStatus) Valid() bool {
	switch s {
	case StatusPendingPayment, StatusPaid, StatusCharging, StatusCompleted,
		StatusCancelled, StatusFailed, StatusRefunding, StatusRefunded:
		return true
	default:
		return false
	}
}

// OrderBusinessStatus 是 charge_order.business_status 列的展示状态，由数据库按
// status 以 STORED 生成列折叠得到（pending_payment 与 paid 归为 pending_start，
// charging 归为 charging，其余归为 completed）。Go 侧只读，不写入。
type OrderBusinessStatus string

const (
	// BusinessPendingStart 表示待支付或已支付、尚未开始充电。
	BusinessPendingStart OrderBusinessStatus = "pending_start"
	// BusinessCharging 表示充电中。
	BusinessCharging OrderBusinessStatus = "charging"
	// BusinessCompleted 表示已进入其余终态，包括取消、失败与退款。
	BusinessCompleted OrderBusinessStatus = "completed"
)

// Valid 判断是否为该生成列已定义的取值。
func (s OrderBusinessStatus) Valid() bool {
	switch s {
	case BusinessPendingStart, BusinessCharging, BusinessCompleted:
		return true
	default:
		return false
	}
}

// OrderPaymentStatus 是 charge_order.payment_status 列的实际到账与退款结果状态，
// 与 OrderStatus 独立：订单处于 refunded 时支付状态可能是 refunded 或 partial_refunded。
type OrderPaymentStatus string

const (
	// PaymentPending 表示尚未到账。
	PaymentPending OrderPaymentStatus = "pending"
	// PaymentPaid 表示款项已全额到账。
	PaymentPaid OrderPaymentStatus = "paid"
	// PaymentRefunded 表示款项已全额退回。
	PaymentRefunded OrderPaymentStatus = "refunded"
	// PaymentPartialRefunded 表示已退回部分款项。
	PaymentPartialRefunded OrderPaymentStatus = "partial_refunded"
)

// Valid 判断是否为该列已定义的取值。
func (s OrderPaymentStatus) Valid() bool {
	switch s {
	case PaymentPending, PaymentPaid, PaymentRefunded, PaymentPartialRefunded:
		return true
	default:
		return false
	}
}

// 以下三个常量是 HTTP 入参白名单与审核准入集合的取值串，供以空格分隔的
// 白名单校验直接引用。改写为常量只消除散落重复的字面量，取值内容与顺序
// 与原字符串逐字节一致，不改变任何接口的校验行为。
// 新增状态取值时不会自动进入这些白名单，需要按业务准入规则显式补充。

// AllStatuses 是 OrderStatus 的全量取值串，顺序与上方常量声明一致。
const AllStatuses = "pending_payment paid charging completed cancelled failed refunding refunded"

// AllBusinessStatuses 是 OrderBusinessStatus 的全量取值串。
const AllBusinessStatuses = "pending_start charging completed"

// AllPaymentStatuses 是 OrderPaymentStatus 的全量取值串。
const AllPaymentStatuses = "pending paid refunded partial_refunded"

// AllChannelRefundableStatuses 是退款审核准入白名单命中的 OrderStatus 子集，
// 取值串与审核判定处的原字符串逐字节一致（含 cancelled）。
// 该集合是准入规则而非全量取值：其中 refunded 会被随后的单独判定排除，
// refunding 则本就不在其中——已受理的退款不得重复发起。
const AllChannelRefundableStatuses = "completed cancelled failed refunded"
