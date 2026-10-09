package order

import (
	"errors"
	"strconv"

	chargingquery "chargepilot/internal/charging/query"
	"chargepilot/internal/domain/operations"
	adminhttp "chargepilot/internal/http/admin"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// orderProcessQuery 保存订单事件分页的游标和页长。
type orderProcessQuery struct {
	AfterID uint64 // 过程行游标，首读为 0
	Limit   uint16 // 单页条数，1–2000
}

// parseOrderProcessQuery 从请求读取游标与页长，返回已校验参数及成功标志；无效时直接输出 400。
func parseOrderProcessQuery(c *gin.Context) (orderProcessQuery, bool) {
	q := orderProcessQuery{Limit: 1000}
	values := c.Request.URL.Query()
	for _, name := range []string{"after_id", "limit"} {
		if raw, exists := values[name]; exists {
			if len(raw) != 1 || raw[0] == "" {
				httpapi.BadRequest(c, "过程游标或分页参数无效")
				return q, false
			}
			n, err := strconv.ParseUint(raw[0], 10, 64)
			if err != nil || (name == "limit" && (n == 0 || n > 2000)) {
				httpapi.BadRequest(c, "过程游标必须非负，limit 为 1–2000")
				return q, false
			}
			if name == "after_id" {
				q.AfterID = n
			} else {
				q.Limit = uint16(n)
			}
		}
	}
	return q, true
}

// orderProcess 校验订单 ID 和分页参数，查询订单事件与计量记录。
func (a API) orderProcess(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	q, ok := parseOrderProcessQuery(c)
	if !ok {
		return
	}
	page, err := a.Process.Read(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), id, q.AfterID, q.Limit)
	if errors.Is(err, chargingquery.ErrProcessUnavailable) {
		httpapi.Write(c, 503, 5003, chargingquery.ErrProcessUnavailable.Error(), nil)
		return
	}
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, page)
}
