package marketing

import (
	"errors"
	"time"
)

// 券活动的参数校验。触发类型决定了门槛字段与邀请人券是否必填，
// 这组约束是业务规则：写错一条，活动就会按错误的条件发券。

// ErrActivityRule 表示活动参数不合法。
var ErrActivityRule = errors.New("invalid activity rule")

// 活动支持的触发类型。
const (
	TriggerFirstRecharge   = "first_recharge"   // 首充
	TriggerInviteReward    = "invite_reward"    // 邀请有奖
	TriggerThresholdRedeem = "threshold_redeem" // 满减
	TriggerHoliday         = "holiday"          // 节日
)

// MaxActivityHorizon 是活动结束时间距今的上限。
const MaxActivityHorizon = 365 * 24 * time.Hour

// MaxActivityNameLength 是活动名称的字数上限。
const MaxActivityNameLength = 128

// ActivityRule 是活动规则的待校验参数。
type ActivityRule struct {
	Name            string  // 活动名称
	TriggerType     string  // 触发类型
	CouponID        uint64  // 活动发放的券
	InviterCouponID *uint64 // 邀请人奖励券；邀请有奖必填
	ThresholdCents  int64   // 触发门槛（分）
	MaxGrants       int     // 活动总发放上限，0 表示不限
	PerUserLimit    int     // 单用户可领份数，0 表示不限
	Status          string  // active / disabled；留空时按 active 处理
	StartAt         string  // RFC3339
	EndAt           string  // RFC3339
}

// NormalizeActivity 校验活动参数并补上缺省值，返回 UTC 化的起止时间。
//
// 三条按触发类型分化的约束最容易被改错：
//   - 满减必须有门槛（> 0）
//   - 邀请有奖必须配置邀请人券
//   - 其余触发类型一律不接受门槛（必须为 0）
//   - 状态留空按 active 处理
//
// now 由调用方传入，使"结束时间不超过一年后"这条判断可测且不受宿主时钟影响。
func NormalizeActivity(rule ActivityRule, now time.Time) (time.Time, time.Time, string, error) {
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
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return zero, zero, "", ErrActivityRule
	}
	return start.UTC(), end.UTC(), status, nil
}

// 活动发券的资格与到期计算。发放路径（coupon_activity_store.go）里这三项
// 原本直接写在事务中间，夹在锁行、预算计数和插入之间，改错了不会有编译错误，
// 也不会有测试失败——只会在活动结束后发出一张还能核销的券。

// ActivityGrantExpiry 计算活动发券的到期时间，取三个时刻的较早者：
// 券有效期（自发放时刻起算 validHours 小时）、券自身的结束时间、活动的结束时间。
//
// 活动的结束时间是硬上限：越过它发出去的券，用户在活动收尾后仍能核销，
// 预算与核销台账会对不上。券读不到时调用方直接回落到活动结束时间。
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

// PerUserAllowance 把活动规则的单人份数归一化。0 或负数按 1 份处理：
// 留空表示"未配置"，不是"不限量"——不限量由总发放上限承担，
// 单人额度若被当成不限量，一次活动就能把预算全发给同一个人。
func PerUserAllowance(perUserLimit int) int {
	if perUserLimit <= 0 {
		return 1
	}
	return perUserLimit
}

// InviteEligible 判断邀请有奖是否具备发放前提。禁止自邀，邀请人也不能为空：
// 空账号互邀不产生任何真实用户，却会成对消耗双方的活动预算。
// 其余触发类型不受邀请关系影响。
func InviteEligible(triggerType string, inviterID, userID uint64) bool {
	if triggerType != TriggerInviteReward {
		return true
	}
	return inviterID != 0 && inviterID != userID
}
