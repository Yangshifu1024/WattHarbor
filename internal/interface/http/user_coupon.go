package http

import (
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
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
	rows, err := marketing.ListUserCoupons(c.Request.Context(), a.UserDB, userID)
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
