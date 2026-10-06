package marketing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"time"
)

// ErrActivityNotApplicable 表示输入或规则资格不成立；存储失败使用原错误，由调用方整体回滚。
var ErrActivityNotApplicable = errors.New("no activity rule applies")

// ActivityEvent 描述调用方冻结的原触发事实，重放不换事件键、金额或用户。
type ActivityEvent struct {
	TriggerType string    // 原首充／邀请／门槛／节日触发类型
	UserID      uint64    // 原受益用户
	EventKey    string    // 原充值请求号或订单号生成的稳定事件键
	AmountCents int64     // 原确认金额，分；仅门槛规则读取
	InviterID   uint64    // 原邀请人主键，非邀请触发不读取
	Now         time.Time // 本次受控UTC求值时刻
}

// ActivityResult 是原规则与模板的受理结果，重放不新增发放。
type ActivityResult struct {
	RuleID     uint64 `json:"rule_id"`          // 原规则主键
	CouponID   uint64 `json:"coupon_id"`        // 原奖励模板
	InviterGot bool   `json:"inviter_rewarded"` // 邀请人奖励已受理或同键重放
	Already    bool   `json:"already_granted"`  // 原受益人发放键已存在
}

// activityGrant 是向持久化交付的冻结发放事实，不携带列映射。
type activityGrant struct {
	CouponID  uint64    // 原奖励模板
	UserID    uint64    // 原受益用户
	Source    string    // 原活动或邀请来源
	ExpiresAt time.Time // 模板与活动窗口封顶后的UTC时刻
	EventID   string    // 原规则／用户／事件键确定的32字符凭据
}

// ApplyInTx 在 tx 原context下求值 event，锁定本次规则与全部模板后按原来源预算和单人份数发放。
// 返回原结果数组或不适用／存储错误；保持调用方完整事务，失败必须回滚，不自行提交或重试。
// 锁序为规则主键、模板主键、发放行；不在模板锁之后取得用户或钱包锁。
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

// usableActivityCoupon 以 coupon 原时窗与 event.Now 判定模板当前适用，无IO；缺失零值不适用。
func usableActivityCoupon(coupon activityCoupon, now time.Time) bool {
	return coupon.ID != 0 && coupon.Status == "active" && (!coupon.StartAt.Valid || !now.Before(coupon.StartAt.Time)) && (!coupon.EndAt.Valid || now.Before(coupon.EndAt.Time))
}

// activityExpiry 对已锁定 rule／coupon 以 now 求值原较早截止；无额外SQL或错误回退。
func activityExpiry(rule activityRule, coupon activityCoupon, now time.Time) time.Time {
	var end *time.Time
	if coupon.EndAt.Valid {
		end = &coupon.EndAt.Time
	}
	return ActivityGrantExpiry(now, coupon.ValidHours, end, rule.EndAt)
}

// grantFromRule 在 tx 已持规则与模板锁下依原 event 和 coupons 准入及发放，返回原资格／存储错误。
// 预算耗尽沿用跳过结果；唯一键重放不再发放；邀请人无资格时跳过整条邀请规则。
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
		_, err = saveActivityGrant(tx, activityGrant{CouponID: id, UserID: event.InviterID, Source: "invite_reward", ExpiresAt: activityExpiry(rule, coupon, event.Now), EventID: activityEventID(rule.ID, inviterEvent, "inviter")}, false)
		if err != nil {
			return out, err
		}
		out.InviterGot = true
	}
	return out, nil
}

// grantSourceFor 保留 trigger 的既有activity／invite_reward来源，无IO。
func grantSourceFor(trigger string) string {
	if trigger == TriggerInviteReward {
		return "invite_reward"
	}
	return "activity"
}

// activityEventID 按 ruleID、event 原用户和事件键及 scope 构造确定32字符号；无IO，不包含当前重试时刻。
func activityEventID(ruleID uint64, event ActivityEvent, scope ...string) string {
	var key strings.Builder
	key.WriteString(fmt.Sprint(ruleID))
	for _, v := range scope {
		key.WriteString("\x00" + v)
	}
	sum := sha256.Sum256([]byte(key.String() + "\x00" + fmt.Sprint(event.UserID) + "\x00" + event.EventKey))
	return hex.EncodeToString(sum[:16])
}

// ActivityEventKeyForRecharge 由 requestID 原充值号构造稳定事件键，无IO。
func ActivityEventKeyForRecharge(requestID string) string { return "recharge:" + requestID }

// ActivityEventKeyForOrder 由 orderNo 原业务号构造稳定事件键，无IO。
func ActivityEventKeyForOrder(orderNo string) string { return "order:" + orderNo }
