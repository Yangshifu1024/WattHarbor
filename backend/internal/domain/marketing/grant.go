// Package marketing 定义优惠券发放记录的状态与来源取值词汇，供营销用例、
// 用户查询与运营后台共用。发放条件、报价与核销判定仍由所属用例负责，
// 本包不引用数据库模型、HTTP 框架或服务实现。
package marketing

// GrantStatus 是 coupon_grant.status 列的券状态，用于可用判定、核销与列表展示。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
// 注意与 coupon.status（active/disabled）区分：后者是券模板状态，词表不同。
type GrantStatus string

const (
	// GrantUnused 表示券已发放且未核销；是否已过期由 expired_at 单独判断。
	GrantUnused GrantStatus = "unused"
	// GrantUsed 表示券已核销并记录了使用的支付订单。
	GrantUsed GrantStatus = "used"
	// GrantExpired 表示券已到期作废，不再可用于报价。
	GrantExpired GrantStatus = "expired"
)

// Valid 判断是否为该列已定义的取值。仅用于识别未知值，不决定任何状态能否流转；
// 读取路径遇到未知值时保持原值透传，不得据此拒绝或替换。
func (s GrantStatus) Valid() bool {
	switch s {
	case GrantUnused, GrantUsed, GrantExpired:
		return true
	default:
		return false
	}
}

// GrantSource 是 coupon_grant.grant_source 列的发放来源，用于发放归因与
// 运营统计。与 GrantStatus 分立：来源描述「因何而发」，状态描述「当前能否用」，
// 同一张券可以在来源不变的情况下由 unused 变为 used。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
type GrantSource string

const (
	// GrantSourceRegister 表示注册赠送。当前代码无写入路径，保留以完整覆盖列约束。
	GrantSourceRegister GrantSource = "register"
	// GrantSourceActivity 表示命中活动规则自动发放。
	GrantSourceActivity GrantSource = "activity"
	// GrantSourceInvite 表示邀请关系发放。当前代码无写入路径，保留以完整覆盖列约束。
	GrantSourceInvite GrantSource = "invite"
	// GrantSourceManual 表示运营后台手动发放。
	GrantSourceManual GrantSource = "manual"
	// GrantSourceInviteReward 表示邀请奖励发放给他人的券。
	GrantSourceInviteReward GrantSource = "invite_reward"
)

// Valid 判断是否为该列已定义的取值。
func (s GrantSource) Valid() bool {
	switch s {
	case GrantSourceRegister, GrantSourceActivity, GrantSourceInvite,
		GrantSourceManual, GrantSourceInviteReward:
		return true
	default:
		return false
	}
}
