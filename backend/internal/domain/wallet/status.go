// Package wallet 定义钱包账户与流水共用的状态取值词汇，供钱包用例、
// 在线卡用例与用户查询共用。资金写入、预占与风险处置仍由所属用例负责，
// 本包不引用数据库模型、HTTP 框架或服务实现。
package wallet

// AccountStatus 是中央库 wallet_account.status 列的账户状态，用于充值、退款与出账准入。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（active/frozen）。
//
// 与 identity 包的 UserStatus 分立：两列取值串完全相同（active/frozen），
// 但分别描述钱包账户与用户账号，不得互相赋值或复用判定方法。
// 已批准风险可在冻结账户继续预占，因此冻结不等于禁止一切资金操作。
type AccountStatus string

const (
	// AccountActive 表示账户可用，正常接受充值、退款与出账。
	AccountActive AccountStatus = "active"
	// AccountFrozen 表示账户冻结，出账被拒绝；已批准风险仍可预占。
	AccountFrozen AccountStatus = "frozen"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
// 仅用于识别未知值；读取路径遇到未知值时保持原值透传，不得据此拒绝或替换。
func (s AccountStatus) Known() bool {
	return s == AccountActive || s == AccountFrozen
}

// AllAccountStatuses 是 AccountStatus 的全量取值串，供入参白名单校验直接引用。
const AllAccountStatuses = "active frozen"

// FreezeStatus 是中央库 risk_freeze_log.status 列的风险冻结记录状态，
// 用于记录一次冻结动作的当前结论。取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
//
// 与 AccountStatus 分立：本类型描述「一条冻结记录处于什么阶段」，
// AccountStatus 描述「账户当前是否被冻结」，二者不是同一个事实。
type FreezeStatus string

const (
	// FreezeFrozen 表示冻结记录处于生效状态。
	FreezeFrozen FreezeStatus = "frozen"
	// FreezeUnfrozen 表示冻结已被解除。
	FreezeUnfrozen FreezeStatus = "unfrozen"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
func (s FreezeStatus) Known() bool {
	return s == FreezeFrozen || s == FreezeUnfrozen
}
