package marketing

import (
	"errors"
	"time"
)

// 活动触发类型决定金额门槛和邀请人券的必填要求。
// 触发类型的类型、常量与词表边界见 status.go 的 TriggerType。

// ErrActivityRule 表示活动参数不合法。
var ErrActivityRule = errors.New("invalid activity rule")

// MaxActivityHorizon 是活动结束时间距今的上限。
const MaxActivityHorizon = 365 * 24 * time.Hour

// MaxActivityNameLength 是活动名称的字数上限。
const MaxActivityNameLength = 128

// ActivityRule 是活动规则的待校验参数。
// TriggerType 对应 coupon_activity_rule.trigger_type，Status 对应同表 status 列；
// 与券模板的 DiscountType、CouponStatus 不同表不同义，不得互换。
type ActivityRule struct {
	Name            string         // 活动名称
	TriggerType     TriggerType    // 触发类型：coupon_activity_rule.trigger_type
	CouponID        uint64         // 活动发放的券
	InviterCouponID *uint64        // 邀请人奖励券；邀请有奖必填
	ThresholdCents  int64          // 触发门槛（分）
	MaxGrants       int            // 同券模板和发放来源共享的份数上限，0 表示不限
	PerUserLimit    int            // 单用户可领份数，0 沿用默认一份
	Status          ActivityStatus // coupon_activity_rule.status；留空时按 active 处理
	StartAt         string         // RFC3339
	EndAt           string         // RFC3339
}

// NormalizeActivity 校验活动并将时间统一为 UTC，空状态默认 active。
// 满减门槛须为正，首充和节日门槛须为零；邀请奖励须配置邀请人券，金额门槛只要求非负。
// 结束时间不得超过调用方提供的当前时间 365 天后。
// 返回的 ActivityStatus 即 coupon_activity_rule.status 的写入值：空状态归一为
// ActivityActive，其余只接受 ActivityStatus.Known() 覆盖的两值。
func NormalizeActivity(rule ActivityRule, now time.Time) (time.Time, time.Time, ActivityStatus, error) {
	var zero time.Time
	if rule.Name == "" || len([]rune(rule.Name)) > MaxActivityNameLength {
		return zero, zero, "", ErrActivityRule
	}
	switch rule.TriggerType {
	case TriggerFirstRecharge, TriggerInviteReward, TriggerThresholdRedeem, TriggerHoliday:
	default:
		return zero, zero, "", ErrActivityRule
	}
	if rule.CouponID == 0 {
		return zero, zero, "", ErrActivityRule
	}
	start, err := time.Parse(time.RFC3339, rule.StartAt)
	if err != nil {
		return zero, zero, "", ErrActivityRule
	}
	end, err := time.Parse(time.RFC3339, rule.EndAt)
	if err != nil || !start.Before(end) {
		return zero, zero, "", ErrActivityRule
	}
	if end.After(now.Add(MaxActivityHorizon)) {
		return zero, zero, "", ErrActivityRule
	}
	if rule.ThresholdCents < 0 || rule.MaxGrants < 0 || rule.PerUserLimit < 0 {
		return zero, zero, "", ErrActivityRule
	}
	switch rule.TriggerType {
	case TriggerThresholdRedeem:
		if rule.ThresholdCents <= 0 {
			return zero, zero, "", ErrActivityRule
		}
	case TriggerInviteReward:
		if rule.InviterCouponID == nil || *rule.InviterCouponID == 0 {
			return zero, zero, "", ErrActivityRule
		}
	default:
		if rule.ThresholdCents != 0 {
			return zero, zero, "", ErrActivityRule
		}
	}
	status := rule.Status
	if status == "" {
		status = ActivityActive
	}
	if !status.Known() {
		return zero, zero, "", ErrActivityRule
	}
	return start.UTC(), end.UTC(), status, nil
}

// 活动规则统一计算发券资格、单人份数和有效期。

// ActivityGrantExpiry 取发券起算 validHours 小时、券模板结束、活动结束三者最早时间。
// 未设置模板结束时间时，只比较有效小时和活动结束时间。
func ActivityGrantExpiry(now time.Time, validHours int, couponEnd *time.Time, ruleEnd time.Time) time.Time {
	expires := now.Add(time.Duration(validHours) * time.Hour)
	if couponEnd != nil && couponEnd.Before(expires) {
		expires = *couponEnd
	}
	if ruleEnd.Before(expires) {
		expires = ruleEnd
	}
	return expires
}

// PerUserAllowance 将零或负数单人份数按一份处理，不表示不限量；总量不限由总份数字段控制。
func PerUserAllowance(perUserLimit int) int {
	if perUserLimit <= 0 {
		return 1
	}
	return perUserLimit
}

// InviteEligible 仅对邀请奖励检查非空邀请人和禁止自邀，其他触发不受邀请关系限制。
// triggerType 取自 coupon_activity_rule.trigger_type；TriggerInviteReward 与
// marketingdomain.GrantSourceInviteReward 同值但不同义，此处只比较触发类型。
func InviteEligible(triggerType TriggerType, inviterID, userID uint64) bool {
	if triggerType != TriggerInviteReward {
		return true
	}
	return inviterID != 0 && inviterID != userID
}
