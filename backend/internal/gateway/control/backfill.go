package control

import (
	"chargepilot/internal/device"
	gatewayrepo "chargepilot/internal/gateway/repo"
	"chargepilot/internal/platform/httpapi"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// backfillFrame 保存一条补传的设备读数，端口为空表示设备级指标。
type backfillFrame struct {
	// DeviceID 设备业务编号。
	DeviceID string `json:"device_id"`
	// PortNo 协议端口序号。
	PortNo *int16 `json:"port_no"`
	// MsgType 补传协议消息类型。
	MsgType string `json:"msg_type"`
	// TS 采样时刻，使用该结构的时间格式。
	TS string `json:"ts"`
	// Payload 协议事件载荷。
	Payload map[string]json.RawMessage `json:"payload"`
}

// backfill 校验离线读数并批量保存，返回新增和跳过数量。
func (a TelemetryAPI) backfill(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	deviceID := c.Param("device_id")
	var in struct {
		// Frames 补传输入帧集合。
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

// parseBackfill 校验整批设备、端口、协议、时间和指标，并按毫秒去重。
// 返回非 nil 列表；任一帧无效则整批报错，不写数据库。
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

// decodeBackfillValue 将字符串或 JSON 数字转为 6 位小数文本，不经过 float64。
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
