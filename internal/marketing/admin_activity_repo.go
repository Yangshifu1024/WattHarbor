package marketing

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对券活动规则的读写。活动触发发券在 activity_grant.go，
// 这里只管规则本身的增删改查与统计。

// AdminActivityRuleRow 是活动规则列表的一行，联表带出活动券名称。
type AdminActivityRuleRow struct {
	ID              uint64     `json:"id" gorm:"column:id"`                               // 原记录主键
	Name            string     `json:"name" gorm:"column:name"`                           // 原业务名称
	TriggerType     string     `json:"trigger_type" gorm:"column:trigger_type"`           // 原触发类型
	CouponID        uint64     `json:"coupon_id" gorm:"column:coupon_id"`                 // 原模板主键
	CouponName      string     `json:"coupon_name" gorm:"column:coupon_name"`             // 原模板名称
	InviterCouponID *uint64    `json:"inviter_coupon_id" gorm:"column:inviter_coupon_id"` // 原可选邀请人模板
	ThresholdCents  int64      `json:"threshold_cents" gorm:"column:threshold_cents"`     // 原触发门槛，分
	MaxGrants       int        `json:"max_grants" gorm:"column:max_grants"`               // 原券与来源共享上限，0不限
	PerUserLimit    int        `json:"per_user_limit" gorm:"column:per_user_limit"`       // 原单人份数，0默认1
	Status          string     `json:"status" gorm:"column:status"`                       // 原业务状态
	StartAt         time.Time  `json:"start_at" gorm:"column:start_at"`                   // 原开始时刻，可空表示立即
	EndAt           *time.Time `json:"end_at" gorm:"column:end_at"`                       // 原截止时刻，可空表示长期
	CreatedAt       time.Time  `json:"created_at" gorm:"column:created_at"`               // 原创建UTC时刻
	// GrantedCount 是活动券的累计发放份数，由 ListActivityRules 单独聚合回填。
	GrantedCount int64 `json:"granted_count" gorm:"-"` // 原已发份数
}

// AdminActivityRuleInput 是活动规则的写入参数。时间由调用方解析成 UTC 后传入。
type AdminActivityRuleInput struct {
	Name            string    // 原业务名称
	TriggerType     string    // 原触发类型
	CouponID        uint64    // 原模板主键
	InviterCouponID *uint64   // 原可选邀请人模板
	ThresholdCents  int64     // 原触发门槛，分
	MaxGrants       int       // 原券与来源共享上限，0不限
	PerUserLimit    int       // 原单人份数，0默认1
	Status          string    // 原业务状态
	StartAt         time.Time // 原开始时刻，可空表示立即
	EndAt           time.Time // 原截止时刻，可空表示长期
}

// AdminActivityRepo 是后台活动规则的读写入口。
type AdminActivityRepo struct {
	DB *gorm.DB // 所属中央连接或调用方事务，由进程管理
}

const activityRuleColumns = "r.id, r.name, r.trigger_type, r.coupon_id, c.name AS coupon_name, " +
	"r.inviter_coupon_id, r.threshold_cents, r.max_grants, r.per_user_limit, r.status, " +
	"r.start_at, r.end_at, r.created_at"

// ListActivityRules 分页返回活动规则，支持按状态与名称关键词过滤，
// 并把每条活动券的实际发放份数补进 GrantedCount。
func (s AdminActivityRepo) ListActivityRules(ctx context.Context, status, keyword string, page, pageSize int) ([]AdminActivityRuleRow, int64, error) {
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
	counts, err := activityGrantedCounts(s.DB.WithContext(ctx), couponIDs(rows))
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		rows[i].GrantedCount = counts[rows[i].CouponID]
	}
	return rows, total, nil
}

// couponIDs 从 rows 提取原模板主键，供批量聚合，不执行IO。
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
		CouponID uint64 `gorm:"column:coupon_id"` // 原模板主键
		Total    int64  `gorm:"column:total"`     // 原聚合份数
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

// CreateActivityRule 新建活动规则并回填主键。
func (s AdminActivityRepo) CreateActivityRule(ctx context.Context, in AdminActivityRuleInput) (uint64, error) {
	var id uint64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireActivityCoupons(tx, in); err != nil {
			return err
		}
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
func (s AdminActivityRepo) UpdateActivityRule(ctx context.Context, id uint64, in AdminActivityRuleInput) (AdminActivityRuleRow, error) {
	var before AdminActivityRuleRow
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID              uint64     `gorm:"column:id"`                // 原记录主键
			Name            string     `gorm:"column:name"`              // 原业务名称
			TriggerType     string     `gorm:"column:trigger_type"`      // 原触发类型
			CouponID        uint64     `gorm:"column:coupon_id"`         // 原模板主键
			InviterCouponID *uint64    `gorm:"column:inviter_coupon_id"` // 原可选邀请人模板
			ThresholdCents  int64      `gorm:"column:threshold_cents"`   // 原触发门槛，分
			MaxGrants       int        `gorm:"column:max_grants"`        // 原券与来源共享上限，0不限
			PerUserLimit    int        `gorm:"column:per_user_limit"`    // 原单人份数，0默认1
			Status          string     `gorm:"column:status"`            // 原业务状态
			StartAt         time.Time  `gorm:"column:start_at"`          // 原开始时刻，可空表示立即
			EndAt           *time.Time `gorm:"column:end_at"`            // 原截止时刻，可空表示长期
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
		if row.TriggerType != in.TriggerType {
			return &InputError{"活动触发类型不可修改"}
		}
		if err := requireActivityCoupons(tx, in); err != nil {
			return err
		}
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

// requireActivityCoupons 在调用方tx内按in原奖励身份当前读取并锁定模板，返回准入／存储错误。
// 更新调用者须先锁规则；模板按主键升序取得SHARE锁，校验与规则写入共用事务，不修改券。
func requireActivityCoupons(tx *gorm.DB, in AdminActivityRuleInput) error {
	ids := []uint64{in.CouponID}
	if in.InviterCouponID != nil && *in.InviterCouponID != 0 && *in.InviterCouponID != in.CouponID {
		ids = append(ids, *in.InviterCouponID)
	}
	var rows []struct {
		ID     uint64 `gorm:"column:id"`     // 原模板主键
		Status string `gorm:"column:status"` // 原启停状态
	}
	if err := tx.Table("coupon").Select("id,status").Clauses(clause.Locking{Strength: "SHARE"}).Where("id IN ? AND deleted_at IS NULL", ids).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	usable := make(map[uint64]bool, len(rows))
	for _, row := range rows {
		usable[row.ID] = row.Status == "active"
	}
	if !usable[in.CouponID] {
		return &InputError{"活动券不存在或已停用"}
	}
	if in.InviterCouponID != nil && *in.InviterCouponID != 0 && !usable[*in.InviterCouponID] {
		return &InputError{"邀请人券不存在或已停用"}
	}
	return nil
}
