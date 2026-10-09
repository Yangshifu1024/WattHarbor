package repo

import (
	"context"
	"time"

	"chargepilot/internal/platform/dbutil"

	"gorm.io/gorm"
)

// 历史曲线按接口粒度选择聚合表，表名与桶宽在本包对应。

// CurveGranularity 是一条时间窗对应的聚合粒度。
type CurveGranularity struct {
	// Bucket 聚合桶宽。
	Bucket time.Duration
	// Table 聚合表名。
	Table string
}

// CurveGranularityFor 仅支持 15min 和 hourly，返回对应桶宽和表名；其他值返回 false。
func CurveGranularityFor(granularity string) (CurveGranularity, bool) {
	switch granularity {
	case "15min":
		return CurveGranularity{Bucket: 15 * time.Minute, Table: "telemetry_aggregate_15min"}, true
	case "hourly":
		return CurveGranularity{Bucket: time.Hour, Table: "telemetry_aggregate_hourly"}, true
	}
	return CurveGranularity{}, false
}

// MetricPeak 是窗口指标摘要；DECIMAL 以文本读取，避免浮点转换改变公开精度。
type MetricPeak struct {
	Metric string `gorm:"column:metric" json:"metric"`               // power_w 或 temperature_c 指标
	Peak   string `gorm:"column:peak" json:"peak"`                   // 窗口峰值 DECIMAL 文本
	Avg    string `gorm:"column:average_value" json:"average_value"` // 按原样本 count 加权的均值文本，不按结构体名称推导列
}

// curveMetrics 是曲线摘要关心的指标。功率与温度是两条主曲线，其余指标
// 在摘要里没有展示价值。
var curveMetrics = []string{"power_w", "temperature_c"}

// MetricPeaksInRange 查询时间闭区间内的功率、温度峰值和按样本数加权的均值，结果保留十进制文本。
func MetricPeaksInRange(ctx context.Context, db *gorm.DB, table, deviceID string, portNo uint16, start, end time.Time) ([]MetricPeak, error) {
	rows := []MetricPeak{}
	err := db.WithContext(ctx).Table(table).
		Select("metric, CAST(MAX(max_value) AS CHAR) AS peak, "+
			"CAST(SUM(avg_value * count) / NULLIF(SUM(count), 0) AS CHAR) AS average_value").
		Where("device_id = ? AND port_no = ? AND bucket_start >= ? AND bucket_start <= ? AND metric IN ?",
			deviceID, portNo, start, end, curveMetrics).
		Where("bucket_month >= ? AND bucket_month <= ?", dbutil.MonthStartAt(start), dbutil.MonthStartAt(end)).
		Group("metric").Find(&rows).Error
	return rows, err
}

// HeartbeatSample 是一条心跳事件的原始载荷。
type HeartbeatSample struct {
	// ID device_event 表的自增主键，用作心跳取证游标。
	ID uint64 `gorm:"column:id"`
	// EventJSON 持久协议事件 JSON 字节。
	EventJSON []byte `gorm:"column:event_json"`
}

// ListHeartbeatsInWindow 按 ID 升序查询时间闭区间内的心跳，并多取一条判断是否还有下一页。
func ListHeartbeatsInWindow(ctx context.Context, db *gorm.DB, device string, from, to time.Time, afterID uint64, limit int) ([]HeartbeatSample, error) {
	query := db.WithContext(ctx).Table("device_event").Select("id, event_json").
		Where("device_id=? AND event_type='heartbeat' AND received_at>=? AND received_at<=?", device, from.UTC(), to.UTC())
	if afterID > 0 {
		query = query.Where("id > ?", afterID)
	}
	rows := []HeartbeatSample{}
	err := query.Order("id ASC").Limit(limit + 1).Find(&rows).Error
	return rows, err
}
