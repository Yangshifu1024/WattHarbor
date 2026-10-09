package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AggregateSample 表示聚合读数，供实时遥测和断线补传共同维护汇总表。
type AggregateSample struct {
	// DeviceID 设备业务编号。
	DeviceID string
	// Port 可空采样端口；NULL 表示设备级指标，聚合保持空端口键。
	Port sql.NullInt16
	// Metric 遥测指标名。
	Metric string
	// Value 聚合采样的十进制数值文本。
	Value string
	// TS 采样时刻，聚合操作以 UTC 时间按对应桶宽归桶。
	TS time.Time
}

// refreshAggregates 依次更新 15 分钟和小时汇总；空列表直接返回，不自行提交事务。
func refreshAggregates(ctx context.Context, tx *gorm.DB, samples []AggregateSample) error {
	if len(samples) == 0 {
		return nil
	}
	for _, spec := range []struct {
		// table 内部聚合目标表名。
		table string
		// width 内部聚合桶宽。
		width time.Duration
	}{{"telemetry_aggregate_15min", 15 * time.Minute}, {"telemetry_aggregate_hourly", time.Hour}} {
		if err := upsertAggregate(ctx, tx, spec.table, spec.width, samples); err != nil {
			return err
		}
	}
	return nil
}

// RefreshAggregates 更新两类汇总；只有 db 是事务时，两次更新才共同提交或回滚。
func RefreshAggregates(ctx context.Context, tx *gorm.DB, samples []AggregateSample) error {
	return refreshAggregates(ctx, tx, samples)
}

// upsertAggregate 按 UTC 时间桶批量累加样本数、加权均值和极值，不自行提交事务。
func upsertAggregate(ctx context.Context, tx *gorm.DB, table string, width time.Duration, samples []AggregateSample) error {
	// 每种汇总粒度批量执行一条 SQL，避免 TCP 写入路径按指标逐条访问数据库。
	values := make([]string, 0, len(samples))
	args := make([]any, 0, len(samples)*9)
	for _, sample := range samples {
		bucket := sample.TS.UTC().Truncate(width)
		port := any(nil)
		if sample.Port.Valid {
			port = int16(sample.Port.Int16)
		}
		month := time.Date(bucket.Year(), bucket.Month(), 1, 0, 0, 0, 0, time.UTC)
		values = append(values, "(?,?,?,?,?,?,?,?,?)")
		args = append(args, sample.DeviceID, port, sample.Metric, bucket, month, sample.Value, sample.Value, sample.Value, 1)
	}
	statement := fmt.Sprintf(`INSERT INTO %s
		(device_id, port_no, metric, bucket_start, bucket_month, avg_value, min_value, max_value, count)
		VALUES %s
		ON DUPLICATE KEY UPDATE
		avg_value = (avg_value * count + VALUES(avg_value) * VALUES(count)) / (count + VALUES(count)),
		min_value = LEAST(min_value, VALUES(min_value)),
		max_value = GREATEST(max_value, VALUES(max_value)),
		count = count + VALUES(count)`, table, strings.Join(values, ","))
	return tx.WithContext(ctx).Exec(statement, args...).Error
}
