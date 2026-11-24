package control

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	contract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/gateway/repo"
	"wattharbor/internal/platform/httpapi"

	"github.com/gin-gonic/gin"
)

func (a DeviceEventAPI) orderCurve(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	q := contract.OrderCurveRequest{
		OrderNo:     strings.TrimSpace(c.Param("order_no")),
		DeviceID:    strings.TrimSpace(c.Query("device_id")),
		Granularity: c.Query("granularity"),
		Latest:      c.Query("view") == "latest",
		Summary:     c.Query("view") == "history",
	}
	id, idErr := strconv.ParseUint(c.Query("charge_order_id"), 10, 64)
	port, portErr := strconv.ParseUint(c.Query("port_no"), 10, 8)
	from, fromErr := time.Parse(time.RFC3339Nano, c.Query("from"))
	to, toErr := time.Parse(time.RFC3339Nano, c.Query("to"))
	if idErr != nil || id == 0 || portErr != nil || port == 0 ||
		q.OrderNo == "" || len(q.OrderNo) > 64 || q.DeviceID == "" || len(q.DeviceID) > 64 ||
		fromErr != nil || toErr != nil || !from.Before(to) || to.Sub(from) > contract.MaxOrderCurveWindow ||
		(q.Granularity != "auto" && q.Granularity != "15min" && q.Granularity != "hourly") {
		httpapi.BadRequest(c, "订单采样查询参数无效")
		return
	}
	q.OrderID, q.PortNo, q.From, q.To = id, uint8(port), from.UTC(), to.UTC()
	out, err := repo.OrderCurve(c.Request.Context(), a.DB, q)
	if q.Latest && len(out.Series) == 0 {
		if sample, ok := a.Snapshots.Latest(q); ok {
			httpapi.OK(c, contract.OrderCurveReply{Series: []contract.CurvePoint{sample}, Summary: map[string]json.RawMessage{}})
			return
		}
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "订单曲线暂不可读取", nil)
		return
	}
	httpapi.OK(c, out)
}
