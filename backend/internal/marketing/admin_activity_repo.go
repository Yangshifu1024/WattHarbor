package marketing

import (
	"context"
	"time"

	marketingdomain "chargepilot/internal/domain/marketing"
	"chargepilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台负责活动规则读写和统计，触发发券由 activity_grant.go 执行。

// AdminActivityRuleRow 是活动规则列表的一行，联表带出活动券名称。
// TriggerType 与 Status 分别对应 coupon_activity_rule 表的 trigger_type 与 status 列；
// CouponName 来自 coupon 表，不代表券模板自身的 CouponStatus。
type AdminActivityRuleRow struct {
	ID              uint64         `json:"id" gorm:"column:id"`                               // 已保存记录主键
	Name            string         `json:"name" gorm:"column:name"`                           // 业务名称
	TriggerType     TriggerType    `json:"trigger_type" gorm:"column:trigger_type"`           // 触发类型
	CouponID        uint64         `json:"coupon_id" gorm:"column:coupon_id"`                 // 模板主键
	CouponName      string         `json:"coupon_name" gorm:"column:coupon_name"`             // 模板名称
	InviterCouponID *uint64        `json:"inviter_coupon_id" gorm:"column:inviter_coupon_id"` // 可选邀请人模板
	ThresholdCents  int64          `json:"threshold_cents" gorm:"column:threshold_cents"`     // 触发门槛，分
	MaxGrants       int            `json:"max_grants" gorm:"column:max_grants"`               // 券与来源共享上限，0 表示不限量
	PerUserLimit    int            `json:"per_user_limit" gorm:"column:per_user_limit"`       // 单人份数，0 默认 1 份
	Status          ActivityStatus `json:"status" gorm:"column:status"`                       // 业务状态
	StartAt         time.Time      `json:"start_at" gorm:"column:start_at"`                   // 活动开始时刻
	EndAt           *time.Time     `json:"end_at" gorm:"column:end_at"`                       // 活动结束时刻；历史记录可空
	CreatedAt       time.Time      `json:"created_at" gorm:"column:created_at"`               // 创建 UTC 时刻
	// GrantedCount 是活动券的累计发放份数，由 ListActivityRules 单独聚合回填。
	GrantedCount int64 `json:"granted_count" gorm:"-"` // 已发份数
}

// AdminActivityRuleInput 是活动规则的写入参数。时间由调用方解析成 UTC 后传入。
// TriggerType 与 Status 对应 coupon_activity_rule 表同名列，Status 由 NormalizeActivity 归一后传入。
type AdminActivityRuleInput struct {
	Name            string         // 业务名称
	TriggerType     TriggerType    // 触发类型
	CouponID        uint64         // 模板主键
	InviterCouponID *uint64        // 可选邀请人模板
	ThresholdCents  int64          // 触发门槛，分
	MaxGrants       int            // 券与来源共享上限，0 表示不限量
	PerUserLimit    int            // 单人份数，0 默认 1 份
	Status          ActivityStatus // 业务状态
	StartAt         time.Time      // 活动开始时刻
	EndAt           time.Time      // 活动结束时刻
}

// AdminActivityRepo 是后台活动规则的读写入口。
type AdminActivityRepo struct {
	DB *gorm.DB // 所属中央连接或调用方事务，由进程管理
}

const activityRuleColumns = "r.id, r.name, r.trigger_type, r.coupon_id, c.name AS coupon_name, " +
	"r.inviter_coupon_id, r.threshold_cents, r.max_grants, r.per_user_limit, r.status, " +
	"r.start_at, r.end_at, r.created_at"

// ListActivityRules 按状态和名称分页查询规则，补充各券模板实际发放量。
// status 来自 persist.PageQuery 的共用筛选项，已在 ActivityService.List 按
// coupon_activity_rule.status 的两值白名单校验，此处原样作为 WHERE 参数。
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

// couponIDs 提取列表中的券模板主键，用于批量统计。
func couponIDs(rows []AdminActivityRuleRow) []uint64 {
	ids := make([]uint64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.CouponID)
	}
	return ids
}

// activityGrantedCounts 按券模板汇总 activity 和 invite_reward 两种来源的发放份数。
// 不同活动引用同一张券时共享该券的累计计数。
func activityGrantedCounts(db *gorm.DB, couponIDs []uint64) (map[uint64]int64, error) {
	counts := map[uint64]int64{}
	if len(couponIDs) == 0 {
		return counts, nil
	}
	// row 按券模板统计活动及邀请奖励发放量；引用同一模板的规则共享计数。
	type row struct {
		CouponID uint64 `gorm:"column:coupon_id"` // 模板主键
		Total    int64  `gorm:"column:total"`     // 聚合份数
	}
	var rows []row
	err := db.Table("coupon_grant").Select("coupon_id, COUNT(*) AS total").
		Where("coupon_id IN ? AND grant_source IN ? AND deleted_at IS NULL", couponIDs, activityGrantSources()).
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

// UpdateActivityRule 更新配置并返回旧记录供审计，trigger_type 不变；缺失返回 ErrRecordNotFound。
func (s AdminActivityRepo) UpdateActivityRule(ctx context.Context, id uint64, in AdminActivityRuleInput) (AdminActivityRuleRow, error) {
	var before AdminActivityRuleRow
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID              uint64         `gorm:"column:id"`                // 已保存记录主键
			Name            string         `gorm:"column:name"`              // 业务名称
			TriggerType     TriggerType    `gorm:"column:trigger_type"`      // 触发类型
			CouponID        uint64         `gorm:"column:coupon_id"`         // 模板主键
			InviterCouponID *uint64        `gorm:"column:inviter_coupon_id"` // 可选邀请人模板
			ThresholdCents  int64          `gorm:"column:threshold_cents"`   // 触发门槛，分
			MaxGrants       int            `gorm:"column:max_grants"`        // 券与来源共享上限，0 表示不限量
			PerUserLimit    int            `gorm:"column:per_user_limit"`    // 单人份数，0 默认 1 份
			Status          ActivityStatus `gorm:"column:status"`            // 业务状态
			StartAt         time.Time      `gorm:"column:start_at"`          // 活动开始时刻
			EndAt           *time.Time     `gorm:"column:end_at"`            // 活动结束时刻；历史记录可空
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

// requireActivityCoupons 在调用方事务内按模板主键升序取得共享锁，并校验奖励券。
// 更新须先锁活动规则，校验和规则写入同事务。
func requireActivityCoupons(tx *gorm.DB, in AdminActivityRuleInput) error {
	ids := []uint64{in.CouponID}
	if in.InviterCouponID != nil && *in.InviterCouponID != 0 && *in.InviterCouponID != in.CouponID {
		ids = append(ids, *in.InviterCouponID)
	}
	var rows []struct {
		ID     uint64       `gorm:"column:id"`     // 模板主键
		Status CouponStatus `gorm:"column:status"` // coupon.status：券模板启停状态
	}
	if err := tx.Table("coupon").Select("id,status").Clauses(clause.Locking{Strength: "SHARE"}).Where("id IN ? AND deleted_at IS NULL", ids).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	usable := make(map[uint64]bool, len(rows))
	for _, row := range rows {
		// 此处校验的是活动奖励的券模板，取值来自 coupon 表而非 coupon_activity_rule 表，
		// 因此判定用 CouponStatus（CouponActive），不用 ActivityStatus。
		usable[row.ID] = row.Status == CouponActive
	}
	if !usable[in.CouponID] {
		return &InputError{"活动券不存在或已停用"}
	}
	if in.InviterCouponID != nil && *in.InviterCouponID != 0 && !usable[*in.InviterCouponID] {
		return &InputError{"邀请人券不存在或已停用"}
	}
	return nil
}

// activityGrantSources 返回需要计入活动发放量的来源集合：仅自动发放与邀请奖励
// 计入，人工发放与注册赠送属于运营或增长口径，不参与活动规则达标判断。
func activityGrantSources() []marketingdomain.GrantSource {
	return []marketingdomain.GrantSource{marketingdomain.GrantSourceActivity, marketingdomain.GrantSourceInviteReward}
}
