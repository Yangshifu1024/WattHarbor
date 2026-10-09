package control

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"

	"chargepilot/internal/gateway/repo"
)

// registerDeviceFaults 把 device_event 故障证据暴露给 central 的告警同步：
// 未处理故障列表、处理标记与最新心跳查询。全部要求服务令牌，
// 原始事件载荷原样返回，由调用方按协议语义解析。
func (a TelemetryAPI) registerDeviceFaults(r *gin.Engine) {
	r.GET(contract.DeviceFaultsPath, a.deviceFaults)
	r.POST(contract.MarkDeviceFaultsPath, a.markFaultsProcessed)
	r.GET("/api/v1/internal/devices/:device_id/latest-heartbeat", a.latestHeartbeat)
}

// deviceFaults 鉴权后按 ID 升序返回未处理故障，最多 100 条；空列表返回 []。
func (a TelemetryAPI) deviceFaults(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			httpapi.BadRequest(c, "limit 须为 1–100")
			return
		}
		limit = parsed
	}
	reports, err := repo.ListUnprocessedFaults(c.Request.Context(), a.DB, limit)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "故障事件暂不可读取", nil)
		return
	}
	out := contract.DeviceFaultsReply{Reports: make([]contract.DeviceFaultReport, 0, len(reports))}
	for _, r := range reports {
		out.Reports = append(out.Reports, contract.DeviceFaultReport{ID: r.ID, EventKey: r.EventKey, DeviceID: r.DeviceID, ReceivedAt: r.ReceivedAt, Payload: r.Payload})
	}
	httpapi.OK(c, out)
}

// markFaultsProcessed 鉴权后在同一事务内标记 1 至 100 条故障事件，返回实际影响行数。
func (a TelemetryAPI) markFaultsProcessed(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var body contract.MarkDeviceFaultsRequest
	if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) == 0 || len(body.IDs) > 100 {
		httpapi.BadRequest(c, "ids 须为 1–100 个事件主键")
		return
	}
	marked, err := repo.MarkEventsProcessed(c.Request.Context(), a.DB, body.IDs)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "处理标记暂不可写", nil)
		return
	}
	httpapi.OK(c, contract.MarkDeviceFaultsReply{Marked: marked})
}

// latestHeartbeat 查询指定起点之后的最新心跳；after_key 优先，只有它为空时才解析 after 时间。
// 设备和时间无效返回 400，起点不可读或数据库错误返回 503。
func (a TelemetryAPI) latestHeartbeat(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Param("device_id")
	if device == "" || len(device) > 64 {
		httpapi.BadRequest(c, "设备无效")
		return
	}
	key := c.Query("after_key")
	var after time.Time
	var err error
	if key == "" {
		after, err = time.Parse(time.RFC3339Nano, c.Query("after"))
		if err != nil {
			httpapi.BadRequest(c, "after 须为 RFC 3339 时间")
			return
		}
	}
	evidence, err := repo.ReadLatestHeartbeat(
		c.Request.Context(),
		a.DB,
		device,
		key,
		after,
	)
	if err != nil {
		if errors.Is(err, repo.ErrHeartbeatWatermarkUnreadable) {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "故障水印暂不可读取", nil)
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "心跳证据暂不可读取", nil)
		return
	}
	if evidence.Heartbeat == nil {
		httpapi.OK(c, contract.HeartbeatAfterKeyReply{Found: false, AfterReceivedAt: evidence.Watermark})
		return
	}
	httpapi.OK(c, contract.HeartbeatAfterKeyReply{Found: true, AfterReceivedAt: evidence.Watermark, ReceivedAt: &evidence.Heartbeat.ReceivedAt, Payload: string(evidence.Heartbeat.Payload)})
}
