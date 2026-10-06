package control

import (
	"encoding/json"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// historicalCurve 查询 central 根据订单确定的时间窗口。
// 网关不读取 central_db，订单归属由 central 校验，调用此内部接口需要服务令牌。
func (a TelemetryAPI) historicalCurve(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	deviceID := c.Param("device_id")
	orderID := strings.TrimSpace(c.Query("order_id"))
	portNo, portErr := strconv.Atoi(c.Query("port_no"))
	if deviceID == "" || len(deviceID) > 64 || orderID == "" || len(orderID) > 64 || portErr != nil || portNo < 1 || portNo > 255 {
		httpapi.BadRequest(c, "设备、订单或端口参数无效")
		return
	}
	from, fromErr := time.Parse(time.RFC3339, c.Query("started_at"))
	to := time.Now().UTC()
	var toErr error
	if c.Query("ended_at") != "" {
		to, toErr = time.Parse(time.RFC3339, c.Query("ended_at"))
	}
	if fromErr != nil || toErr != nil || !from.Before(to) || to.After(time.Now().UTC().Add(5*time.Minute)) {
		httpapi.BadRequest(c, "订单时间窗无效")
		return
	}
	if from.Before(time.Now().UTC().AddDate(-3, 0, 0)) {
		httpapi.Write(c, http.StatusUnprocessableEntity, 2018, "历史遥测已超过三年保留期", nil)
		return
	}
	granularity, ok := gatewayrepo.CurveGranularityFor(c.Query("granularity"))
	if !ok {
		httpapi.BadRequest(c, "granularity 须为 15min 或 hourly")
		return
	}
	bucket, table := granularity.Bucket, granularity.Table
	startBucket, endBucket := from.UTC().Truncate(bucket), to.UTC().Truncate(bucket)
	limit := a.maxPoints()
	if int(endBucket.Sub(startBucket)/bucket)+1 > limit {
		httpapi.BadRequest(c, "订单时间窗超过当前粒度的最大点数，请使用更粗粒度")
		return
	}
	series, err := a.readPort(c.Request.Context(), deviceID, portNo, startBucket, endBucket, limit, table)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "历史遥测暂时无法读取", nil)
		return
	}
	peaks, err := gatewayrepo.MetricPeaksInRange(c.Request.Context(), a.DB, table, deviceID, uint16(portNo), startBucket, endBucket)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "历史遥测摘要暂时无法读取", nil)
		return
	}
	summary := map[string]json.RawMessage{}
	for _, peak := range peaks {
		switch peak.Metric {
		case "power_w":
			summary["max_power_w"] = curveSummaryValue(peak.Peak)
			summary["avg_power_w"] = curveSummaryValue(peak.Avg)
		case "temperature_c":
			summary["max_temperature_c"] = curveSummaryValue(peak.Peak)
		}
	}
	httpapi.OK(c, gatewaycontract.HistoricalCurveReply{OrderNo: orderID, DeviceID: deviceID, PortNo: uint8(portNo), StartedAt: from.UTC(), EndedAt: to.UTC(), Granularity: c.Query("granularity"), Series: series, Count: len(series), Summary: summary, BoundaryApproximate: !from.Equal(startBucket) || !to.Equal(endBucket)})
}

// curveSummaryValue 将 value 原DECIMAL文本编码为JSON字符串，返回精确原文本载荷，无IO或数值转换。
// 字符串编码不会失败；摘要保持原公开字符串类型，不替换成浮点数。
func curveSummaryValue(value string) json.RawMessage { raw, _ := json.Marshal(value); return raw }
