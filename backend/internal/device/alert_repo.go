package device

import (
	"chargepilot/internal/domain/operations"
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AlertRepo 保存设备告警、读取待恢复告警并更新状态；恢复条件由用例检查。
type AlertRepo struct {
	DB *gorm.DB /* 所属 central 连接或调用方事务，不自行关闭 */
}

// LockLatestDeviceAlert 锁读设备和指标对应的最新非规则告警。
// 首次缺行仍使用 RR 间隙锁控制并发，冲突交给下一轮事件重扫，不在此重试。
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

// MarkAlertOccurrence 按告警主键和分区月份更新最新故障事件、故障码，保留首次时间。
// 用例按网关事件时间判断先后，不能按事件键字符串排序。
func (s AlertRepo) MarkAlertOccurrence(tx *gorm.DB, row DeviceAlert, eventID string, faultCode int) error {
	return tx.Table("alert_event").Where("id = ? AND created_month = ?", row.ID, row.CreatedMonth).
		Updates(map[string]any{"event_id": eventID, "value": faultCode}).Error
}

// InsertAlert 在调用方事务中保存告警，用例提供事件时间和分区月份。
func (s AlertRepo) InsertAlert(tx *gorm.DB, values map[string]any) error {
	return tx.Table("alert_event").Create(values).Error
}

// ListRecoverableAlerts 读取 active 或 acknowledged 告警，是否恢复由用例核对后续心跳。
func (s AlertRepo) ListRecoverableAlerts(ctx context.Context) ([]DeviceAlert, error) {
	rows := []DeviceAlert{}
	err := s.DB.WithContext(ctx).Table("alert_event").
		Where("rule_id IS NULL AND status IN ('active','acknowledged') AND " +
			"(metric IN ('smoke','high_temperature','device_fault') OR metric LIKE 'port_fault_%')").
		Find(&rows).Error
	return rows, err
}

// AutoResolveAlert 按主键、分区月份和最新事件键写恢复时间。
// 事件键已改变时不更新，避免用旧心跳清除新故障。
func (s AlertRepo) AutoResolveAlert(tx *gorm.DB, row DeviceAlert, at time.Time) error {
	return tx.Table("alert_event").
		Where("id = ? AND created_month = ? AND event_id = ? AND status IN ('active','acknowledged')",
			row.ID, row.CreatedMonth, row.EventID).
		Updates(map[string]any{"status": "auto_resolved", "resolved_at": at}).Error
}

// DeviceAlert 保存告警状态、最新故障键和分区月份。
// EventID 标记最新故障；更新须带 CreatedMonth，避免扫描全部分区。
type DeviceAlert struct {
	ID           uint64     // 告警主键
	DeviceID     string     // 设备编号
	Metric       string     // 协议故障维度
	EventID      string     // 最近故障事件键
	Status       string     // active／acknowledged／auto_resolved 状态
	CreatedAt    time.Time  // 首次故障持久时刻，不代表最近故障
	CreatedMonth time.Time  // 首次分区月份
	ResolvedAt   *time.Time // 恢复时刻，未恢复为 nil
}

// ListAlerts 倒序读取当前范围内的告警展示字段。
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

// LockAlert 在调用方事务中锁定告警并读取状态变更所需字段。
func (s AlertRepo) LockAlert(tx *gorm.DB, id uint64) (DeviceAlert, error) {
	var row DeviceAlert
	err := tx.Table("alert_event").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error
	return row, err
}

// Acknowledge 按主键和分区月份保存已认领状态、处理人和时间，权限由用例核对。
func (s AlertRepo) Acknowledge(tx *gorm.DB, row DeviceAlert, actorID uint64, at time.Time) error {
	return tx.Table("alert_event").Where("id=? AND created_month=?", row.ID, row.CreatedMonth).Updates(map[string]any{"status": "acknowledged", "acked_by": actorID, "acked_at": at}).Error
}
