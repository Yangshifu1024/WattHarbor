package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"gorm.io/gorm"
)

// AlertEvidence 是告警所属用例需要的网关窄能力，不暴露数据库、URL或服务令牌。
type AlertEvidence interface {
	// DeviceFaults 在ctx下读取原有界待处理故障，返回原证据或调用错误，不主动标记。
	DeviceFaults(ctx context.Context) ([]contract.DeviceFaultReport, error)
	// MarkDeviceFaults 在ctx下确认ids原事件已成功处理，返回实际首次标记数或调用错误，0允许重放。
	MarkDeviceFaults(ctx context.Context, ids []uint64) (int64, error)
	// HeartbeatAfterKey 在ctx下按deviceID与key读取原水印和后续心跳；缺失不补零，返回证据或错误。
	HeartbeatAfterKey(ctx context.Context, deviceID, key string) (contract.HeartbeatEvidence, error)
}

// AlertSyncService 拥有central设备告警同步与恢复，协议翻译归dc589，跨库仅经具名能力。
type AlertSyncService struct {
	DB      *gorm.DB      // 所属central连接，生命周期由进程管理
	Gateway AlertEvidence // cmd装配的具名gateway证据能力
}

// Run 在ctx下最多25秒顺序处理原100条故障，成功提交后标记原事件，再核对恢复证据。
// 返回已新建告警数及局部失败；单条失败不阻塞其余事件，取消不开始后续工作，无后台任务或自动重试。
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

// recordFault 在ctx下核对report原主键／设备／毫秒时刻与event协议事实，再锁所属维度并写入。
// 相同原键、早于当前故障水印或不晚于已恢复时刻无新告警；未知原水印返回错误且不标记处理，不推断恢复。
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
			// 原最近键的持久时刻来自gateway，不用首次created_at或中央处理墙钟代替。
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
			if existing.Status == "active" || existing.Status == "acknowledged" {
				return repo.MarkAlertOccurrence(tx, existing, report.EventKey, int(event.FaultCode))
			}
		}
		at := report.ReceivedAt.UTC()
		month := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		if err := repo.InsertAlert(tx, map[string]any{"device_id": event.DeviceID, "metric": metric, "severity": severity, "value": event.FaultCode, "status": "active", "note": note, "event_id": report.EventKey, "created_at": at, "created_month": month}); err != nil {
			return err
		}
		raised = true
		return nil
	})
	return raised, err
}

// resolveRecovered 在ctx下逐条核对原最新键之后的同设备完整dc589心跳，返回首个错误并继续其它独立告警。
// 无水印／无心跳／无端口状态不恢复；核销带原键条件，期间新增故障不会被旧证据覆盖，无人工审计或资金写入。
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

// decodeRecoveryPayload 把payload原JSON解码为协议事件，并核对metric恢复所需字段的存在性。
// 设备状态缺失或null不能当正常零值，端口数组中的null不能当空闲；完整nil端口仍由协议规则表达无证据。
// 返回事件或解码／缺字段错误，无IO或状态改变。
func decodeRecoveryPayload(payload, metric string) (protocol.Event, error) {
	var event protocol.Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return event, err
	}
	var required struct {
		DeviceStatus *uint8          // 原设备状态的显式存在性，0合法
		PortStates   json.RawMessage // 原[]byte为base64字符串；兼容数值数组时逐项验证null
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
