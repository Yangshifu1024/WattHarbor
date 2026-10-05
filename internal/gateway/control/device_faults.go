package control

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
)

// registerDeviceFaults 把 device_event 故障证据暴露给 central 的告警同步：
// 未处理故障列表、处理标记与最新心跳查询。全部要求服务令牌，
// 原始事件载荷原样返回，由调用方按协议语义解析。
func (a TelemetryAPI) registerDeviceFaults(r *gin.Engine) {
	r.GET(contract.DeviceFaultsPath, a.deviceFaults)
	r.POST(contract.MarkDeviceFaultsPath, a.markFaultsProcessed)
	r.GET("/api/v1/internal/devices/:device_id/latest-heartbeat", a.latestHeartbeat)
}

// deviceFaults 接收 c 中的服务令牌和可选 limit，按 id 升序返回原未处理故障载荷，空 reports 保持 []。
// limit 一至一百，默认一百；鉴权失败返回 401，限额无效返回 400，读取失败返回原 503，无标记写入。
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

// markFaultsProcessed 接收 c 中的服务令牌及一至一百个原事件主键，交由 repo 完整事务推进仍未处理记录。
// 成功返回实际影响行数；鉴权失败返回 401，绑定或长度错误返回 400，事务失败返回原 503 且不输出中间计数。
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

// latestHeartbeat 接收 c 中的服务令牌、设备 ID 和原水印参数，非空 after_key 优先且不解析 after。
// 没有 key 时解析 RFC3339Nano 时间，交由 repo 完整读取，再返回缺水印、缺心跳或原心跳证据。
// 鉴权失败返回 401，设备/时间无效返回 400；零水印与 SQL 错误分别映射原 503 消息，不解释协议恢复状态。
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
