package database

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对券活动规则的读写。活动触发发券在 coupon_activity.go，
// 这里只管规则本身的增删改查与统计。

// AdminActivityRuleRow 是活动规则列表的一行，联表带出活动券名称。
type AdminActivityRuleRow struct {
	ID              uint64     `json:"id" gorm:"column:id"`
	Name            string     `json:"name" gorm:"column:name"`
	TriggerType     string     `json:"trigger_type" gorm:"column:trigger_type"`
	CouponID        uint64     `json:"coupon_id" gorm:"column:coupon_id"`
	CouponName      string     `json:"coupon_name" gorm:"column:coupon_name"`
	InviterCouponID *uint64    `json:"inviter_coupon_id" gorm:"column:inviter_coupon_id"`
	ThresholdCents  int64      `json:"threshold_cents" gorm:"column:threshold_cents"`
	MaxGrants       int        `json:"max_grants" gorm:"column:max_grants"`
	PerUserLimit    int        `json:"per_user_limit" gorm:"column:per_user_limit"`
	Status          string     `json:"status" gorm:"column:status"`
	StartAt         time.Time  `json:"start_at" gorm:"column:start_at"`
	EndAt           *time.Time `json:"end_at" gorm:"column:end_at"`
	CreatedAt       time.Time  `json:"created_at" gorm:"column:created_at"`
	// GrantedCount 是活动券的累计发放份数，由 ListActivityRules 单独聚合回填。
	GrantedCount int64 `json:"granted_count" gorm:"-"`
}

// AdminActivityRuleInput 是活动规则的写入参数。时间由调用方解析成 UTC 后传入。
type AdminActivityRuleInput struct {
	Name            string
	TriggerType     string
	CouponID        uint64
	InviterCouponID *uint64
	ThresholdCents  int64
	MaxGrants       int
	PerUserLimit    int
	Status          string
	StartAt         time.Time
	EndAt           time.Time
}

// AdminActivityStore 是后台活动规则的读写入口。
type AdminActivityStore struct{ DB *gorm.DB }

const activityRuleColumns = "r.id, r.name, r.trigger_type, r.coupon_id, c.name AS coupon_name, " +
	"r.inviter_coupon_id, r.threshold_cents, r.max_grants, r.per_user_limit, r.status, " +
	"r.start_at, r.end_at, r.created_at"

// ListActivityRules 分页返回活动规则，支持按状态与名称关键词过滤，
// 并把每条活动券的实际发放份数补进 GrantedCount。
func (s AdminActivityStore) ListActivityRules(ctx context.Context, status, keyword string, page, pageSize int) ([]AdminActivityRuleRow, int64, error) {
	query := s.DB.WithContext(ctx).Table("coupon_activity_rule AS r").
		Joins("LEFT JOIN coupon AS c ON c.id = r.coupon_id").
		Where("r.deleted_at IS NULL")
	if status != "" {
		query = query.Where("r.status = ?", status)
	}
	if keyword != "" {
		query = query.Where("r.name LIKE ? ESCAPE '!'", dbutil.LikePattern(keyword))
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []AdminActivityRuleRow
	if err := query.Session(&gorm.Session{}).Select(activityRuleColumns).
		Order("r.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	counts, err := activityGrantedCounts(s.DB, couponIDs(rows))
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		rows[i].GrantedCount = counts[rows[i].CouponID]
	}
	return rows, total, nil
}

func couponIDs(rows []AdminActivityRuleRow) []uint64 {
	ids := make([]uint64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.CouponID)
	}
	return ids
}

// activityGrantedCounts 按券 ID 聚合活动类发放数量。
// 不同活动引用同一张券时共享该券的累计计数。
func activityGrantedCounts(db *gorm.DB, couponIDs []uint64) (map[uint64]int64, error) {
	counts := map[uint64]int64{}
	if len(couponIDs) == 0 {
		return counts, nil
	}
	type row struct {
		CouponID uint64 `gorm:"column:coupon_id"`
		Total    int64  `gorm:"column:total"`
	}
	var rows []row
	err := db.Table("coupon_grant").Select("coupon_id, COUNT(*) AS total").
		Where("coupon_id IN ? AND grant_source IN ('activity','invite_reward') AND deleted_at IS NULL", couponIDs).
		Group("coupon_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		counts[r.CouponID] = r.Total
	}
	return counts, nil
}

// CouponUsable 判断券是否可被活动引用：存在、状态 active 且未软删除。
// Count 不把零行当错误，因此必须显式判零。
func CouponUsable(ctx context.Context, db *gorm.DB, couponID uint64) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Table("coupon").
		Where("id = ? AND status = 'active' AND deleted_at IS NULL", couponID).Count(&count).Error
	return count > 0, err
}

// CreateActivityRule 新建活动规则并回填主键。
func (s AdminActivityStore) CreateActivityRule(ctx context.Context, in AdminActivityRuleInput) (uint64, error) {
	var id uint64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("coupon_activity_rule").Create(map[string]any{
			"name": in.Name, "trigger_type": in.TriggerType,
			"coupon_id": in.CouponID, "inviter_coupon_id": in.InviterCouponID,
			"threshold_cents": in.ThresholdCents, "max_grants": in.MaxGrants,
			"per_user_limit": in.PerUserLimit, "status": in.Status,
			"start_at": in.StartAt, "end_at": in.EndAt,
		}).Error; err != nil {
			return err
		}
		return tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error
	})
	return id, err
}

// UpdateActivityRule 更新活动配置但保持 trigger_type 不变——改了触发语义，
// 历史发放就解释不清了。返回更新前的一行供审计留痕；规则不存在时返回 ErrRecordNotFound。
func (s AdminActivityStore) UpdateActivityRule(ctx context.Context, id uint64, in AdminActivityRuleInput) (AdminActivityRuleRow, error) {
	var before AdminActivityRuleRow
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID              uint64     `gorm:"column:id"`
			Name            string     `gorm:"column:name"`
			TriggerType     string     `gorm:"column:trigger_type"`
			CouponID        uint64     `gorm:"column:coupon_id"`
			InviterCouponID *uint64    `gorm:"column:inviter_coupon_id"`
			ThresholdCents  int64      `gorm:"column:threshold_cents"`
			MaxGrants       int        `gorm:"column:max_grants"`
			PerUserLimit    int        `gorm:"column:per_user_limit"`
			Status          string     `gorm:"column:status"`
			StartAt         time.Time  `gorm:"column:start_at"`
			EndAt           *time.Time `gorm:"column:end_at"`
		}
		// 取具体类型而非接口：GORM 反射 nil 接口会 panic。
		if err := tx.Table("coupon_activity_rule").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return err
		}
		before = AdminActivityRuleRow{ID: row.ID, Name: row.Name, TriggerType: row.TriggerType,
			CouponID: row.CouponID, InviterCouponID: row.InviterCouponID,
			ThresholdCents: row.ThresholdCents, MaxGrants: row.MaxGrants,
			PerUserLimit: row.PerUserLimit, Status: row.Status,
			StartAt: row.StartAt, EndAt: row.EndAt}
		changed := tx.Table("coupon_activity_rule").Where("id = ? AND deleted_at IS NULL", id).
			Updates(map[string]any{
				"name": in.Name, "coupon_id": in.CouponID, "inviter_coupon_id": in.InviterCouponID,
				"threshold_cents": in.ThresholdCents, "max_grants": in.MaxGrants,
				"per_user_limit": in.PerUserLimit, "status": in.Status,
				"start_at": in.StartAt, "end_at": in.EndAt,
			})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	return before, err
}
