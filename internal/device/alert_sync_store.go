package device

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AlertSyncStore 承接设备告警的同步落库与恢复判定。
type AlertSyncStore struct{ DB *gorm.DB }

// LockLatestDeviceAlert 在行锁下取该设备最近一条非规则告警。
// 故障是持续状态，同一设备同一指标只保留一条告警，靠它做去重。
func (s AlertSyncStore) LockLatestDeviceAlert(tx *gorm.DB, deviceID, metric string) (DeviceAlert, bool, error) {
	var row DeviceAlert
	found := tx.Table("alert_event").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND metric = ? AND rule_id IS NULL", deviceID, metric).
		Order("created_at DESC, id DESC").Take(&row)
	if errors.Is(found.Error, gorm.ErrRecordNotFound) {
		return row, false, nil
	}
	if found.Error != nil {
		return row, false, found.Error
	}
	return row, true, nil
}

// MarkAlertOccurrence 推进持续故障的告警：把事件键更新为最近一次报告。
// 恢复判定以这个键为水印，早于它的心跳不构成恢复证据。
func (s AlertSyncStore) MarkAlertOccurrence(tx *gorm.DB, row DeviceAlert, eventID string, faultCode int) error {
	return tx.Table("alert_event").Where("id = ? AND created_month = ?", row.ID, row.CreatedMonth).
		Updates(map[string]any{"event_id": eventID, "value": faultCode}).Error
}

// InsertAlert 落一条新告警。created_month 由调用方按事件时间给出：表按月分区，
// 该列没有默认值，不给会报 Field 'created_month' doesn't have a default value。
func (s AlertSyncStore) InsertAlert(tx *gorm.DB, values map[string]any) error {
	return tx.Table("alert_event").Create(values).Error
}

// ListRecoverableAlerts 返回可能已恢复但尚未核销的告警：设备类告警里
// active 与 acknowledged 两种状态都还在等恢复证据。
func (s AlertSyncStore) ListRecoverableAlerts(ctx context.Context) ([]DeviceAlert, error) {
	rows := []DeviceAlert{}
	err := s.DB.WithContext(ctx).Table("alert_event").
		Where("rule_id IS NULL AND status IN ('active','acknowledged') AND " +
			"(metric IN ('smoke','high_temperature','device_fault') OR metric LIKE 'port_fault_%')").
		Find(&rows).Error
	return rows, err
}

// AutoResolveAlert 把已确认恢复的告警标记为自动核销。
// 带上 event_id 是水印条件：期间又报了新故障的告警不该被这次心跳核销掉。
func (s AlertSyncStore) AutoResolveAlert(tx *gorm.DB, row DeviceAlert, at time.Time) error {
	return tx.Table("alert_event").
		Where("id = ? AND created_month = ? AND event_id = ? AND status IN ('active','acknowledged')",
			row.ID, row.CreatedMonth, row.EventID).
		Updates(map[string]any{"status": "auto_resolved", "resolved_at": at}).Error
}

// DeviceAlert 是设备告警的流转所需字段。
// EventID 是最近一次故障事件的键，也是恢复判定的水印；CreatedMonth 是分区列，
// 更新时必须带上，否则会扫全部分区。
type DeviceAlert struct {
	ID           uint64
	DeviceID     string
	Metric       string
	EventID      string
	Status       string
	CreatedAt    time.Time
	CreatedMonth time.Time
	ResolvedAt   *time.Time
}
