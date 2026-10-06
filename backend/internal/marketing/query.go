package marketing

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
	"time"
)

// AvailableCoupon 是原支付前可用券的精简读模型，不携带ORM或核销能力。
type AvailableCoupon struct {
	CouponID       uint64    `json:"coupon_id"`        // 原模板主键
	Name           string    `json:"name"`             // 原模板名称
	GrantID        uint64    `json:"grant_id"`         // 原发放主键
	DiscountType   string    `json:"discount_type"`    // 原优惠类型
	MinChargeCents int64     `json:"min_charge_cents"` // 原使用门槛，分
	ExpiresAt      time.Time `json:"expires_at"`       // 原有效截止时刻
}

// UserCoupon 是当前用户原持券视图；已过期状态只用于本次展示，不写回数据库。
type UserCoupon struct {
	CouponID           uint64    `json:"coupon_id"`            // 原模板主键
	Name               string    `json:"name"`                 // 原模板名称
	GrantID            uint64    `json:"grant_id"`             // 原发放主键
	DiscountType       string    `json:"discount_type"`        // 原优惠类型
	DiscountValueCents *int64    `json:"discount_value_cents"` // 原减免分值，其他口径为空
	DiscountPercent    *float64  `json:"discount_percent"`     // 原百分比，其他口径为空
	MinChargeCents     int64     `json:"min_charge_cents"`     // 原门槛分值
	Status             string    `json:"status"`               // 原used／unused，未用已到期展示expired
	ExpiredAt          time.Time `json:"expired_at"`           // 原截止兼容字段
	ExpiresAt          time.Time `json:"expires_at"`           // 原截止兼容字段，与expired_at相同
	Usable             bool      `json:"usable"`               // 原未用且未到期事实，不推断未来订单是否达门槛
}

// CouponPage 保留原count／total双字段；筛选后分页，空页输出数组。
type CouponPage struct {
	persist.Page[UserCoupon]     // 原用户券页、总数、页号及大小
	Count                    int `json:"count"` // 原筛选后总份数，与total一致
}

// CouponQueryService 拥有持券状态与原筛选／分页，不发放、核销或刷新到期时刻。
type CouponQueryService struct {
	DB  *gorm.DB         // 所属中央只读连接或事务，由进程管理
	Now func() time.Time // 可选本次展示时钟，空值使用当前UTC
}

// Available 在 ctx 下按 userID 原用户范围返回仍可用的券，返回原到期顺序数组或输入／存储错误。
// 只读取启用且未删除模板及未删除未用未过期发放，不执行核销或发放。
func (s CouponQueryService) Available(ctx context.Context, userID uint64) ([]AvailableCoupon, error) {
	if userID == 0 {
		return nil, &InputError{"用户编号无效"}
	}
	rows, err := (CouponRepo{DB: s.DB}).AvailableCoupons(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]AvailableCoupon, 0, len(rows))
	for _, row := range rows {
		out = append(out, AvailableCoupon{CouponID: row.CouponID, Name: row.Name, GrantID: row.GrantID, DiscountType: row.Type, MinChargeCents: row.MinCharge, ExpiresAt: row.ExpiresAt})
	}
	return out, nil
}

// Mine 在 ctx 下按 userID 和 q 原分页／状态及 onlyUsable 组合持券视图，返回原页或输入／存储错误。
// 原顺序和启用模板过滤保持；状态、可用性在同一本次时刻求值后分页，不把存储故障当作空券。
func (s CouponQueryService) Mine(ctx context.Context, userID uint64, q persist.PageQuery, onlyUsable bool) (CouponPage, error) {
	out := CouponPage{Page: persist.Page[UserCoupon]{Items: []UserCoupon{}, Page: q.Page, PageSize: q.PageSize}}
	if userID == 0 || q.Page < 1 || q.Page > 100000 || q.PageSize < 1 || q.PageSize > 100 {
		return out, &InputError{"分页或用户编号无效"}
	}
	if q.Status != "" && q.Status != "unused" && q.Status != "used" && q.Status != "expired" {
		return out, &InputError{"优惠券状态无效"}
	}
	rows, err := ListUserCoupons(ctx, s.DB, userID)
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	items := []UserCoupon{}
	for _, row := range rows {
		status := row.Status
		if status == "unused" && !row.ExpiresAt.After(now) {
			status = "expired"
		}
		if q.Status != "" && status != q.Status {
			continue
		}
		usable := status == "unused" && row.ExpiresAt.After(now)
		if onlyUsable && !usable {
			continue
		}
		items = append(items, UserCoupon{CouponID: row.CouponID, Name: row.Name, GrantID: row.GrantID, DiscountType: row.Type, DiscountValueCents: row.Amount, DiscountPercent: row.Percent, MinChargeCents: row.MinCharge, Status: status, ExpiredAt: row.ExpiresAt, ExpiresAt: row.ExpiresAt, Usable: usable})
	}
	out.Count = len(items)
	out.Total = int64(len(items))
	start := min((q.Page-1)*q.PageSize, len(items))
	end := min(start+q.PageSize, len(items))
	out.Items = items[start:end]
	return out, nil
}
