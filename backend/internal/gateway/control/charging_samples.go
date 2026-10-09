package control

import (
	gatewayrepo "chargepilot/internal/gateway/repo"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/protocol"
	"github.com/gin-gonic/gin"
)

// 停机判定取证的上界：窗口内单设备单端口最多回传的心跳证据行数
// （每分钟一条可覆盖 7 天）。批量端点按条数乘该上界界定响应体积。
const chargingEvidenceMaxRows = 10080

// chargingEvidenceWindow 是最远可回溯的取证窗口，与分区保留策略对齐。
const chargingEvidenceWindow = 7 * 24 * time.Hour

// collectChargingSamples 读取设备+端口在 [from, 当前] 窗口内的心跳证据，
// 按 id 升序返回。afterID > 0 时只取 id 更大的行，用于游标翻页；
// complete 为真表示窗口内证据已取完，为假时调用方应以返回的
// nextAfterID 继续取下一段。单段行数上限 chargingEvidenceMaxRows。
func (a TelemetryAPI) collectChargingSamples(ctx context.Context, device string, port uint8, from time.Time, afterID uint64, limit int) ([]gatewaycontract.EvidenceSample, uint64, bool, error) {
	now := time.Now().UTC()
	rows, err := gatewayrepo.ListHeartbeatsInWindow(ctx, a.DB, device, from, now, afterID, limit)
	if err != nil {
		return nil, 0, false, err
	}
	complete := len(rows) <= limit
	if !complete {
		rows = rows[:limit]
	}
	samples := make([]gatewaycontract.EvidenceSample, 0, len(rows))
	var lastID uint64
	for _, row := range rows {
		lastID = row.ID
		var e protocol.Event
		if err := json.Unmarshal(row.EventJSON, &e); err != nil {
			return nil, 0, false, err
		}
		if e.DeviceID != device || e.Type != protocol.Heartbeat {
			continue
		}
		// 即使本次心跳缺失目标端口也保留事件，使调用方能中断连续证据。
		// 只输出目标端口，不泄露其他端口遥测、卡号或原始载荷。
		sample := gatewaycontract.EvidenceSample{Type: protocol.Heartbeat, DeviceID: device, ReceivedAt: e.ReceivedAt, ChargingPorts: []protocol.PortTelemetry{}}
		for _, p := range e.ChargingPorts {
			if p.Port == port {
				sample.ChargingPorts = append(sample.ChargingPorts, p)
				break
			}
		}
		samples = append(samples, sample)
	}
	var nextAfterID uint64
	if !complete {
		nextAfterID = lastID
	}
	return samples, nextAfterID, complete, nil
}

// chargingSamples 返回指定端口的有限条心跳，不含原始载荷和卡号。
// after_id 为游标：传入上一段返回的 next_after_id 可续取更早的证据。
func (a TelemetryAPI) chargingSamples(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Param("device_id")
	port, err := strconv.ParseUint(c.Query("port_no"), 10, 8)
	if err != nil || port == 0 || len(device) == 0 || len(device) > 64 {
		httpapi.BadRequest(c, "设备或端口无效")
		return
	}
	from, err := time.Parse(time.RFC3339Nano, c.Query("started_at"))
	now := time.Now().UTC()
	if err != nil || from.After(now) || now.Sub(from) > chargingEvidenceWindow {
		httpapi.BadRequest(c, "计量时间窗无效")
		return
	}
	var afterID uint64
	if raw := c.Query("after_id"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			httpapi.BadRequest(c, "after_id 无效")
			return
		}
		afterID = parsed
	}
	samples, nextAfterID, complete, err := a.collectChargingSamples(c.Request.Context(), device, uint8(port), from, afterID, chargingEvidenceMaxRows)
	if err != nil {
		httpapi.Write(c, 503, 5003, "计量证据暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"samples": samples, "complete": complete, "next_after_id": nextAfterID})
}

// chargingEvidence 一次为多个设备+端口取证，供停机判定循环批量拉取，
// 避免每订单一次 HTTP 调用。单条上限与 GET 相同，条目数上限
// gatewaycontract.MaxEvidenceBatch；任一请求体无效则整体拒绝。
func (a TelemetryAPI) chargingEvidence(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var body gatewaycontract.EvidenceRequest
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Requests) == 0 || len(body.Requests) > gatewaycontract.MaxEvidenceBatch {
		httpapi.BadRequest(c, "批量证据请求须为 1–20 条")
		return
	}
	now := time.Now().UTC()
	for i, req := range body.Requests {
		if req.DeviceID == "" || len(req.DeviceID) > 64 || req.PortNo == 0 || req.StartedAt.IsZero() || req.StartedAt.After(now) || now.Sub(req.StartedAt) > chargingEvidenceWindow {
			httpapi.BadRequest(c, fmt.Sprintf("第 %d 条证据请求无效", i+1))
			return
		}
	}
	evidence := make([]gatewaycontract.EvidencePageReply, 0, len(body.Requests))
	for _, req := range body.Requests {
		samples, nextAfterID, complete, err := a.collectChargingSamples(c.Request.Context(), req.DeviceID, req.PortNo, req.StartedAt, req.AfterID, chargingEvidenceMaxRows)
		if err != nil {
			httpapi.Write(c, 503, 5003, "计量证据暂不可读取", nil)
			return
		}
		evidence = append(evidence, gatewaycontract.EvidencePageReply{DeviceID: req.DeviceID, PortNo: req.PortNo, Samples: samples, Complete: complete, NextAfterID: nextAfterID})
	}
	httpapi.OK(c, gatewaycontract.EvidenceReply{Evidence: evidence})
}
