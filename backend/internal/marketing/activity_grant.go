package marketing

import (
	marketingdomain "chargepilot/internal/domain/marketing"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"time"
)

// ErrActivityNotApplicable 表示事件或规则不满足发券条件；数据库故障仍返回错误，调用方须整体回滚。
var ErrActivityNotApplicable = errors.New("no activity rule applies")

// ActivityEvent 保存触发类型、用户、金额和事件键，重试时保持不变。
type ActivityEvent struct {
	TriggerType string    // 首充／邀请／门槛／节日触发类型
	UserID      uint64    // 受益用户
	EventKey    string    // 充值请求号或订单号生成的稳定事件键
	AmountCents int64     // 已确认金额，分；仅门槛规则读取
	InviterID   uint64    // 邀请人主键，非邀请触发不读取
	Now         time.Time // 调用方提供的本次判断时间
}

// ActivityResult 是规则和券模板的发放结果，重放不会新增券。
type ActivityResult struct {
	RuleID     uint64 `json:"rule_id"`          // 本次匹配的活动规则 ID
	CouponID   uint64 `json:"coupon_id"`        // 奖励模板
	InviterGot bool   `json:"inviter_rewarded"` // 本次已给邀请人发券，或此前已发过同一事件奖励
	Already    bool   `json:"already_granted"`  // 受益人发放键已存在
}

// activityGrant 是待保存的发放内容，不含数据库字段映射。
type activityGrant struct {
	CouponID  uint64                      // 奖励模板
	UserID    uint64                      // 受益用户
	Source    marketingdomain.GrantSource // 发放来源，落库为 coupon_grant.grant_source
	ExpiresAt time.Time                   // 自发放起算的有效期，最晚不超过模板和活动的结束时间
	EventID   string                      // 由规则、用户和事件键生成的稳定 32 字符发放号
}

// ApplyInTx 按规则主键→模板主键→发放行加锁，再检查总量和单人额度并发券。
// 使用调用方完整事务，失败整体回滚；模板锁之后不能再锁用户或钱包。
func (s ActivityService) ApplyInTx(tx *gorm.DB, event ActivityEvent) ([]ActivityResult, error) {
	if event.UserID == 0 || event.EventKey == "" || event.Now.IsZero() {
		return nil, ErrActivityNotApplicable
	}
	if tx == nil {
		return nil, errors.New("marketing activity transaction is required")
	}
	rules, err := activeRules(tx, event)
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(rules)*2)
	for _, rule := range rules {
		ids = append(ids, rule.CouponID)
		if rule.InviterCouponID.Valid && rule.InviterCouponID.Int64 > 0 {
			ids = append(ids, uint64(rule.InviterCouponID.Int64))
		}
	}
	coupons, err := lockActivityCoupons(tx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ActivityResult, 0, len(rules))
	for _, rule := range rules {
		result, err := grantFromRule(tx, rule, event, coupons)
		if errors.Is(err, ErrActivityNotApplicable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return out, nil
}

// usableActivityCoupon 检查模板状态和有效时间，缺失模板视为不可用。
func usableActivityCoupon(coupon activityCoupon, now time.Time) bool {
	return coupon.ID != 0 && coupon.Status == "active" && (!coupon.StartAt.Valid || !now.Before(coupon.StartAt.Time)) && (!coupon.EndAt.Valid || now.Before(coupon.EndAt.Time))
}

// activityExpiry 取发放后有效小时、模板结束和活动结束三个时间中的最早值。
func activityExpiry(rule activityRule, coupon activityCoupon, now time.Time) time.Time {
	var end *time.Time
	if coupon.EndAt.Valid {
		end = &coupon.EndAt.Time
	}
	return ActivityGrantExpiry(now, coupon.ValidHours, end, rule.EndAt)
}

// grantFromRule 在规则和模板锁内检查资格并发券，份数达到上限时跳过，重试不重复发券。
// 邀请人未完成过充电或到账充值时整条规则不发券；邀请人的券不可用或份数用尽时只跳过邀请人奖励。
func grantFromRule(tx *gorm.DB, rule activityRule, event ActivityEvent, coupons map[uint64]activityCoupon) (ActivityResult, error) {
	out := ActivityResult{RuleID: rule.ID, CouponID: rule.CouponID}
	eventID := activityEventID(rule.ID, event)
	receipt, err := findGrantReceipt(tx, eventID, true)
	if err == nil {
		if receipt.UserID != event.UserID {
			return out, ErrCouponGrant
		}
		out.CouponID = receipt.CouponID
		out.Already = true
		if rule.TriggerType == TriggerInviteReward && event.InviterID != 0 {
			out.InviterGot, err = activityGrantExists(tx, activityEventID(rule.ID, ActivityEvent{UserID: event.InviterID, EventKey: eventID}, "inviter"), event.InviterID)
		}
		return out, err
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return out, err
	}
	if !usableActivityCoupon(coupons[rule.CouponID], event.Now) {
		return out, ErrActivityNotApplicable
	}
	if rule.TriggerType == TriggerInviteReward {
		if !InviteEligible(rule.TriggerType, event.InviterID, event.UserID) {
			return out, ErrActivityNotApplicable
		}
		ok, err := isEstablishedUser(tx, event.InviterID)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, ErrActivityNotApplicable
		}
	}
	if rule.MaxGrants > 0 {
		n, err := countRuleGrants(tx, rule)
		if err != nil {
			return out, err
		}
		if n >= int64(rule.MaxGrants) {
			return out, ErrActivityNotApplicable
		}
	}
	n, err := countUserGrants(tx, rule.CouponID, event.UserID)
	if err != nil {
		return out, err
	}
	if n >= int64(PerUserAllowance(rule.PerUserLimit)) {
		return out, ErrActivityNotApplicable
	}
	out.Already, err = saveActivityGrant(tx, activityGrant{CouponID: rule.CouponID, UserID: event.UserID, Source: grantSourceFor(rule.TriggerType), ExpiresAt: activityExpiry(rule, coupons[rule.CouponID], event.Now), EventID: eventID}, true)
	if err != nil || out.Already {
		return out, err
	}
	if rule.TriggerType == TriggerInviteReward && rule.InviterCouponID.Valid && rule.InviterCouponID.Int64 > 0 {
		id := uint64(rule.InviterCouponID.Int64)
		coupon := coupons[id]
		if !usableActivityCoupon(coupon, event.Now) {
			return out, nil
		}
		n, err := countUserGrants(tx, id, event.InviterID)
		if err != nil {
			return out, err
		}
		if n >= int64(PerUserAllowance(rule.PerUserLimit)) {
			return out, nil
		}
		inviterEvent := ActivityEvent{UserID: event.InviterID, EventKey: eventID, Now: event.Now}
		_, err = saveActivityGrant(tx, activityGrant{CouponID: id, UserID: event.InviterID, Source: marketingdomain.GrantSourceInviteReward, ExpiresAt: activityExpiry(rule, coupon, event.Now), EventID: activityEventID(rule.ID, inviterEvent, "inviter")}, false)
		if err != nil {
			return out, err
		}
		out.InviterGot = true
	}
	return out, nil
}

// grantSourceFor 将邀请奖励归为 invite_reward，其余活动归为 activity。
func grantSourceFor(trigger string) marketingdomain.GrantSource {
	if trigger == TriggerInviteReward {
		return marketingdomain.GrantSourceInviteReward
	}
	return marketingdomain.GrantSourceActivity
}

// activityEventID 根据规则、用户、事件键和奖励方生成稳定的 32 字符发放号，不使用重试时间。
func activityEventID(ruleID uint64, event ActivityEvent, scope ...string) string {
	var key strings.Builder
	key.WriteString(fmt.Sprint(ruleID))
	for _, v := range scope {
		key.WriteString("\x00" + v)
	}
	sum := sha256.Sum256([]byte(key.String() + "\x00" + fmt.Sprint(event.UserID) + "\x00" + event.EventKey))
	return hex.EncodeToString(sum[:16])
}

// ActivityEventKeyForRecharge 为充值请求号生成稳定事件键。
func ActivityEventKeyForRecharge(requestID string) string { return "recharge:" + requestID }

// ActivityEventKeyForOrder 为充电订单号生成稳定事件键。
func ActivityEventKeyForOrder(orderNo string) string { return "order:" + orderNo }
