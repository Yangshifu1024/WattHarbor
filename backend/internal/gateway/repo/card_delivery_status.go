package repo

// CardDeliveryStatus 是网关库 card_event_delivery.status 列的投递状态。
// 取值与 migrations/gateway_db/0001_init.sql 中该 ENUM 列一致（pending/done）。
//
// 该表只在网关库中存在，不经服务间契约传递，worker 通过 gateway 的仓储函数间接使用，
// 因此类型留在 gateway/repo 而非 contracts 或 domain。
//
// 与 contracts/gateway.CardAdvanceOutcome 分立：后者是 worker 上报推进动作的外部契约输入
// （retry/done），表达「这次调用要求做什么」；本类型表达「记录当前处于什么状态」。
// 两个词表只有 done 同值，其余取值不同，不得互相赋值或复用判定。
//
// pending 既是入库初值（由数据库默认值填充）也是重试态：
// AdvanceCardDelivery 的重试分支只增加次数并延后执行，不改 status。
type CardDeliveryStatus string

const (
	// CardDeliveryPending 表示待投递，由数据库默认值填充，不由 Go 侧赋值。
	CardDeliveryPending CardDeliveryStatus = "pending"
	// CardDeliveryDone 表示投递完成，worker 确认不再重试时写入。
	CardDeliveryDone CardDeliveryStatus = "done"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
// 当前无调用方：读取侧用 SQL 条件筛选，写入侧只用常量赋值，均不需要识别未知值。
// 保留用于将来需要区分已知与未知取值的读取路径；
// 届时仍不得据此拒绝或替换，读取路径遇未知值必须原值透传。
func (s CardDeliveryStatus) Known() bool {
	return s == CardDeliveryPending || s == CardDeliveryDone
}
