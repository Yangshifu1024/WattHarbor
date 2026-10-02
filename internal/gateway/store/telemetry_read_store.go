package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 遥测与充电过程的读取。这里提供的是"带好默认过滤条件的查询"，调用方继续
// 追加自己的条件——把组合查询整个搬进 store 只会让 store 长成第二个控制层。
//
// 设备级指标（电压、温度）在库里 port_no 为 NULL。指定端口时这两项仍然要返回，
// 否则端口曲线会缺少电压与温度——所以过滤条件是"端口匹配 或 设备级指标"，
// 不是简单的 port_no = ?。

// Reading 是一条遥测读数。表不同、时间列名不同，统一成 ts / metric / value。
type Reading struct {
	TS     time.Time `gorm:"column:ts"`
	Metric string    `gorm:"column:metric"`
	Value  float64   `gorm:"column:value_num"`
}

// deviceLevelMetrics 是挂在设备上而不是端口上的指标。
var deviceLevelMetrics = []string{"voltage_v", "temperature_c"}

// readingOverfetch 多取几倍再在内存里降采样：原始采样率远高于曲线需要的点数，
// 直接 LIMIT 会只返回窗口尾部。
const readingOverfetch = 8

// RawReadingsQuery 返回原始遥测的读取查询。
func RawReadingsQuery(ctx context.Context, db *gorm.DB, deviceID string, portNo int, from, to time.Time, limit int) *gorm.DB {
	query := db.WithContext(ctx).Table("telemetry").
		Select("ts, metric, value_num").
		Where("device_id = ? AND ts >= ? AND ts <= ? AND value_num IS NOT NULL", deviceID, from, to)
	if portNo > 0 {
		query = query.Where("port_no = ? OR (port_no IS NULL AND metric IN ?)", portNo, deviceLevelMetrics)
	}
	return query.Order("ts DESC").Limit(limit * readingOverfetch)
}

// BucketedReadingsQuery 返回聚合表的读取查询，同样保留设备级指标。
func BucketedReadingsQuery(ctx context.Context, db *gorm.DB, table, deviceID string, portNo int, from, to time.Time, limit int) *gorm.DB {
	query := db.WithContext(ctx).Table(table).
		Select("bucket_start AS ts, metric, avg_value AS value_num").
		Where("device_id = ? AND bucket_start >= ? AND bucket_start <= ?", deviceID, from, to)
	if portNo > 0 {
		query = query.Where("port_no = ? OR (port_no IS NULL AND metric IN ?)", portNo, deviceLevelMetrics)
	}
	return query.Order("bucket_start DESC").Limit(limit * readingOverfetch)
}

// ChargeProcessQuery 返回充电过程的读取查询。功率与电量在库里是整数缩放值
// （分瓦、毫瓦时），这里换算成对外的瓦与千瓦时，调用方不必再记一次缩放。
func ChargeProcessQuery(ctx context.Context, db *gorm.DB, orderNo string, chargeOrderID uint64, deviceID string, portNo uint64, afterID uint64) *gorm.DB {
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
	return query
}

// MarkSyncEventProcessed 标记一条同步事件已处理，返回是否真的标记了。
// 条件带 processed_at IS NULL，已处理的重复标记影响 0 行——调用方据此累加
// 计数，两次标同一条会让计数虚高。
func MarkSyncEventProcessed(tx *gorm.DB, id uint64) (int64, error) {
	res := tx.Table("device_event").Where("id = ? AND processed_at IS NULL", id).
		Update("processed_at", gorm.Expr("CURRENT_TIMESTAMP(3)"))
	return res.RowsAffected, res.Error
}

// FrozenEndPayload 返回已冻结的结束回执载荷。空串且无错误表示尚未冻结。
func FrozenEndPayload(ctx context.Context, db *gorm.DB, deviceEventID uint64) (string, error) {
	var row struct {
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

// InsertFrozenEndDelivery 冻结一条结束回执。device_event_id 上有唯一键，
// 并发冻结由数据库挡下，调用方据此回落到读存量。
func InsertFrozenEndDelivery(db *gorm.DB, deviceEventID, chargeOrderID uint64, payload string) error {
	return db.Table("charge_end_delivery").Create(map[string]any{
		"device_event_id": deviceEventID, "charge_order_id": chargeOrderID, "payload_json": payload,
	}).Error
}
