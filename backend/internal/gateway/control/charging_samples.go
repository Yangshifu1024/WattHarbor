package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
	gatewayrepo "wattharbor/internal/gateway/repo"

	"github.com/gin-gonic/gin"
	gatewaycontract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/protocol"
)

// 每页最多返回 10080 条心跳；每分钟一条时可覆盖 7 天。
const chargingEvidenceMaxRows = 10080

// chargingEvidenceWindow 限制心跳查询最多回溯 7 天。
const chargingEvidenceWindow = 7 * 24 * time.Hour

// collectChargingSamples 按 ID 升序读取时间窗口内的心跳，只返回目标端口的读数。
// complete=false 时，使用 nextAfterID 继续读取 ID 更大的记录。
func (a DeviceEventAPI) collectChargingSamples(ctx context.Context, device string, port uint8, from time.Time, afterID uint64, limit int) ([]gatewaycontract.EvidenceSample, uint64, bool, error) {
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
		// 心跳未包含目标端口时也返回空读数，使停机判断中断连续计数。
		// 只输出目标端口，不泄露其他端口读数、卡号或原始载荷。
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

// chargingSamples 分页读取指定端口的心跳；after_id 继续读取 ID 更大的记录。
func (a DeviceEventAPI) chargingSamples(c *gin.Context) {
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

// chargingEvidence 批量读取各订单的端口心跳，减少停机扫描中的 HTTP 调用。
// 每条最多返回 10080 条记录；任一请求参数无效则整批拒绝。
func (a DeviceEventAPI) chargingEvidence(c *gin.Context) {
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
