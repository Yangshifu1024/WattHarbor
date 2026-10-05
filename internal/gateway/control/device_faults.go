package control

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
)

// registerDeviceFaults 把 device_event 故障证据暴露给 central 的告警同步：
// 未处理故障列表、处理标记与最新心跳查询。全部要求服务令牌，
// 原始事件载荷原样返回，由调用方按协议语义解析。
func (a TelemetryAPI) registerDeviceFaults(r *gin.Engine) {
	r.GET(contract.DeviceFaultsPath, a.deviceFaults)
	r.POST(contract.MarkDeviceFaultsPath, a.markFaultsProcessed)
	r.GET("/api/v1/internal/devices/:device_id/latest-heartbeat", a.latestHeartbeat)
}

// deviceFaults 按 id 升序返回未处理故障，limit 1–100，默认 100。
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
	reports, err := store.ListUnprocessedFaults(c.Request.Context(), a.DB, limit)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "故障事件暂不可读取", nil)
		return
	}
	out := contract.DeviceFaultData{Reports: make([]contract.DeviceFaultReport, 0, len(reports))}
	for _, r := range reports {
		out.Reports = append(out.Reports, contract.DeviceFaultReport{ID: r.ID, EventKey: r.EventKey, DeviceID: r.DeviceID, ReceivedAt: r.ReceivedAt, Payload: r.Payload})
	}
	httpapi.OK(c, out)
}

// markFaultsProcessed 批量推进故障事件的处理时间；已处理的行跳过，
// 返回实际推进条数。行锁保证与并发扫描的顺序一致。
func (a TelemetryAPI) markFaultsProcessed(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var body contract.MarkDeviceFaultsRequest
	if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) == 0 || len(body.IDs) > 100 {
		httpapi.BadRequest(c, "ids 须为 1–100 个事件主键")
		return
	}
	marked := int64(0)
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		count, err := store.MarkEventsProcessed(tx, body.IDs)
		marked = count
		return err
	})
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "处理标记暂不可写", nil)
		return
	}
	httpapi.OK(c, contract.MarkDeviceFaultsData{Marked: marked})
}

// latestHeartbeat 返回某设备在指定时间之后的最新 dc589 心跳原始载荷；
// 没有更新的心跳时 found 为 false，供恢复判定区分"无证据"与"未恢复"。
// after_key 以事件键为水印（取该事件的接收时间），优先于 after；
// 恢复判定用它锚定"最近一次故障之后"的心跳。
func (a TelemetryAPI) latestHeartbeat(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Param("device_id")
	if device == "" || len(device) > 64 {
		httpapi.BadRequest(c, "设备无效")
		return
	}
	var after time.Time
	var err error
	if key := c.Query("after_key"); key != "" {
		receivedAt, found := store.FindEventReceivedAt(c.Request.Context(), a.DB, device, key)
		if found != nil {
			if errors.Is(found, gorm.ErrRecordNotFound) {
				httpapi.OK(c, contract.HeartbeatEvidence{Found: false})
				return
			}
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "心跳证据暂不可读取", nil)
			return
		}
		after = receivedAt
	} else {
		after, err = time.Parse(time.RFC3339Nano, c.Query("after"))
		if err != nil {
			httpapi.BadRequest(c, "after 须为 RFC 3339 时间")
			return
		}
	}
	if after.IsZero() {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "故障水印暂不可读取", nil)
		return
	}
	row, err := store.LatestHeartbeatAfter(c.Request.Context(), a.DB, device, after.UTC())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.OK(c, contract.HeartbeatEvidence{Found: false, AfterReceivedAt: &after})
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "心跳证据暂不可读取", nil)
		return
	}
	httpapi.OK(c, contract.HeartbeatEvidence{Found: true, AfterReceivedAt: &after, ReceivedAt: &row.ReceivedAt, Payload: string(row.Payload)})
}
