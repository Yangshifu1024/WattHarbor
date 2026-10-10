package marketing

// 本文件集中优惠券模板与活动规则的列取值词汇，供营销用例、后台列表与活动发券共用。
// 每个类型对应一张表的单个列；取值相同而语义不同的列分立，不合并为单一状态类型。

// CouponStatus 是 coupon.status 列的券模板启停状态，用于模板列表、更新写入与发放准入。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（active / disabled）。
// 与 ActivityStatus 的取值串完全相同，但分属 coupon 与 coupon_activity_rule 两张表，
// 语义不同，不得合并；与 marketingdomain.GrantStatus（coupon_grant.status 的
// unused／used／expired）也不是同一词表，同样不得合并。
type CouponStatus string

const (
	// CouponActive 表示模板启用，允许发放。
	CouponActive CouponStatus = "active"
	// CouponDisabled 表示模板停用，不再接受发放；已发出的券能否使用仍由 GrantStatus 判断。
	CouponDisabled CouponStatus = "disabled"
)

// Known 判断是否属于 coupon.status 的合法集合（两值）。
// 仅用于识别未知值：读取路径遇到未知值保持原值原样透传，不得据此拒绝、替换或降级；
// 是否允许写入由各操作自己的校验决定，不在此处收口。
func (s CouponStatus) Known() bool {
	switch s {
	case CouponActive, CouponDisabled:
		return true
	default:
		return false
	}
}

// ActivityStatus 是 coupon_activity_rule.status 列的活动规则启停状态，
// 用于活动配置的写入、列表筛选与加锁发放时的可用判定。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（active / disabled）。
// 与 CouponStatus 取值串相同但分属不同业务域，按基本规则 4 保持两个类型；
// 校验活动奖励券是否停用时读取的是 coupon 表，应使用 CouponStatus 而非本类型。
type ActivityStatus string

const (
	// ActivityActive 表示活动规则启用。
	ActivityActive ActivityStatus = "active"
	// ActivityDisabled 表示活动规则停用，不再命中发券。
	ActivityDisabled ActivityStatus = "disabled"
)

// Known 判断是否属于 coupon_activity_rule.status 的合法集合（两值）。
// 仅用于识别未知值，读取路径保持原值透传。
func (s ActivityStatus) Known() bool {
	switch s {
	case ActivityActive, ActivityDisabled:
		return true
	default:
		return false
	}
}

// DiscountType 是 coupon.discount_type 列的优惠计算类型，
// 用于模板写入、优惠参数互斥校验、报价计算与用户端券展示。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（amount／percentage／time_free）。
// 三种类型对应的优惠金额列互斥：amount 读 discount_value_cents，percentage 读 discount_percent，
// time_free 读 free_minutes；词表不随优惠列的新增而扩展。
type DiscountType string

const (
	// DiscountAmount 表示按分值直减，读取 discount_value_cents。
	DiscountAmount DiscountType = "amount"
	// DiscountPercentage 表示按应付比例折扣，读取 discount_percent。
	DiscountPercentage DiscountType = "percentage"
	// DiscountTimeFree 表示按免费时长优惠，读取 free_minutes。
	DiscountTimeFree DiscountType = "time_free"
)

// Known 判断是否属于 coupon.discount_type 的合法集合（三值）。
// 仅用于识别未知值，读取路径保持原值透传；报价计算的取值判定见 rules_coupon_discount.go。
func (d DiscountType) Known() bool {
	switch d {
	case DiscountAmount, DiscountPercentage, DiscountTimeFree:
		return true
	default:
		return false
	}
}

// TriggerType 是 coupon_activity_rule.trigger_type 列的活动触发类型，
// 用于活动配置校验、发券候选筛选与规则锁定后的复查。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （first_recharge／invite_reward／threshold_redeem／holiday）。
// 与 marketingdomain.GrantSourceInviteReward 同为 invite_reward 但不同义：
// 前者描述「因何触发活动」，后者描述「券因何发放」（coupon_grant.grant_source），
// 两者必须保持不同类型，不得合并为同一常量。
type TriggerType string

const (
	// TriggerFirstRecharge 表示首次充值触发，门槛须为零。
	TriggerFirstRecharge TriggerType = "first_recharge"
	// TriggerInviteReward 表示邀请有奖触发，须配置邀请人奖励券。与 GrantSourceInviteReward 不同义。
	TriggerInviteReward TriggerType = "invite_reward"
	// TriggerThresholdRedeem 表示满减门槛触发，须配置正门槛。
	TriggerThresholdRedeem TriggerType = "threshold_redeem"
	// TriggerHoliday 表示节日触发，门槛须为零。
	TriggerHoliday TriggerType = "holiday"
)

// Known 判断是否属于 coupon_activity_rule.trigger_type 的合法集合（四值）。
// 仅用于识别未知值，读取路径保持原值透传；活动配置的可写集合见 rules_coupon_activity.go。
func (t TriggerType) Known() bool {
	switch t {
	case TriggerFirstRecharge, TriggerInviteReward, TriggerThresholdRedeem, TriggerHoliday:
		return true
	default:
		return false
	}
}
