package control

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	backfillMaxFrames  = 1000
	backfillMaxBody    = 1 << 20
	backfillMaxClockSk = 5 * time.Minute
	// 补传时间限定在允许的历史窗口内；超期读数拒绝写入，避免进入兜底分区。
	backfillMaxAge = 365 * 24 * time.Hour
)

// backfillFrame 承载原补传 JSON 帧的设备、可空端口、消息类型、采样时刻及指标载荷，由控制层校验。
type backfillFrame struct {
	// DeviceID 原设备业务编号。
	DeviceID string `json:"device_id"`
	// PortNo 原协议端口序号。
	PortNo *int16 `json:"port_no"`
	// MsgType 原补传协议消息类型。
	MsgType string `json:"msg_type"`
	// TS 原采样时刻，保留所属结构的时间表示。
	TS string `json:"ts"`
	// Payload 原协议事件载荷。
	Payload map[string]json.RawMessage `json:"payload"`
}

// backfill 接收 c，按原服务鉴权、帧数与正文预算解析离线遥测，再将请求 context 和样本交完整补采仓储。
// 返回原 inserted/skipped；设备、端口及存储失败按原顺序映射 404、400、503，解析失败写 400，不持有事务。
func (a TelemetryAPI) backfill(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	deviceID := c.Param("device_id")
	var in struct {
		// Frames 原补传输入帧集合。
		Frames []backfillFrame `json:"frames"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, backfillMaxBody)
	if c.ShouldBindJSON(&in) != nil || len(in.Frames) == 0 || len(in.Frames) > backfillMaxFrames {
		httpapi.BadRequest(c, "invalid backfill batch")
		return
	}
	samples, err := parseBackfill(deviceID, in.Frames, time.Now().UTC())
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	inserted, skipped, err := gatewayrepo.PersistBackfill(c.Request.Context(), a.DB, deviceID, samples)
	if errors.Is(err, gatewayrepo.ErrBackfillUnknownDevice) {
		httpapi.Write(c, 404, 1004, "设备不存在或已退役", nil)
		return
	}
	if errors.Is(err, gatewayrepo.ErrBackfillUnknownPort) {
		httpapi.BadRequest(c, "frame port_no is not a port of this device")
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "断线补传写入失败", nil)
		return
	}
	httpapi.OK(c, gin.H{"inserted": inserted, "skipped": skipped})
}

// parseBackfill 接收路径 deviceID、frames 与 now，按原设备、端口、协议、时间窗口和指标规则校验并归一整批样本。
// 返回原毫秒键去重后的非 nil 列表或任一帧错误；仅解析与纯规则调用，不访问数据库或写响应。
func parseBackfill(deviceID string, frames []backfillFrame, now time.Time) ([]gatewayrepo.BackfillSample, error) {
	if !device.ValidDeviceID(deviceID) {
		return nil, fmt.Errorf("invalid device id")
	}
	out := make([]gatewayrepo.BackfillSample, 0, len(frames))
	seen := make(map[string]struct{}, len(frames))
	for _, frame := range frames {
		if frame.DeviceID != deviceID {
			return nil, fmt.Errorf("frame device_id must match the path parameter")
		}
		if frame.MsgType != "telemetry" {
			return nil, fmt.Errorf("only telemetry frames can be backfilled")
		}
		if len(frame.Payload) == 0 {
			return nil, fmt.Errorf("frame payload is empty")
		}
		ts, err := time.Parse(time.RFC3339, frame.TS)
		if err != nil {
			return nil, fmt.Errorf("frame ts is not RFC3339")
		}
		ts = ts.UTC()
		if ts.After(now.Add(backfillMaxClockSk)) || ts.Before(now.Add(-backfillMaxAge)) {
			return nil, fmt.Errorf("frame ts is outside the accepted window")
		}
		port := sql.NullInt16{}
		if frame.PortNo != nil {
			if *frame.PortNo < 1 || *frame.PortNo > 255 {
				return nil, fmt.Errorf("frame port_no is out of range")
			}
			port = sql.NullInt16{Int16: *frame.PortNo, Valid: true}
		}
		for metric, raw := range frame.Payload {
			if !gatewayrepo.IsBackfillMetric(metric) {
				return nil, fmt.Errorf("unsupported metric %q", metric)
			}
			value, err := decodeBackfillValue(raw)
			if err != nil {
				return nil, fmt.Errorf("metric %q is not a decimal number", metric)
			}
			key := gatewayrepo.BackfillKey(deviceID, port, metric, ts)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, gatewayrepo.BackfillSample{DeviceID: deviceID, PortNo: port, Metric: metric, Value: value, TS: ts})
		}
	}
	return out, nil
}

// decodeBackfillValue 接收 raw 字符串或 JSON 数字，返回 decimal 归一的六位小数文本或原解析错误；不经 float64、不执行 IO。
func decodeBackfillValue(raw json.RawMessage) (string, error) {
	text := strings.TrimSpace(string(raw))
	if unquoted, err := strconv.Unquote(text); err == nil {
		text = strings.TrimSpace(unquoted)
	}
	value, err := decimal.NewFromString(text)
	if err != nil {
		return "", err
	}
	return value.StringFixed(6), nil
}
