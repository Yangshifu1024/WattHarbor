package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/protocol"
	"chargepilot/internal/protocol/dc589"
	"gorm.io/gorm"
)

// AlertEvidence 只提供告警所需的网关查询和确认方法，不暴露数据库、URL 或令牌。
type AlertEvidence interface {
	// DeviceFaults 读取有数量上限的待处理故障，不修改处理状态。
	DeviceFaults(ctx context.Context) ([]contract.DeviceFaultReport, error)
	// MarkDeviceFaults 确认指定事件已处理，返回首次标记数；重放可返回 0。
	MarkDeviceFaults(ctx context.Context, ids []uint64) (int64, error)
	// HeartbeatAfterKey 读取指定故障键和后续同设备心跳，缺失时不补造数据。
	HeartbeatAfterKey(ctx context.Context, deviceID, key string) (contract.HeartbeatAfterKeyReply, error)
}

// AlertSyncService 负责中央告警同步和恢复，通过专用网关接口跨库读取，协议解释由 dc589 负责。
type AlertSyncService struct {
	DB      *gorm.DB      // 所属 central 连接，生命周期由进程管理
	Gateway AlertEvidence // cmd 注入的网关故障、心跳查询和处理确认接口
}

// Run 在最多 25 秒内顺序处理 100 条故障，提交成功后确认事件，再核对恢复。
// 返回新增告警数和失败，单条失败继续其他事件，取消后停止后续处理。
func (s AlertSyncService) Run(ctx context.Context) (int, error) {
	if s.DB == nil || s.Gateway == nil {
		return 0, errors.New("device alert sync is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	reports, err := s.Gateway.DeviceFaults(ctx)
	if err != nil {
		return 0, err
	}
	raised := 0
	processed := make([]uint64, 0, len(reports))
	var firstError error
	for _, report := range reports {
		if err := ctx.Err(); err != nil {
			return raised, errors.Join(firstError, err)
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(report.Payload), &event); err != nil {
			if firstError == nil {
				firstError = fmt.Errorf("decode fault %d: %w", report.ID, err)
			}
			continue
		}
		changed, err := s.recordFault(ctx, report, event)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		processed = append(processed, report.ID)
		if changed {
			raised++
		}
	}
	if len(processed) > 0 {
		if _, err := s.Gateway.MarkDeviceFaults(ctx, processed); err != nil {
			firstError = errors.Join(firstError, err)
		}
	}
	return raised, errors.Join(firstError, s.resolveRecovered(ctx))
}

// recordFault 核对事件 ID、设备、毫秒时间和协议内容，再锁定设备对应告警并保存。
// 同事件、早于最新故障或不晚于恢复时间时不新增；最新事件时间未知时不确认处理。
func (s AlertSyncService) recordFault(ctx context.Context, report contract.DeviceFaultReport, event protocol.Event) (bool, error) {
	if report.ID == 0 || report.EventKey == "" || len(report.EventKey) > 64 || report.DeviceID == "" || len(report.DeviceID) > 64 || report.DeviceID != event.DeviceID || report.ReceivedAt.IsZero() || event.ReceivedAt.IsZero() || event.ReceivedAt.Sub(report.ReceivedAt).Abs() >= time.Millisecond || event.Protocol != "dc589" || event.Type != protocol.Fault {
		return false, errors.New("invalid device fault evidence")
	}
	if event.FaultCode == 0 {
		return false, nil
	}
	metric, severity, note := dc589.FaultMetric(event)
	raised := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := AlertRepo{DB: tx}
		existing, found, err := repo.LockLatestDeviceAlert(tx, event.DeviceID, metric)
		if err != nil {
			return err
		}
		if found {
			if existing.EventID == report.EventKey || report.ReceivedAt.Before(existing.CreatedAt) || existing.ResolvedAt != nil && !report.ReceivedAt.After(*existing.ResolvedAt) {
				return nil
			}
			// 最新故障时间从网关读取，不能用首次告警时间或中央处理时间代替。
			watermark, err := s.Gateway.HeartbeatAfterKey(ctx, existing.DeviceID, existing.EventID)
			if err != nil {
				return err
			}
			if watermark.AfterReceivedAt == nil {
				return errors.New("original fault watermark is unavailable")
			}
			if !report.ReceivedAt.After(*watermark.AfterReceivedAt) {
				return nil
			}
			if existing.Status == devices.AlertActive || existing.Status == devices.AlertAcknowledged {
				return repo.MarkAlertOccurrence(tx, existing, report.EventKey, int(event.FaultCode))
			}
		}
		at := report.ReceivedAt.UTC()
		month := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		if err := repo.InsertAlert(tx, map[string]any{"device_id": event.DeviceID, "metric": metric, "severity": severity, "value": event.FaultCode, "status": devices.AlertActive, "note": note, "event_id": report.EventKey, "created_at": at, "created_month": month}); err != nil {
			return err
		}
		raised = true
		return nil
	})
	return raised, err
}

// resolveRecovered 用最新故障之后的同设备完整心跳判断恢复；缺故障时间、心跳或端口状态均不恢复。
// 更新带最新事件键，防止期间出现的新故障被覆盖；逐条继续检查并返回首个错误。
func (s AlertSyncService) resolveRecovered(ctx context.Context) error {
	repo := AlertRepo{DB: s.DB}
	active, err := repo.ListRecoverableAlerts(ctx)
	if err != nil {
		return err
	}
	var firstError error
	for _, alert := range active {
		if err := ctx.Err(); err != nil {
			return errors.Join(firstError, err)
		}
		hb, err := s.Gateway.HeartbeatAfterKey(ctx, alert.DeviceID, alert.EventID)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		if !hb.Found {
			continue
		}
		event, decodeErr := decodeRecoveryPayload(hb.Payload, alert.Metric)
		err = decodeErr
		if err == nil && (hb.AfterReceivedAt == nil || hb.ReceivedAt == nil || !hb.ReceivedAt.After(*hb.AfterReceivedAt) || event.DeviceID != alert.DeviceID || event.Type != protocol.Heartbeat || event.Protocol != "dc589" || event.ReceivedAt.IsZero() || event.ReceivedAt.Sub(*hb.ReceivedAt).Abs() >= time.Millisecond) {
			err = errors.New("invalid device recovery evidence")
		}
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		if !dc589.FaultRecovered(alert.Metric, event) {
			continue
		}
		if err := repo.AutoResolveAlert(s.DB.WithContext(ctx), alert, *hb.ReceivedAt); err != nil && firstError == nil {
			firstError = err
		}
	}
	return firstError
}

// decodeRecoveryPayload 解码心跳并检查恢复所需字段。
// 设备状态缺失或 null 不能视为正常，端口数组中的 null 不能视为空闲；nil 表示未收到端口状态。
func decodeRecoveryPayload(payload, metric string) (protocol.Event, error) {
	var event protocol.Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return event, err
	}
	var required struct {
		DeviceStatus *uint8          // 设备状态的显式存在性，0合法
		PortStates   json.RawMessage // []byte 为 base64 字符串；兼容数值数组时逐项验证 null
	}
	if err := json.Unmarshal([]byte(payload), &required); err != nil {
		return event, err
	}
	if (metric == "smoke" || metric == "high_temperature" || metric == "device_fault") && required.DeviceStatus == nil {
		return event, errors.New("device recovery status is absent")
	}
	if bytes.HasPrefix(bytes.TrimSpace(required.PortStates), []byte("[")) {
		var states []*uint8
		if err := json.Unmarshal(required.PortStates, &states); err != nil {
			return event, err
		}
		for _, state := range states {
			if state == nil {
				return event, errors.New("port recovery status is absent")
			}
		}
	}
	return event, nil
}
