package order

// OrderStartSource 是订单的启动来源，由 SQL CASE 依据是否刷卡、支付方式派生，
// **数据库中没有对应列**，取值不来自任何写入路径。
//
// 判定顺序（见 orderStartSourceSQL）：存在刷卡记录即为 card，
// 否则按支付方式分 balance 与 payment，其余为 null。
// 三个分支值与该常量一一对应，改动 SQL 分支时必须同步本词表。
type OrderStartSource string

const (
	// StartSourcePayment 表示扫码支付启动。
	StartSourcePayment OrderStartSource = "payment"
	// StartSourceBalance 表示余额支付启动。
	StartSourceBalance OrderStartSource = "balance"
	// StartSourceCard 表示在线卡刷卡启动。
	StartSourceCard OrderStartSource = "card"
)

// Known 判断是否属于该派生值的取值集合。
// 仅用于识别；展示字段无来源时为 nil 而非空串，筛选参数为空串表示不筛选，
// 三种状态互不等价，不得据此互相转换。
func (s OrderStartSource) Known() bool {
	switch s {
	case StartSourcePayment, StartSourceBalance, StartSourceCard:
		return true
	default:
		return false
	}
}

// AllStartSources 是该派生值的全量取值串，供后台列表筛选的白名单校验直接引用。
// 顺序与 orderStartSourceSQL 的分支顺序一致。
const AllStartSources = "payment balance card"

// RefundViewStatus 是订单的退款进度展示状态，由 SQL CASE 依据支付单退款金额、
// 订单状态与在途退款单派生，**数据库中没有对应列**。
//
// 派生规则（见 orderColumns 的 refund_status 表达式）：存在在途退款单或订单处于
// refunding 时为 processing，其次支付单全额退回为 refunded，
// 其次部分退款为 partial_refunded，均无则 none。
//
// 注意与 domain/money.RefundStatus 分立：后者是 refund_record.status 的持久化词表
// （pending／processing／success／failed／rejected），本类型是面向运营的聚合标签，
// 两者取值有交集但不是同一层语义，不得互相赋值或复用判定方法。
type RefundViewStatus string

const (
	// RefundViewNone 表示尚未发起退款。
	RefundViewNone RefundViewStatus = "none"
	// RefundViewProcessing 表示退款已受理且未完成。
	RefundViewProcessing RefundViewStatus = "processing"
	// RefundViewPartialRefunded 表示已退回部分款项。
	RefundViewPartialRefunded RefundViewStatus = "partial_refunded"
	// RefundViewRefunded 表示款项已全额退回。
	RefundViewRefunded RefundViewStatus = "refunded"
)

// Known 判断是否属于该派生值的取值集合。
// 该列由 ELSE 'none' 收口，不会产出空串；仅用于识别，未知值按原值透传。
func (s RefundViewStatus) Known() bool {
	switch s {
	case RefundViewNone, RefundViewProcessing, RefundViewPartialRefunded, RefundViewRefunded:
		return true
	default:
		return false
	}
}

// AllRefundViewStatuses 是该派生值的全量取值串。
// 当前订单列表没有 refund_status 筛选入口，暂登记在此供将来接线；新增取值不会自动进入筛选白名单。
const AllRefundViewStatuses = "none processing partial_refunded refunded"
