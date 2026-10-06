package marketing

import (
	"database/sql"
	"errors"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// activityRule 是锁定活动后用于本次发放的完整配置，不使用锁前快照。
type activityRule struct {
	ID              uint64        `gorm:"column:id"`                // 原规则主键
	TriggerType     string        `gorm:"column:trigger_type"`      // 原触发类型
	CouponID        uint64        `gorm:"column:coupon_id"`         // 原被邀请人或普通奖励模板
	InviterCouponID sql.NullInt64 `gorm:"column:inviter_coupon_id"` // 原邀请人奖励模板
	ThresholdCents  int64         `gorm:"column:threshold_cents"`   // 原门槛，分
	MaxGrants       int           `gorm:"column:max_grants"`        // 原券与来源共同预算，0不限
	PerUserLimit    int           `gorm:"column:per_user_limit"`    // 原单人份数，0默认1
	Status          string        `gorm:"column:status"`            // 原启停状态
	StartAt         time.Time     `gorm:"column:start_at"`          // 原活动开始时刻
	EndAt           time.Time     `gorm:"column:end_at"`            // 原活动截止时刻
}

// activityCoupon 是同事务按主键顺序锁定的模板事实。
type activityCoupon struct {
	ID         uint64       `gorm:"column:id"`          // 原模板主键
	Status     string       `gorm:"column:status"`      // 原启停状态
	ValidHours int          `gorm:"column:valid_hours"` // 领取后的有效小时
	StartAt    sql.NullTime `gorm:"column:start_at"`    // 原可选开始时刻
	EndAt      sql.NullTime `gorm:"column:end_at"`      // 原可选截止时刻
}

// activeRules 在 tx 按 event 原触发和金额选出候选ID，再用PRIMARY按主键当前读、复核并锁定完整配置。
// 候选不作为发放配置；返回锁后事实或存储错误，避免优化器用全表锁扫描无关触发。
// 之后只按模板主键升序加锁，不反向锁用户／钱包。
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

// lockActivityCoupons 在 tx 持有全部规则锁后按 ids 主键升序取得模板写锁，共用模板的不同规则也串行仲裁预算。
// 返回未删除模板事实，缺失由业务判断不适用；存储错误原样返回，不发券。
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

// countRuleGrants 在模板锁下当前读取 rule 原券／来源已发份数，避免RR快照遗漏并发提交；无写入。
func countRuleGrants(tx *gorm.DB, rule activityRule) (int64, error) {
	var n int64
	err := tx.Table("coupon_grant").Clauses(clause.Locking{Strength: "UPDATE"}).Where("coupon_id=? AND grant_source=? AND deleted_at IS NULL", rule.CouponID, grantSourceFor(rule.TriggerType)).Count(&n).Error
	return n, err
}

// countUserGrants 在模板锁下当前读取 userID 对 couponID 的未删除累计份数，沿用原跨来源单人额度口径。
func countUserGrants(tx *gorm.DB, couponID, userID uint64) (int64, error) {
	var n int64
	err := tx.Table("coupon_grant").Clauses(clause.Locking{Strength: "UPDATE"}).Where("coupon_id=? AND user_id=? AND deleted_at IS NULL", couponID, userID).Count(&n).Error
	return n, err
}

// activityGrantExists 在 tx 下当前读取 eventID／userID 原邀请奖励存在事实，返回事实或存储错误，无额外发放。
func activityGrantExists(tx *gorm.DB, eventID string, userID uint64) (bool, error) {
	var n int64
	err := tx.Table("coupon_grant").Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_event_id=? AND user_id=? AND deleted_at IS NULL", eventID, userID).Count(&n).Error
	return n > 0, err
}

// saveActivityGrant 在 tx 中写 in 原权益并按 receipt 决定是否追加请求凭据；返回重放事实或存储错误，不自行提交。
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

// isEstablishedUser 在 tx 下只读邀请人的原已完成订单／已到账充值存在事实；不锁或写外部用例表。
// 此资格投影不产生资金、订单编号或营销反向依赖；存储失败原样返回。
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
