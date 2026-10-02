package http

import (
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"time"
)

// 我的优惠券。
func (a UserAccountAPI) myCoupons(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, size, ok := readPaging(c)
	if !ok {
		return
	}
	status := c.Query("status")
	if status != "" && !oneOfStatus(status, "unused used expired") {
		httpapi.BadRequest(c, "优惠券状态无效")
		return
	}
	rows := []orderstore.CouponGrant{}
	err := a.UserDB.WithContext(c.Request.Context()).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c2 ON c2.id = g.coupon_id AND c2.deleted_at IS NULL AND c2.status = 'active'").
		Where("g.user_id = ? AND g.deleted_at IS NULL", userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c2.name, c2.discount_type, c2.discount_value_cents, c2.discount_percent, c2.min_charge_cents").
		Order("g.status ASC, g.expired_at ASC, g.id ASC").Find(&rows).Error
	if err != nil {
		httpapi.Write(c, 503, 5003, "优惠券暂时无法读取", nil)
		return
	}
	onlyUsable := c.Query("only_usable") == "true"
	now := time.Now().UTC()
	out := []gin.H{}
	for _, row := range rows {
		if row.Status == "unused" && !row.ExpiresAt.After(now) {
			row.Status = "expired"
		}
		if status != "" && row.Status != status {
			continue
		}
		usable := row.Status == "unused" && row.ExpiresAt.After(now)
		if onlyUsable && !usable {
			continue
		}
		out = append(out, gin.H{
			"coupon_id": row.CouponID, "name": row.Name, "grant_id": row.GrantID,
			"discount_type": row.Type, "discount_value_cents": row.Amount,
			"discount_percent": row.Percent, "min_charge_cents": row.MinCharge,
			"status": row.Status, "expired_at": row.ExpiresAt, "expires_at": row.ExpiresAt, "usable": usable,
		})
	}
	total := len(out)
	start := min((page-1)*size, total)
	end := min(start+size, total)
	httpapi.OK(c, gin.H{"items": out[start:end], "count": total, "total": total, "page": page, "page_size": size})
}

// ---- 公告 ----
