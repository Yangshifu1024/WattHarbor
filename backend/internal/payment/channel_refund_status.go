package payment

// ChannelRefundStatus 是渠道退款响应里的退款状态，取自渠道响应字段，
// 原样透传给结算执行器，本仓库不落该值的独立列。
//
// 词表来自渠道官方文档：SUCCESS 退款成功、CLOSED 退款关闭、
// PROCESSING 退款处理中、ABNORMAL 退款异常。取值**大小写与渠道响应逐字节一致**，
// 本地不得改写成小写，也不得做大小写不敏感的归一。
//
// 集合性质判定：**开放集合，不是封闭枚举**。依据有三：
// 一、写入侧是纯透传。refundResult 对 status 不做 switch，只在 SUCCESS 且缺成功时刻时
// 判回执不完整，其余取值一律原值放进 RefundResult.Status；
// 二、SDK 的 refunddomestic.Status 是 string 底层类型，无 JSON 绑定校验，
// 渠道新增取值不会被客户端解析层拦下；
// 三、执行侧的 switch default 是**幂等重放/并发冲突闸门**（未知取值不得动资金、不改状态、
// 返回 ErrRefundConflict 等待重试），不是词表校验——它拒绝的是"无法判定该不该动资金"，
// 而不是"不在已知集合内"。只要把 default 改成放行，未知取值就会沿着 SUCCESS 之后的
// 入账路径直接出账，那才是真正危险的收窄。
//
// 因此 Known 只用于识别与观测（日志、审计、灰度统计），**任何路径都不得拿它拒绝或改写取值**：
// 渠道返回未登记的新状态时，原值必须到达执行器，由执行器保持退款在途。
//
// 与 domain/money.RefundStatus（refund_record.status 列）没有任何映射关系：
// 两者取值的大小写与语义都不同（success 对 SUCCESS、failed 对 CLOSED/ABNORMAL、
// pending 与 rejected 在渠道侧无对应），且退款单状态由执行器按资金事务独立流转。
// 严禁把本类型并入 RefundStatus，或在二者之间建立转换函数。
type ChannelRefundStatus string

const (
	// ChannelRefundStatusSuccess 表示渠道确认退款成功，须有渠道确认时刻才可入账。
	ChannelRefundStatusSuccess ChannelRefundStatus = "SUCCESS"
	// ChannelRefundStatusProcessing 表示渠道仍在处理，保持在途等待下次查询。
	ChannelRefundStatusProcessing ChannelRefundStatus = "PROCESSING"
	// ChannelRefundStatusClosed 表示退款已关闭，视为终态失败并释放钱包预占。
	ChannelRefundStatusClosed ChannelRefundStatus = "CLOSED"
	// ChannelRefundStatusAbnormal 表示退款异常，视为终态失败并释放钱包预占。
	ChannelRefundStatusAbnormal ChannelRefundStatus = "ABNORMAL"
)

// Known 判断取值是否属于上述已登记词表。**只用于识别，不得用于拒绝**：
// 未知取值必须原样保留并透传，否则渠道新增状态会让退款永远停留在在途。
func (s ChannelRefundStatus) Known() bool {
	switch s {
	case ChannelRefundStatusSuccess, ChannelRefundStatusProcessing,
		ChannelRefundStatusClosed, ChannelRefundStatusAbnormal:
		return true
	default:
		return false
	}
}
