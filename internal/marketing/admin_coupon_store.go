package marketing

import (
	"context"
	"errors"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对券模板与人工发放的读写。C 端的持券、核销在 coupon_store.go /
// coupon_redeem.go，活动触发发券在 coupon_activity.go；这里只管后台这一面。

// AdminCouponRow 是券模板的完整行。优惠三选一：amount 用 DiscountValueCents、
// percentage 用 DiscountPercent、time_free 用 FreeMinutes。
type AdminCouponRow struct {
	ID                 uint64           `json:"id" gorm:"column:id;primaryKey"` // 券模板主键；创建时须为 0 由库自增
	Name               string           `json:"name" gorm:"column:name"`        // 券名称，非空且不超过 128 字符
	DiscountType       string           `json:"discount_type" gorm:"column:discount_type"`
	DiscountValueCents *int64           `json:"discount_value_cents" gorm:"column:discount_value_cents"`
	DiscountPercent    *decimal.Decimal `json:"discount_percent" gorm:"column:discount_percent"`
	FreeMinutes        *uint32          `json:"free_minutes" gorm:"column:free_minutes"`
	MinChargeCents     int64            `json:"min_charge_cents" gorm:"column:min_charge_cents"`
	ValidHours         uint32           `json:"valid_hours" gorm:"column:valid_hours"`
	TotalQuota         uint32           `json:"total_quota" gorm:"column:total_quota"`
	PerUserQuota       uint32           `json:"per_user_quota" gorm:"column:per_user_quota"`
	Status             string           `json:"status" gorm:"column:status"`
	StartAt            *time.Time       `json:"start_at" gorm:"column:start_at"`
	EndAt              *time.Time       `json:"end_at" gorm:"column:end_at"`
}

func (AdminCouponRow) TableName() string { return "coupon" }

// AdminCouponStats 是某张券的发放统计。四个状态互斥，加总等于发放总数；
// 未使用与已过期按当前时刻动态判定，而非只看 status 列。
type AdminCouponStats struct {
	TotalQuota   uint32  `json:"total_quota"`
	GrantedCount int64   `json:"granted_count"`
	UnusedCount  int64   `json:"unused_count"`
	UsedCount    int64   `json:"used_count"`
	ExpiredCount int64   `json:"expired_count"`
	UsageRate    float64 `json:"usage_rate"` // 已使用 / 已发放；未发放时为 0
}

// AdminCouponStore 是后台券模板的读写入口。
type AdminCouponStore struct{ DB *gorm.DB }

// ListCoupons 返回全部未删除的券模板，按 ID 倒序。列表不分页，模板量有限。
func (s AdminCouponStore) ListCoupons(ctx context.Context) ([]AdminCouponRow, error) {
	rows := []AdminCouponRow{}
	err := s.DB.WithContext(ctx).Table("coupon").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error
	return rows, err
}

// CreateCoupon 新建券模板，状态由调用方置为 active。
// 入参是指针：主键由库自增回填，调用方要用它写审计。传值会把自增结果丢在函数里，
// 表现为新建成功但响应与审计里的 id 都是 0。
func (s AdminCouponStore) CreateCoupon(ctx context.Context, in *AdminCouponRow) error {
	return s.DB.WithContext(ctx).Create(in).Error
}

// UpdateCoupon 只改名称与启停状态。额度与优惠力度发出去就不能再改，
// 否则已发出的券无法解释。改前对券行加写锁并返回旧值供审计留痕。
func (s AdminCouponStore) UpdateCoupon(ctx context.Context, id uint64, name, status string) (AdminCouponRow, error) {
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
func (s AdminCouponStore) Stats(ctx context.Context, id uint64) (AdminCouponStats, error) {
	out := AdminCouponStats{}
	db := s.DB.WithContext(ctx)
	var coupon AdminCouponRow
	if err := db.Table("coupon").Where("id=? AND deleted_at IS NULL", id).Take(&coupon).Error; err != nil {
		return out, err
	}
	out.TotalQuota = coupon.TotalQuota
	var counts struct {
		GrantedCount int64 `gorm:"column:granted_count"`
		UnusedCount  int64 `gorm:"column:unused_count"`
		UsedCount    int64 `gorm:"column:used_count"`
		ExpiredCount int64 `gorm:"column:expired_count"`
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

// GrantCouponInTx 在事务内锁定模板并校验有效性、用户状态、总量与单人额度后发券。
// 同一 requestID、模板与用户返回首次券 ID；同号不同内容返回 ErrCouponGrantConflict。
// 到期时间取"领取后有效期"与"模板结束时间"的较早值。
//
// 调用方负责把本函数放进自己的事务，以便与审计同事务提交。
func GrantCouponInTx(tx *gorm.DB, couponID, userID uint64, requestID string) (uint64, error) {
	var coupon AdminCouponRow
	if err := tx.Table("coupon").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", couponID).Take(&coupon).Error; err != nil {
		return 0, err
	}
	var receipt struct{ CouponID, UserID, CouponGrantID uint64 }
	err := tx.Table("coupon_grant_request").Where("request_id=?", requestID).Take(&receipt).Error
	if err == nil {
		return money.ReplayGrant(money.CouponGrantReceipt{RequestID: requestID, CouponID: receipt.CouponID,
			UserID: receipt.UserID, CouponGrantID: receipt.CouponGrantID}, requestID, couponID, userID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	var user struct{ Status string }
	if err := tx.Table("user").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", userID).Take(&user).Error; err != nil {
		return 0, err
	}
	if user.Status != "active" {
		return 0, money.ErrCouponGrant
	}
	var total, perUser int64
	if err := tx.Table("coupon_grant").Where("coupon_id=?", couponID).Count(&total).Error; err != nil {
		return 0, err
	}
	if err := tx.Table("coupon_grant").Where("coupon_id=? AND user_id=?", couponID, userID).Count(&perUser).Error; err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	template := money.CouponTemplate{TemplateID: coupon.ID, Status: coupon.Status, TotalQuota: coupon.TotalQuota,
		PerUserQuota: coupon.PerUserQuota, ValidHours: coupon.ValidHours, StartAt: coupon.StartAt, EndAt: coupon.EndAt}
	if err := money.CheckGrantable(now, template, money.CouponUsage{TotalGranted: total, UserGranted: perUser}); err != nil {
		return 0, money.ErrCouponGrant
	}
	if err := tx.Table("coupon_grant").Create(map[string]any{
		"coupon_id": couponID, "user_id": userID, "grant_source": "manual", "status": "unused",
		"expired_at": money.GrantExpiry(now, template), "source_event_id": requestID,
	}).Error; err != nil {
		return 0, err
	}
	var grantID uint64
	if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&grantID).Error; err != nil {
		return 0, err
	}
	if err := tx.Table("coupon_grant_request").Create(map[string]any{
		"request_id": requestID, "coupon_id": couponID, "user_id": userID, "coupon_grant_id": grantID,
	}).Error; err != nil {
		return 0, err
	}
	return grantID, nil
}
