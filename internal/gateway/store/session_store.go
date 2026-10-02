package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 设备会话的空闲回收。回收分两步：先读出候选，再用 id + 分区月份逐条关闭。
// 分两步不是为了事务好看，而是因为 device_session 按 created_month 分区——
// 读的时候必须带上分区月份，回去更新时也必须用同一对键，只按 id 更新的话
// 扫全部分区且可能命中同名 id。

// SessionKey 是一条会话的定位键。CreatedMonth 是分区列，不是主键的一部分。
type SessionKey struct {
	ID           uint64    `gorm:"column:id"`
	CreatedMonth time.Time `gorm:"column:created_month"`
}

// IdleSessionsQuery 返回"超过静默阈值且仍未结束"的会话查询。
// ended_at IS NULL 是硬条件：已经正常关闭的记录一律不碰，哪怕它很久没活动。
func IdleSessionsQuery(ctx context.Context, db *gorm.DB, idleThreshold time.Duration) *gorm.DB {
	return db.WithContext(ctx).Table("device_session").
		Where("ended_at IS NULL AND last_active_at < ?", time.Now().UTC().Add(-idleThreshold))
}

// CloseAbandonedSession 关闭一条仍开放的会话，返回是否真的关掉了。
// 返回 0 表示并发中已被正常关闭——那不是错误，调用方跳过即可。
func CloseAbandonedSession(tx *gorm.DB, key SessionKey, at time.Time) (int64, error) {
	month := time.Date(key.CreatedMonth.UTC().Year(), key.CreatedMonth.UTC().Month(), key.CreatedMonth.UTC().Day(), 0, 0, 0, 0, time.UTC)
	res := tx.Table("device_session").
		Where("id = ? AND created_month = ? AND ended_at IS NULL", key.ID, month).
		Updates(map[string]any{"ended_at": at, "close_reason": "abandoned"})
	return res.RowsAffected, res.Error
}

// DeviceSummaryRow 是设备摘要的一行，联表带出厂商名。
type DeviceSummaryRow struct {
	DeviceID        string     `json:"device_id" gorm:"column:device_id"`
	VendorName      *string    `json:"vendor_name" gorm:"column:vendor_name"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at" gorm:"column:last_heartbeat_at"`
	SignalStrength  *uint8     `json:"signal_strength" gorm:"column:signal_strength"`
	SignalAt        *time.Time `json:"signal_at" gorm:"column:signal_at"`
	// Ports 是该设备下各端口的上报状态，由 ListDevicePortStatuses 取回后回填。
	Ports []DevicePortStatusRow `json:"ports" gorm:"-"`
}

// ListDeviceSummaries 批量取设备摘要。厂商已软删的设备厂商名为空而不是整个设备消失。
func ListDeviceSummaries(ctx context.Context, db *gorm.DB, deviceIDs []string) ([]DeviceSummaryRow, error) {
	rows := []DeviceSummaryRow{}
	err := db.WithContext(ctx).Table("device d").
		Select("d.device_id,v.vendor_name,d.last_heartbeat_at,d.signal_strength,d.signal_at").
		Joins("LEFT JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL").
		Where("d.device_id IN ? AND d.deleted_at IS NULL", deviceIDs).Scan(&rows).Error
	return rows, err
}

// DevicePortStatusRow 是设备一个端口的上报状态。
type DevicePortStatusRow struct {
	DeviceID string `json:"-" gorm:"column:device_id"`
	PortNo   uint8  `json:"port_no" gorm:"column:port_no"`
	// StatusCode 是端口上报的协议状态码，nil 表示设备还没上报过——
	// 那是"未知"，不能当成"空闲"。
	StatusCode *uint8     `json:"status_code" gorm:"column:reported_status"`
	StatusAt   *time.Time `json:"status_at" gorm:"column:reported_status_at"`
}

// ListDevicePortStatuses 批量取端口上报状态，按设备与端口序排列便于前端直接铺开。
// reported_status 为空表示设备还没上报过，不等于"空闲"。
func ListDevicePortStatuses(ctx context.Context, db *gorm.DB, deviceIDs []string) ([]DevicePortStatusRow, error) {
	rows := []DevicePortStatusRow{}
	err := db.WithContext(ctx).Table("device_port").
		Select("device_id,port_no,reported_status,reported_status_at").
		Where("device_id IN ? AND deleted_at IS NULL", deviceIDs).
		Order("device_id,port_no").Scan(&rows).Error
	return rows, err
}
