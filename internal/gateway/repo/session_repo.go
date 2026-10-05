package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 设备连接会话先读取候选，再按 id 与 created_month 完整复合主键关闭。
// created_month 同时用于分区定位，不能只按 id 更新；本操作不处理充电命令会话。

// IdleSessionRow 是静默阈值筛选所得的设备连接持久投影，只读取原六列，不代表 TCP 已断开。
type IdleSessionRow struct {
	// SessionID 原设备连接会话身份，不是 START 或 STOP 命令会话。
	SessionID string `gorm:"column:session_id"`
	// DeviceID 原设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// Protocol 原连接协议名称。
	Protocol string `gorm:"column:protocol"`
	// RemoteAddr 原连接远端地址。
	RemoteAddr string `gorm:"column:remote_addr"`
	// StartedAt 原设备连接开始时刻，保留数据库映射，不转换时区。
	StartedAt time.Time `gorm:"column:started_at"`
	// LastActiveAt 原设备连接最后活动时刻，作为静默阈值与列表排序依据。
	LastActiveAt time.Time `gorm:"column:last_active_at"`
}

// ListIdleSessions 接收 ctx、db、idleThreshold 与 limit，以调用时当前 UTC 减阈值筛选仍开放的设备连接记录。
// 按原六列 Scan、last_active_at 升序和 limit 返回记录及 SQL 错误，不检查内存连接或关闭实际 TCP。
func ListIdleSessions(
	ctx context.Context,
	db *gorm.DB,
	idleThreshold time.Duration,
	limit int,
) ([]IdleSessionRow, error) {
	var rows []IdleSessionRow
	err := idleSessionsQuery(ctx, db, idleThreshold).
		Select(
			"session_id",
			"device_id",
			"protocol",
			"remote_addr",
			"started_at",
			"last_active_at",
		).
		Order("last_active_at ASC").Limit(limit).Scan(&rows).Error
	return rows, err
}

// CloseIdleSessions 接收 ctx、db、idleThreshold、limit 与 at，在原完整事务中读取候选并按完整主键关闭仍开放的记录。
// 候选截止在事务内另取当前 UTC 减阈值，at 仅作关闭时刻；候选无新增排序，返回累计影响行数及错误。
// 计数仅在 err=nil 时代表提交结果，任一失败回滚；不检查内存连接、不发送充电命令或断开 TCP。
func CloseIdleSessions(
	ctx context.Context,
	db *gorm.DB,
	idleThreshold time.Duration,
	limit int,
	at time.Time,
) (int64, error) {
	// 在同一事务中读取并关闭候选会话；更新再次检查 ended_at IS NULL，避免覆盖并发正常关闭的记录。
	var closed int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []sessionKey
		if err := idleSessionsQuery(ctx, tx, idleThreshold).
			Select("id", "created_month").Limit(limit).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, key := range candidates {
			affected, err := closeAbandonedSession(tx, key, at)
			if err != nil {
				return err
			}
			closed += affected
		}
		return nil
	})

	return closed, err
}

// sessionKey 是设备连接会话的完整复合主键；CreatedMonth 同时定位所属分区。
type sessionKey struct {
	// ID device_session 表的原自增 id，与 CreatedMonth 组成完整复合主键。
	ID uint64 `gorm:"column:id"`
	// CreatedMonth 原分区日期，与记录身份共同定位所属分区。
	CreatedMonth time.Time `gorm:"column:created_month"`
}

// idleSessionsQuery 接收 ctx、db 与 idleThreshold，构造仍开放且早于当前 UTC 减阈值的设备连接会话查询；不执行查询或检查内存连接注册表。
func idleSessionsQuery(ctx context.Context, db *gorm.DB, idleThreshold time.Duration) *gorm.DB {
	return db.WithContext(ctx).Table("device_session").
		Where("ended_at IS NULL AND last_active_at < ?", time.Now().UTC().Add(-idleThreshold))
}

// closeAbandonedSession 接收所属 tx、完整 key 与 at，仅关闭原 id/created_month 定位且仍开放的设备连接会话；返回影响行数及 SQL 错误，不提交或关闭 TCP 连接。
func closeAbandonedSession(tx *gorm.DB, key sessionKey, at time.Time) (int64, error) {
	month := time.Date(key.CreatedMonth.UTC().Year(), key.CreatedMonth.UTC().Month(), key.CreatedMonth.UTC().Day(), 0, 0, 0, 0, time.UTC)
	res := tx.Table("device_session").
		Where("id = ? AND created_month = ? AND ended_at IS NULL", key.ID, month).
		Updates(map[string]any{"ended_at": at, "close_reason": "abandoned"})
	return res.RowsAffected, res.Error
}

// DeviceSummaryRow 是设备摘要的一行，联表带出厂商名。
type DeviceSummaryRow struct {
	// DeviceID 原设备业务编号。
	DeviceID string `json:"device_id" gorm:"column:device_id"`
	// VendorName 原厂商名称，读取时保留可空映射。
	VendorName *string `json:"vendor_name" gorm:"column:vendor_name"`
	// LastHeartbeatAt 原最后心跳时刻，可空。
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at" gorm:"column:last_heartbeat_at"`
	// SignalStrength 原设备信号强度，保留未知可空映射。
	SignalStrength *uint8 `json:"signal_strength" gorm:"column:signal_strength"`
	// SignalAt 原信号上报时刻，可空。
	SignalAt *time.Time `json:"signal_at" gorm:"column:signal_at"`
	// Ports 是该设备下各端口的上报状态，由 ListDevicePortStatuses 取回后回填。
	Ports []DevicePortStatusRow `json:"ports" gorm:"-"`
}

// ListDeviceSummaries 接收 ctx、db 与 deviceIDs，读取未删除设备及未删除厂商名称，保留可空遥测事实；返回记录及 SQL 错误，无写入。
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
	// DeviceID 原设备业务编号。
	DeviceID string `json:"-" gorm:"column:device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `json:"port_no" gorm:"column:port_no"`
	// StatusCode 是端口上报的协议状态码，nil 表示设备还没上报过——
	// 那是"未知"，不能当成"空闲"。
	StatusCode *uint8 `json:"status_code" gorm:"column:reported_status"`
	// StatusAt 原端口状态上报时刻，可空。
	StatusAt *time.Time `json:"status_at" gorm:"column:reported_status_at"`
}

// ListDevicePortStatuses 接收 ctx、db 与 deviceIDs，按设备和端口读取未删除端口的可空协议状态及时间；返回记录与 SQL 错误，不把未知状态变为空闲。
func ListDevicePortStatuses(ctx context.Context, db *gorm.DB, deviceIDs []string) ([]DevicePortStatusRow, error) {
	rows := []DevicePortStatusRow{}
	err := db.WithContext(ctx).Table("device_port").
		Select("device_id,port_no,reported_status,reported_status_at").
		Where("device_id IN ? AND deleted_at IS NULL", deviceIDs).
		Order("device_id,port_no").Scan(&rows).Error
	return rows, err
}
