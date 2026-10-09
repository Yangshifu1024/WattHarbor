package control

import (
	gatewaycontract "chargepilot/internal/contracts/gateway"
	gatewayrepo "chargepilot/internal/gateway/repo"
	"net/http"
	"strconv"
	"strings"

	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// chargeProcess 接收 c 中的原订单及可选身份、游标和 limit，先鉴权校验，再读取过程点并用多取一条生成下一游标；写 200、400、401 或 503，无业务状态写入。
func (a TelemetryAPI) chargeProcess(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	orderNo := c.Param("order_no")
	if strings.TrimSpace(orderNo) == "" || len(orderNo) > 64 {
		httpapi.BadRequest(c, "订单编号无效")
		return
	}
	values := c.Request.URL.Query()
	afterID := uint64(0)
	if values.Has("after_id") {
		var err error
		afterID, err = strconv.ParseUint(values.Get("after_id"), 10, 64)
		if err != nil {
			httpapi.BadRequest(c, "after_id 须为非负整数")
			return
		}
	}
	limit := 1000
	if values.Has("limit") {
		var err error
		limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > 2000 {
			httpapi.BadRequest(c, "limit 须为1至2000")
			return
		}
	}
	// 在读取数据库前校验全部原身份筛选。
	var chargeOrderID uint64
	if values.Has("charge_order_id") {
		var err error
		chargeOrderID, err = strconv.ParseUint(values.Get("charge_order_id"), 10, 64)
		if err != nil || chargeOrderID == 0 {
			httpapi.BadRequest(c, "charge_order_id 须为有效订单ID")
			return
		}
	}
	deviceID := values.Get("device_id")
	if values.Has("device_id") && (strings.TrimSpace(deviceID) == "" || len(deviceID) > 64) {
		httpapi.BadRequest(c, "设备编号无效")
		return
	}
	var portNo uint64
	if values.Has("port_no") {
		var err error
		portNo, err = strconv.ParseUint(values.Get("port_no"), 10, 8)
		if err != nil || portNo == 0 {
			httpapi.BadRequest(c, "port_no 须为有效端口号")
			return
		}
	}
	items, err := gatewayrepo.ListChargeProcessPoints(
		c.Request.Context(),
		a.DB,
		orderNo,
		chargeOrderID,
		deviceID,
		portNo,
		afterID,
		limit,
	)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "充电过程暂不可读取", nil)
		return
	}
	var next *uint64
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1].ID
		next = &last
	}
	points := make([]gatewaycontract.ProcessPoint, len(items))
	for i, row := range items {
		points[i] = gatewaycontract.ProcessPoint{ID: row.ID, TS: row.TS.UTC(), PowerW: row.PowerW, ChargedKWh: row.ChargedKWh, RemainingKWh: row.RemainingKWh, ChargedSeconds: row.ChargedSeconds, RemainingSeconds: row.RemainingSeconds, SignalStrength: row.SignalStrength, PortStatus: row.PortStatus, VoltageV: row.VoltageV, TemperatureC: row.TemperatureC, DeviceStatus: row.DeviceStatus}
	}
	httpapi.OK(c, gatewaycontract.OrderProcessReply{Items: points, NextAfterID: next})
}
