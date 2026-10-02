package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 历史遥测的曲线读取。原始 telemetry 保留期短，长期查询走按粒度预聚合的
// 两张表——粒度由时间窗决定，表名与桶宽的对应关系只在这里维护，
// 免得两处各写一份、改了一处忘了另一处。

// CurveGranularity 是一条时间窗对应的聚合粒度。
type CurveGranularity struct {
	Bucket time.Duration
	Table  string
}

// CurveGranularityFor 返回调用方指定的聚合粒度对应的表。
// 粒度由接口参数给出而不是按窗口自动选：调用方知道自己要多少个点。
func CurveGranularityFor(granularity string) (CurveGranularity, bool) {
	switch granularity {
	case "15min":
		return CurveGranularity{Bucket: 15 * time.Minute, Table: "telemetry_aggregate_15min"}, true
	case "hourly":
		return CurveGranularity{Bucket: time.Hour, Table: "telemetry_aggregate_hourly"}, true
	}
	return CurveGranularity{}, false
}

// MetricPeak 是一个指标在窗口内的峰值与均值，以文本返回。
// 库中是 DECIMAL，CAST 成字符串免去调用方的精度处理。
type MetricPeak struct {
	Metric string `json:"metric"`
	Peak   string `json:"peak"`
	Avg    string `json:"average_value"`
}

// curveMetrics 是曲线摘要关心的指标。功率与温度是两条主曲线，其余指标
// 在摘要里没有展示价值。
var curveMetrics = []string{"power_w", "temperature_c"}

// MetricPeaksInRange 返回窗口内各指标的峰值与加权均值。
// 均值按 count 加权，不是简单平均——桶内样本数不同时简单平均会被空桶拉偏。
func MetricPeaksInRange(ctx context.Context, db *gorm.DB, table, deviceID string, portNo uint16, start, end time.Time) ([]MetricPeak, error) {
	rows := []MetricPeak{}
	err := db.WithContext(ctx).Table(table).
		Select("metric, CAST(MAX(max_value) AS CHAR) AS peak, "+
			"CAST(SUM(avg_value * count) / NULLIF(SUM(count), 0) AS CHAR) AS average_value").
		Where("device_id = ? AND port_no = ? AND bucket_start >= ? AND bucket_start <= ? AND metric IN ?",
			deviceID, portNo, start, end, curveMetrics).
		Group("metric").Find(&rows).Error
	return rows, err
}

// HeartbeatSample 是一条心跳事件的原始载荷。
type HeartbeatSample struct {
	ID        uint64 `gorm:"column:id"`
	EventJSON []byte `gorm:"column:event_json"`
}

// ListHeartbeatsInWindow 返回设备在 [from, to] 窗口内的心跳证据，按 id 升序。
// afterID 大于 0 时只取更大的 id，用于游标翻页；多取一条（limit+1）由调用方
// 据此判断窗口内是否还有下一页。
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
