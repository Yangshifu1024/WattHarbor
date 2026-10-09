package marketing

import (
	"database/sql"
	"errors"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// activityRule 保存加锁后用于发券的完整配置，不使用锁前查询结果。
type activityRule struct {
	ID              uint64        `gorm:"column:id"`                // 既定规则主键
	TriggerType     string        `gorm:"column:trigger_type"`      // 触发类型
	CouponID        uint64        `gorm:"column:coupon_id"`         // 被邀请人或普通奖励模板
	InviterCouponID sql.NullInt64 `gorm:"column:inviter_coupon_id"` // 邀请人奖励模板
	ThresholdCents  int64         `gorm:"column:threshold_cents"`   // 门槛，分
	MaxGrants       int           `gorm:"column:max_grants"`        // 券与来源共同预算，0 表示不限量
	PerUserLimit    int           `gorm:"column:per_user_limit"`    // 单人份数，0 默认 1 份
	Status          string        `gorm:"column:status"`            // 启停状态
	StartAt         time.Time     `gorm:"column:start_at"`          // 活动开始时刻
	EndAt           time.Time     `gorm:"column:end_at"`            // 活动截止时刻
}

// activityCoupon 保存按主键锁定的模板配置。
type activityCoupon struct {
	ID         uint64       `gorm:"column:id"`          // 模板主键
	Status     string       `gorm:"column:status"`      // 启停状态
	ValidHours int          `gorm:"column:valid_hours"` // 领取后的有效小时
	StartAt    sql.NullTime `gorm:"column:start_at"`    // 可选开始时刻
	EndAt      sql.NullTime `gorm:"column:end_at"`      // 可选截止时刻
}

// activeRules 先筛选触发类型和金额匹配的规则主键，再按主键锁读最新配置并复查。
// 候选查询结果不能直接用于发券；之后按模板主键升序加锁，不再锁用户或钱包。
func activeRules(tx *gorm.DB, event ActivityEvent) ([]activityRule, error) {
	query := tx.Table("coupon_activity_rule").
		Where("trigger_type=? AND status='active' AND deleted_at IS NULL", event.TriggerType).
		Where("start_at<=? AND end_at>?", event.Now, event.Now)
	if event.TriggerType == TriggerThresholdRedeem {
		query = query.Where("threshold_cents>0 AND threshold_cents<=?", event.AmountCents)
	}
	var ids []uint64
	if err := query.Order("id").Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	rows := []activityRule{}
	if len(ids) == 0 {
		return rows, nil
	}
	locked := tx.Table("coupon_activity_rule FORCE INDEX (PRIMARY)").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ? AND trigger_type=? AND status='active' AND deleted_at IS NULL", ids, event.TriggerType).
		Where("start_at<=? AND end_at>?", event.Now, event.Now)
	if event.TriggerType == TriggerThresholdRedeem {
		locked = locked.Where("threshold_cents>0 AND threshold_cents<=?", event.AmountCents)
	}
	err := locked.Order("id").Find(&rows).Error
	return rows, err
}

// lockActivityCoupons 在规则锁后按模板主键升序加写锁，共用模板的规则须串行检查预算。
// 缺失模板由发放逻辑判断为不适用。
func lockActivityCoupons(tx *gorm.DB, ids []uint64) (map[uint64]activityCoupon, error) {
	out := map[uint64]activityCoupon{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []activityCoupon
	if err := tx.Table("coupon").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ? AND deleted_at IS NULL", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

// countRuleGrants 在模板锁内用当前读统计该券和来源的已发数量，避免旧 RR 快照遗漏并发发放。
func countRuleGrants(tx *gorm.DB, rule activityRule) (int64, error) {
	var n int64
	err := tx.Table("coupon_grant").Clauses(clause.Locking{Strength: "UPDATE"}).Where("coupon_id=? AND grant_source=? AND deleted_at IS NULL", rule.CouponID, grantSourceFor(rule.TriggerType)).Count(&n).Error
	return n, err
}

// countUserGrants 在模板锁内统计用户对该券的累计发放量，各来源共同计入单人额度。
func countUserGrants(tx *gorm.DB, couponID, userID uint64) (int64, error) {
	var n int64
	err := tx.Table("coupon_grant").Clauses(clause.Locking{Strength: "UPDATE"}).Where("coupon_id=? AND user_id=? AND deleted_at IS NULL", couponID, userID).Count(&n).Error
	return n, err
}

// activityGrantExists 用当前读检查指定用户和事件的邀请奖励是否已保存。
func activityGrantExists(tx *gorm.DB, eventID string, userID uint64) (bool, error) {
	var n int64
	err := tx.Table("coupon_grant").Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_event_id=? AND user_id=? AND deleted_at IS NULL", eventID, userID).Count(&n).Error
	return n > 0, err
}

// saveActivityGrant 在事务中保存发放，按需追加请求凭据；重放返回首次结果。
func saveActivityGrant(tx *gorm.DB, in activityGrant, receipt bool) (bool, error) {
	err := tx.Table("coupon_grant").Create(map[string]any{"coupon_id": in.CouponID, "user_id": in.UserID, "grant_source": in.Source, "status": "unused", "expired_at": in.ExpiresAt, "source_event_id": in.EventID}).Error
	if isDuplicateKey(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !receipt {
		return false, nil
	}
	var id uint64
	if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
		return false, err
	}
	err = tx.Table("coupon_grant_request").Create(map[string]any{"request_id": in.EventID, "coupon_id": in.CouponID, "user_id": in.UserID, "coupon_grant_id": id}).Error
	return false, err
}

// isEstablishedUser 检查邀请人是否有已完成充电或已到账充值；只读相关表，不加锁或写入。
func isEstablishedUser(tx *gorm.DB, userID uint64) (bool, error) {
	var n int64
	if err := tx.Table("charge_order").Where("user_id=? AND status='completed' AND deleted_at IS NULL", userID).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if err := tx.Table("payment_order").Where("user_id=? AND biz_type='wallet_recharge' AND status='paid' AND deleted_at IS NULL", userID).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// isDuplicateKey 仅识别数据库唯一键冲突；其他存储错误不得当作已发放。
func isDuplicateKey(err error) bool {
	var e *mysql.MySQLError
	if errors.As(err, &e) {
		return e.Number == 1062
	}
	return errors.Is(err, gorm.ErrDuplicatedKey)
}
