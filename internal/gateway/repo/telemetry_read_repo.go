package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 遥测与充电过程完整读取保留原筛选、单位转换、排序和读取预算，返回记录及错误。
// 指定端口时同时保留 port_no 为 NULL 的设备级电压、温度指标。

// Reading 是一条遥测读数。表不同、时间列名不同，统一成 ts / metric / value。
type Reading struct {
	// TS 原遥测采样时刻或聚合桶起始时刻；曲线转换按 UTC 秒合并。
	TS   time.Time `gorm:"column:ts"`
	Name string    `gorm:"column:metric"`    // 查询返回的指标名，曲线按时刻和指标合并。
	Raw  []byte    `gorm:"column:value_num"` // 查询返回的十进制文本，转换前不经过浮点值。
}

// deviceLevelMetrics 是 port_no 为 NULL 的设备级指标集合。
var deviceLevelMetrics = []string{"voltage_v", "temperature_c"}

// readingOverfetch 以曲线点上限的八倍限制行数，在按秒合并前容纳同一时刻的多个指标。
const readingOverfetch = 8

// ListRawReadings 接收 ctx、db、deviceID、portNo、from/to 和 limit，以原 Find 读取闭区间非 NULL 数值。
// 保留设备级指标、ts 倒序及八倍行数预算；返回非 nil 记录列表与原 SQL 错误，不写入或新建事务。
func ListRawReadings(
	ctx context.Context,
	db *gorm.DB,
	deviceID string,
	portNo int,
	from, to time.Time,
	limit int,
) ([]Reading, error) {
	query := db.WithContext(ctx).Table("telemetry").
		Select("ts, metric, value_num").
		Where("device_id = ? AND ts >= ? AND ts <= ? AND value_num IS NOT NULL", deviceID, from, to)
	if portNo > 0 {
		query = query.Where("port_no = ? OR (port_no IS NULL AND metric IN ?)", portNo, deviceLevelMetrics)
	}
	rows := []Reading{}
	err := query.Order("ts DESC").Limit(limit * readingOverfetch).Find(&rows).Error
	return rows, err
}

// ListBucketedReadings 接收 ctx、db、所属 table、deviceID、portNo、from/to 和 limit，以原 Find 读取闭区间聚合数值。
// 保留设备级指标、bucket_start 倒序及八倍行数预算，不新增数值筛选；返回非 nil 列表与原 SQL 错误，无写入或新事务。
func ListBucketedReadings(
	ctx context.Context,
	db *gorm.DB,
	table, deviceID string,
	portNo int,
	from, to time.Time,
	limit int,
) ([]Reading, error) {
	query := db.WithContext(ctx).Table(table).
		Select("bucket_start AS ts, metric, avg_value AS value_num").
		Where("device_id = ? AND bucket_start >= ? AND bucket_start <= ?", deviceID, from, to)
	if portNo > 0 {
		query = query.Where("port_no = ? OR (port_no IS NULL AND metric IN ?)", portNo, deviceLevelMetrics)
	}
	rows := []Reading{}
	err := query.Order("bucket_start DESC").Limit(limit * readingOverfetch).Find(&rows).Error
	return rows, err
}

// ListChargeProcessPoints 接收 ctx、db、orderNo、chargeOrderID、deviceID、portNo、afterID 与 limit，以原 Scan 读取过程点。
// 保留可选身份筛选、原缩放与 NULL，按 id 升序多取一条供调用方分页；返回非 nil 列表与原 SQL 错误，无写入或新事务。
func ListChargeProcessPoints(
	ctx context.Context,
	db *gorm.DB,
	orderNo string,
	chargeOrderID uint64,
	deviceID string,
	portNo uint64,
	afterID uint64,
	limit int,
) ([]ChargeProcessPoint, error) {
	query := db.WithContext(ctx).Table("charge_process").
		Select("id,ts,power_deciwatts/10.0 AS power_w,"+
			"charged_mwh/1000000.0 AS charged_kwh,remaining_mwh/1000000.0 AS remaining_kwh,"+
			"charged_seconds,remaining_seconds,signal_strength,port_status,"+
			"voltage_v,temperature_c,device_status").
		Where("order_no=? AND id>?", orderNo, afterID)
	if chargeOrderID > 0 {
		query = query.Where("charge_order_id=?", chargeOrderID)
	}
	if deviceID != "" {
		query = query.Where("device_id=?", deviceID)
	}
	if portNo > 0 {
		query = query.Where("port_no=?", portNo)
	}
	rows := []ChargeProcessPoint{}
	err := query.Order("id ASC").Limit(limit + 1).Scan(&rows).Error
	return rows, err
}

// MarkSyncEventProcessed 接收所属 tx 与事件 id，仅更新仍未处理事件的处理时刻；返回影响行数及 SQL 错误，不自行提交。
func MarkSyncEventProcessed(tx *gorm.DB, id uint64) (int64, error) {
	res := tx.Table("device_event").Where("id = ? AND processed_at IS NULL", id).
		Update("processed_at", gorm.Expr("CURRENT_TIMESTAMP(3)"))
	return res.RowsAffected, res.Error
}

// FrozenEndPayload 接收 ctx、db 与 deviceEventID，读取原冻结结束载荷；无记录返回空串和 nil，SQL 失败返回错误，无写入。
func FrozenEndPayload(ctx context.Context, db *gorm.DB, deviceEventID uint64) (string, error) {
	var row struct {
		// PayloadJSON 原冻结结束回执 JSON 字节。
		PayloadJSON []byte `gorm:"column:payload_json"`
	}
	res := db.WithContext(ctx).Table("charge_end_delivery").
		Where("device_event_id=?", deviceEventID).Find(&row)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", nil
	}
	return string(row.PayloadJSON), nil
}

// InsertFrozenEndDelivery 接收 db、deviceEventID、chargeOrderID 与 payload，插入原冻结记录；返回唯一键或其它 SQL 错误，由调用方决定重读与事务边界。
func InsertFrozenEndDelivery(db *gorm.DB, deviceEventID, chargeOrderID uint64, payload string) error {
	return db.Table("charge_end_delivery").Create(map[string]any{
		"device_event_id": deviceEventID, "charge_order_id": chargeOrderID, "payload_json": payload,
	}).Error
}

// ChargeProcessPoint 是原充电过程查询投影，保留整数身份、原功率和电量换算以及可空协议扩展证据。
type ChargeProcessPoint struct {
	// ID charge_process 表的原自增主键，用作过程曲线游标。
	ID uint64 `json:"id"`
	// TS 原采样时刻，保留所属结构的时间表示。
	TS time.Time `json:"ts"`
	// PowerW 原转换后的功率，单位瓦。
	PowerW float64 `json:"power_w"`
	// ChargedKWh 原转换后的已充电量，单位千瓦时。
	ChargedKWh float64 `json:"charged_kwh" gorm:"column:charged_kwh"`
	// RemainingKWh 原转换后的剩余电量，单位千瓦时。
	RemainingKWh float64 `json:"remaining_kwh" gorm:"column:remaining_kwh"`
	// ChargedSeconds 原已充时长秒数。
	ChargedSeconds uint32 `json:"charged_seconds"`
	// RemainingSeconds 原剩余时长秒数。
	RemainingSeconds uint32 `json:"remaining_seconds"`
	// SignalStrength 原设备信号强度整数，零为有效报告，不具有 NULL 标志。
	SignalStrength uint8 `json:"signal_strength"`
	// PortStatus 原端口协议状态，nil 表示未提供该证据。
	PortStatus *uint8 `json:"port_status"`
	// VoltageV 原整机电压，单位伏；nil 表示未提供对应扩展证据。
	VoltageV *uint16 `json:"voltage_v"`
	// TemperatureC 原整机温度，单位摄氏度；nil 表示未提供对应扩展证据。
	TemperatureC *int16 `json:"temperature_c"`
	// DeviceStatus 原设备协议状态，保留可空扩展块。
	DeviceStatus *uint8 `json:"device_status"`
}
