package marketing

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对券模板与人工发放的读写。C 端的持券、核销在 coupon_repo.go /
// coupon_redeem_repo.go，活动用例在 activity_grant.go；这里只管后台这一面。

// AdminCouponRow 是券模板的完整行。优惠三选一：amount 用 DiscountValueCents、
// percentage 用 DiscountPercent、time_free 用 FreeMinutes。
type AdminCouponRow struct {
	ID                 uint64           `json:"id" gorm:"column:id;primaryKey"`                          // 券模板主键；创建时须为 0 由库自增
	Name               string           `json:"name" gorm:"column:name"`                                 // 券名称，非空且不超过 128 字符
	DiscountType       string           `json:"discount_type" gorm:"column:discount_type"`               // 原优惠类型
	DiscountValueCents *int64           `json:"discount_value_cents" gorm:"column:discount_value_cents"` // 原减免分值，可空
	DiscountPercent    *decimal.Decimal `json:"discount_percent" gorm:"column:discount_percent"`         // 原优惠百分比，可空
	FreeMinutes        *uint32          `json:"free_minutes" gorm:"column:free_minutes"`                 // 原免费分钟，可空
	MinChargeCents     int64            `json:"min_charge_cents" gorm:"column:min_charge_cents"`         // 原使用门槛，分
	ValidHours         uint32           `json:"valid_hours" gorm:"column:valid_hours"`                   // 原领取后有效小时
	TotalQuota         uint32           `json:"total_quota" gorm:"column:total_quota"`                   // 原总量上限，0不限
	PerUserQuota       uint32           `json:"per_user_quota" gorm:"column:per_user_quota"`             // 原人工发放单人上限
	Status             string           `json:"status" gorm:"column:status"`                             // 原业务状态
	StartAt            *time.Time       `json:"start_at" gorm:"column:start_at"`                         // 原开始时刻，可空表示立即
	EndAt              *time.Time       `json:"end_at" gorm:"column:end_at"`                             // 原截止时刻，可空表示长期
}

// TableName 固定原模板表，不依赖类型改名推导，无IO。
func (AdminCouponRow) TableName() string { return "coupon" }

// AdminCouponStats 是某张券的发放统计。四个状态互斥，加总等于发放总数；
// 未使用与已过期按当前时刻动态判定，而非只看 status 列。
type AdminCouponStats struct {
	TotalQuota   uint32  `json:"total_quota"`   // 原总量上限，0不限
	GrantedCount int64   `json:"granted_count"` // 原已发份数
	UnusedCount  int64   `json:"unused_count"`  // 原未使用未过期份数
	UsedCount    int64   `json:"used_count"`    // 原已使用份数
	ExpiredCount int64   `json:"expired_count"` // 原到期份数
	UsageRate    float64 `json:"usage_rate"`    // 已使用 / 已发放；未发放时为 0
}

// AdminCouponRepo 是后台券模板的读写入口。
type AdminCouponRepo struct {
	DB *gorm.DB // 所属中央连接或调用方事务，由进程管理
}

// ListCoupons 返回全部未删除的券模板，按 ID 倒序。列表不分页，模板量有限。
func (s AdminCouponRepo) ListCoupons(ctx context.Context) ([]AdminCouponRow, error) {
	rows := []AdminCouponRow{}
	err := s.DB.WithContext(ctx).Table("coupon").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error
	return rows, err
}

// CreateCoupon 新建券模板，状态由调用方置为 active。
// 入参是指针：主键由库自增回填，调用方要用它写审计。传值会把自增结果丢在函数里，
// 表现为新建成功但响应与审计里的 id 都是 0。
func (s AdminCouponRepo) CreateCoupon(ctx context.Context, in *AdminCouponRow) error {
	return s.DB.WithContext(ctx).Create(in).Error
}

// UpdateCoupon 只改名称与启停状态。额度与优惠力度发出去就不能再改，
// 否则已发出的券无法解释。改前对券行加写锁并返回旧值供审计留痕。
func (s AdminCouponRepo) UpdateCoupon(ctx context.Context, id uint64, name, status string) (AdminCouponRow, error) {
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
		GrantedCount int64 `gorm:"column:granted_count"` // 原已发份数
		UnusedCount  int64 `gorm:"column:unused_count"`  // 原未使用未过期份数
		UsedCount    int64 `gorm:"column:used_count"`    // 原已使用份数
		ExpiredCount int64 `gorm:"column:expired_count"` // 原到期份数
	}
	// status='unused' 但已过期的记为过期：判定看当前时刻，不只看待存列。
	err := db.Table("coupon_grant").
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

// findGrantReceipt 在 tx 中按 requestID 读取原受理凭据；current 为真时取得SHARE当前读。
// 返回原凭据或缺失／存储错误，不创建请求预占、不提交事务。
func findGrantReceipt(tx *gorm.DB, requestID string, current bool) (CouponGrantReceipt, error) {
	var row struct {
		CouponID      uint64 `gorm:"column:coupon_id"`       // 原模板
		UserID        uint64 `gorm:"column:user_id"`         // 原受益用户
		CouponGrantID uint64 `gorm:"column:coupon_grant_id"` // 原发放记录
	}
	q := tx.Table("coupon_grant_request").Where("request_id=?", requestID)
	if current {
		q = q.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := q.Take(&row).Error
	return CouponGrantReceipt{RequestID: requestID, CouponID: row.CouponID, UserID: row.UserID, CouponGrantID: row.CouponGrantID}, err
}

// manualGrantState 在 tx 已持 userID 用户锁后锁 couponID 模板并当前读取预算。
// 返回原模板、累计份数或缺失／存储错误；不访问用户表，不反向等待用户或钱包。
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

// saveManualGrant 在 tx 内追加 in 原发放及请求凭据，返回原自增主键或存储错误；失败由调用方整体回滚。
func saveManualGrant(tx *gorm.DB, in activityGrant) (uint64, error) {
	if err := tx.Table("coupon_grant").Create(map[string]any{"coupon_id": in.CouponID, "user_id": in.UserID, "grant_source": "manual", "status": "unused", "expired_at": in.ExpiresAt, "source_event_id": in.EventID}).Error; err != nil {
		return 0, err
	}
	var id uint64
	if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
		return 0, err
	}
	err := tx.Table("coupon_grant_request").Create(map[string]any{"request_id": in.EventID, "coupon_id": in.CouponID, "user_id": in.UserID, "coupon_grant_id": id}).Error
	return id, err
}
