package marketing

import (
	"context"
	"time"
	marketingdomain "wattharbor/internal/domain/marketing"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台管理券模板和人工发放；用户持券与核销在 coupon_repo.go、coupon_redeem_repo.go，活动发放在 activity_grant.go。

// AdminCouponRow 是券模板记录：amount 用分值，percentage 用折扣比例，time_free 用免费分钟数，三者互斥。
// DiscountType 与 Status 对应 coupon 表的 discount_type 与 status 列。
type AdminCouponRow struct {
	ID                 uint64           `json:"id" gorm:"column:id;primaryKey"`                          // 券模板主键；创建时须为 0 由库自增
	Name               string           `json:"name" gorm:"column:name"`                                 // 券名称，非空且不超过 128 字符
	DiscountType       DiscountType     `json:"discount_type" gorm:"column:discount_type"`               // 优惠类型
	DiscountValueCents *int64           `json:"discount_value_cents" gorm:"column:discount_value_cents"` // 减免分值，可空
	DiscountPercent    *decimal.Decimal `json:"discount_percent" gorm:"column:discount_percent"`         // 应付比例百分数，80 表示八折；可空
	FreeMinutes        *uint32          `json:"free_minutes" gorm:"column:free_minutes"`                 // 免费分钟，可空
	MinChargeCents     int64            `json:"min_charge_cents" gorm:"column:min_charge_cents"`         // 使用门槛，分
	ValidHours         uint32           `json:"valid_hours" gorm:"column:valid_hours"`                   // 领取后有效小时
	TotalQuota         uint32           `json:"total_quota" gorm:"column:total_quota"`                   // 总量上限，0 表示不限量
	PerUserQuota       uint32           `json:"per_user_quota" gorm:"column:per_user_quota"`             // 人工发放单人上限
	Status             CouponStatus     `json:"status" gorm:"column:status"`                             // 业务状态
	StartAt            *time.Time       `json:"start_at" gorm:"column:start_at"`                         // 开始时刻，可空表示立即
	EndAt              *time.Time       `json:"end_at" gorm:"column:end_at"`                             // 截止时刻，可空表示长期
}

// TableName 指定优惠券模板表。
func (AdminCouponRow) TableName() string { return "coupon" }

// AdminCouponStats 汇总已发、未用、已用和过期份数；未用券按当前时间区分是否到期。
type AdminCouponStats struct {
	TotalQuota   uint32  `json:"total_quota"`   // 总量上限，0 表示不限量
	GrantedCount int64   `json:"granted_count"` // 已发份数
	UnusedCount  int64   `json:"unused_count"`  // 未使用未过期份数
	UsedCount    int64   `json:"used_count"`    // 已使用份数
	ExpiredCount int64   `json:"expired_count"` // 到期份数
	UsageRate    float64 `json:"usage_rate"`    // 已使用 / 已发放；未发放时为 0
}

// AdminCouponRepo 是后台券模板的读写入口。
type AdminCouponRepo struct {
	DB *gorm.DB // 所属中央连接或调用方事务，由进程管理
}

// ListCoupons 倒序读取全部未删除券模板。
func (s AdminCouponRepo) ListCoupons(ctx context.Context) ([]AdminCouponRow, error) {
	rows := []AdminCouponRow{}
	err := s.DB.WithContext(ctx).Table("coupon").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error
	return rows, err
}

// CreateCoupon 保存调用方设置好状态的模板，并回填自增主键供响应和审计使用。
func (s AdminCouponRepo) CreateCoupon(ctx context.Context, in *AdminCouponRow) error {
	return s.DB.WithContext(ctx).Create(in).Error
}

// UpdateCoupon 锁定模板后只更新名称和启停状态，返回旧值供审计；额度和优惠不能修改。
// status 已由 CouponService.Update 按 coupon.status 的两值白名单校验，原样写入 coupon.status。
func (s AdminCouponRepo) UpdateCoupon(ctx context.Context, id uint64, name string, status CouponStatus) (AdminCouponRow, error) {
	var before AdminCouponRow
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("coupon").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		return tx.Table("coupon").Where("id=?", id).Updates(map[string]any{"name": name, "status": status}).Error
	})
	return before, err
}

// Stats 统计该模板的发放情况与使用率。
func (s AdminCouponRepo) Stats(ctx context.Context, id uint64) (AdminCouponStats, error) {
	out := AdminCouponStats{}
	db := s.DB.WithContext(ctx)
	var coupon AdminCouponRow
	if err := db.Table("coupon").Where("id=? AND deleted_at IS NULL", id).Take(&coupon).Error; err != nil {
		return out, err
	}
	out.TotalQuota = coupon.TotalQuota
	var counts struct {
		GrantedCount int64 `gorm:"column:granted_count"` // 已发份数
		UnusedCount  int64 `gorm:"column:unused_count"`  // 未使用未过期份数
		UsedCount    int64 `gorm:"column:used_count"`    // 已使用份数
		ExpiredCount int64 `gorm:"column:expired_count"` // 到期份数
	}
	// unused 但已过期的记录计入过期数量。
	err := db.Table("coupon_grant").
		// 以下 SUM 条件内的 status 字面量是 coupon_grant.status 的 GrantStatus 词表
		//（unused／used／expired，internal/domain/marketing/grant.go），
		// 嵌在聚合表达式里无法绑定 Go 参数，保留原写法不拆分查询。
		Select("COUNT(*) AS granted_count,"+
			"COALESCE(SUM(status='unused' AND expired_at>UTC_TIMESTAMP(3)),0) AS unused_count,"+
			"COALESCE(SUM(status='used'),0) AS used_count,"+
			"COALESCE(SUM(status='expired' OR (status='unused' AND expired_at<=UTC_TIMESTAMP(3))),0) AS expired_count").
		Where("coupon_id=?", id).Scan(&counts).Error
	if err != nil {
		return out, err
	}
	out.GrantedCount, out.UnusedCount = counts.GrantedCount, counts.UnusedCount
	out.UsedCount, out.ExpiredCount = counts.UsedCount, counts.ExpiredCount
	if out.GrantedCount > 0 {
		out.UsageRate = float64(out.UsedCount) / float64(out.GrantedCount)
	}
	return out, nil
}

// findGrantReceipt 按请求号读取首次发放记录；需要当前读时取得共享锁，不创建请求记录。
func findGrantReceipt(tx *gorm.DB, requestID string, current bool) (CouponGrantReceipt, error) {
	var row struct {
		CouponID      uint64 `gorm:"column:coupon_id"`       // 模板
		UserID        uint64 `gorm:"column:user_id"`         // 受益用户
		CouponGrantID uint64 `gorm:"column:coupon_grant_id"` // 发放记录
	}
	q := tx.Table("coupon_grant_request").Where("request_id=?", requestID)
	if current {
		q = q.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := q.Take(&row).Error
	return CouponGrantReceipt{RequestID: requestID, CouponID: row.CouponID, UserID: row.UserID, CouponGrantID: row.CouponGrantID}, err
}

// manualGrantState 在用户锁后锁定券模板，统计已发份数；模板锁之后不能反向等待用户或钱包。
func manualGrantState(tx *gorm.DB, couponID, userID uint64) (AdminCouponRow, CouponUsage, error) {
	var coupon AdminCouponRow
	usage := CouponUsage{}
	if err := tx.Table("coupon").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", couponID).Take(&coupon).Error; err != nil {
		return coupon, usage, err
	}
	if err := tx.Table("coupon_grant").Clauses(clause.Locking{Strength: "UPDATE"}).Where("coupon_id=?", couponID).Count(&usage.TotalGranted).Error; err != nil {
		return coupon, usage, err
	}
	err := tx.Table("coupon_grant").Clauses(clause.Locking{Strength: "UPDATE"}).Where("coupon_id=? AND user_id=?", couponID, userID).Count(&usage.UserGranted).Error
	return coupon, usage, err
}

// saveManualGrant 在调用方事务内保存新券及请求号与发放 ID 的关联，失败须整体回滚。
func saveManualGrant(tx *gorm.DB, in activityGrant) (uint64, error) {
	if err := tx.Table("coupon_grant").Create(map[string]any{"coupon_id": in.CouponID, "user_id": in.UserID, "grant_source": marketingdomain.GrantSourceManual, "status": marketingdomain.GrantUnused, "expired_at": in.ExpiresAt, "source_event_id": in.EventID}).Error; err != nil {
		return 0, err
	}
	var id uint64
	if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
		return 0, err
	}
	err := tx.Table("coupon_grant_request").Create(map[string]any{"request_id": in.EventID, "coupon_id": in.CouponID, "user_id": in.UserID, "coupon_grant_id": id}).Error
	return id, err
}
