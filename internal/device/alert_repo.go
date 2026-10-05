package device

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AlertRepo 承接设备告警的同步落库与恢复判定。
type AlertRepo struct {
	DB *gorm.DB /* 所属central连接或调用方事务，不自行关闭 */
}

// LockLatestDeviceAlert 在tx下按deviceID／metric当前读并锁定最新非规则告警，返回原行、存在性或读取错误。
// 调用用例保持RR首次缺行间隙锁仲裁；冲突返回原错误，下一轮原事件重扫，不自行重试或提交。
func (s AlertRepo) LockLatestDeviceAlert(tx *gorm.DB, deviceID, metric string) (DeviceAlert, bool, error) {
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

// MarkAlertOccurrence 在tx下以row原主键／月份保存eventID与faultCode，返回写入错误，不改首次时刻或提交。
// 用例先核对最近故障的持久时刻；本方法不依据键的字符串顺序判断时序。
func (s AlertRepo) MarkAlertOccurrence(tx *gorm.DB, row DeviceAlert, eventID string, faultCode int) error {
	return tx.Table("alert_event").Where("id = ? AND created_month = ?", row.ID, row.CreatedMonth).
		Updates(map[string]any{"event_id": eventID, "value": faultCode}).Error
}

// InsertAlert 在tx下插入values所属告警列，返回写入错误，不自行提交；用例提供原事件时刻及分区月份。
func (s AlertRepo) InsertAlert(tx *gorm.DB, values map[string]any) error {
	return tx.Table("alert_event").Create(values).Error
}

// ListRecoverableAlerts 在ctx下返回active／acknowledged设备告警或读取错误，无写入，恢复证据由用例核对。
func (s AlertRepo) ListRecoverableAlerts(ctx context.Context) ([]DeviceAlert, error) {
	rows := []DeviceAlert{}
	err := s.DB.WithContext(ctx).Table("alert_event").
		Where("rule_id IS NULL AND status IN ('active','acknowledged') AND " +
			"(metric IN ('smoke','high_temperature','device_fault') OR metric LIKE 'port_fault_%')").
		Find(&rows).Error
	return rows, err
}

// AutoResolveAlert 在tx下以row原主键／月份／事件键和活动状态写入at原恢复时刻，返回写入错误，不自行提交。
// 条件未匹配是新故障已取代水印的合法无操作，不能核销期间新增故障。
func (s AlertRepo) AutoResolveAlert(tx *gorm.DB, row DeviceAlert, at time.Time) error {
	return tx.Table("alert_event").
		Where("id = ? AND created_month = ? AND event_id = ? AND status IN ('active','acknowledged')",
			row.ID, row.CreatedMonth, row.EventID).
		Updates(map[string]any{"status": "auto_resolved", "resolved_at": at}).Error
}

// DeviceAlert 是设备告警的流转所需字段。
// EventID 是最近一次故障事件的键，也是恢复判定的水印；CreatedMonth 是分区列，
// 更新时必须带上，否则会扫全部分区。
type DeviceAlert struct {
	ID           uint64     // 原告警主键
	DeviceID     string     // 原设备编号
	Metric       string     // 原协议故障维度
	EventID      string     // 最近原故障事件键
	Status       string     // 原active／acknowledged／auto_resolved状态
	CreatedAt    time.Time  // 首次故障持久时刻，不代表最近故障
	CreatedMonth time.Time  // 原分区月份
	ResolvedAt   *time.Time // 原恢复时刻，未恢复为nil
}

// ListAlerts 在ctx下按scope只读原告警公开列，返回原倒序数组或存储错误，不泄漏范围外告警。
func (s AlertRepo) ListAlerts(ctx context.Context, scope operations.DataScope) ([]map[string]any, error) {
	q := s.DB.WithContext(ctx).Table("alert_event AS a")
	if !scope.Unrestricted {
		q = q.Joins("JOIN device_meta AS d ON d.device_id=a.device_id AND d.deleted_at IS NULL")
		q = ScopeVendors(scope, ScopeStations(scope, q, "d.station_id"), "d.vendor_id")
	}
	rows := []map[string]any{}
	err := q.Select("a.id,a.device_id,a.severity,a.metric,a.status,a.created_at").Order("a.id DESC").Find(&rows).Error
	return rows, err
}

// LockAlert 在调用方tx下按id锁原告警最小流转事实，返回原行或缺失／读取错误，不提交。
func (s AlertRepo) LockAlert(tx *gorm.DB, id uint64) (DeviceAlert, error) {
	var row DeviceAlert
	err := tx.Table("alert_event").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error
	return row, err
}

// Acknowledge 在tx下把row原主键／月份标为已认领并保存actorID及at，返回写入错误，不决定权限或提交。
func (s AlertRepo) Acknowledge(tx *gorm.DB, row DeviceAlert, actorID uint64, at time.Time) error {
	return tx.Table("alert_event").Where("id=? AND created_month=?", row.ID, row.CreatedMonth).Updates(map[string]any{"status": "acknowledged", "acked_by": actorID, "acked_at": at}).Error
}
