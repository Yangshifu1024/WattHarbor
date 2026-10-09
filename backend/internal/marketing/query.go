package marketing

import (
	"chargepilot/internal/persist"
	"context"
	"gorm.io/gorm"
	"time"
)

// AvailableCoupon 是支付前可用券的展示资料。
type AvailableCoupon struct {
	CouponID       uint64    `json:"coupon_id"`        // 模板主键
	Name           string    `json:"name"`             // 模板名称
	GrantID        uint64    `json:"grant_id"`         // 发放主键
	DiscountType   string    `json:"discount_type"`    // 优惠类型
	MinChargeCents int64     `json:"min_charge_cents"` // 使用门槛，分
	ExpiresAt      time.Time `json:"expires_at"`       // 有效截止时刻
}

// UserCoupon 是本人券列表记录，到期状态只用于展示，不写回数据库。
type UserCoupon struct {
	CouponID           uint64    `json:"coupon_id"`            // 模板主键
	Name               string    `json:"name"`                 // 模板名称
	GrantID            uint64    `json:"grant_id"`             // 发放主键
	DiscountType       string    `json:"discount_type"`        // 优惠类型
	DiscountValueCents *int64    `json:"discount_value_cents"` // 减免分值，其他优惠类型为空
	DiscountPercent    *float64  `json:"discount_percent"`     // 百分比，其他优惠类型为空
	MinChargeCents     int64     `json:"min_charge_cents"`     // 门槛金额
	Status             string    `json:"status"`               // used／unused，未用已到期展示 expired
	ExpiredAt          time.Time `json:"expired_at"`           // 截止兼容字段
	ExpiresAt          time.Time `json:"expires_at"`           // 截止兼容字段，与 expired_at 相同
	Usable             bool      `json:"usable"`               // 未用且未到期事实，不推断未来订单是否达门槛
}

// CouponPage 保留 count、total 字段，筛选后分页，空页返回数组。
type CouponPage struct {
	persist.Page[UserCoupon]     // 用户券页、总数、页号及大小
	Count                    int `json:"count"` // 筛选后总份数，与 total 一致
}

// CouponQueryService 计算券展示状态并筛选分页，不修改到期时间。
type CouponQueryService struct {
	DB  *gorm.DB         // 所属中央只读连接或事务，由进程管理
	Now func() time.Time // 可选本次展示时钟，空值使用当前 UTC
}

// Available 按到期时间返回本人可用券，排除停用、删除、已用和过期记录。
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

// Mine 按同一当前时间计算本人券状态，再筛选分页；保留查询排序，查询失败不能当作空列表。
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
