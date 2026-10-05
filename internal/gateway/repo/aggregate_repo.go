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
	// DeviceID 原设备业务编号。
	DeviceID string
	// Port 原可空采样端口；NULL 表示设备级指标，聚合保持原空端口键。
	Port sql.NullInt16
	// Metric 原遥测指标名。
	Metric string
	// Value 原聚合采样的十进制数值文本。
	Value string
	// TS 原采样时刻，聚合操作以 UTC 时间按对应桶宽归桶。
	TS time.Time
}

// refreshAggregates 接收 ctx、所属 tx 与 samples，依次更新十五分钟和小时原聚合，空列表返回 nil；返回首次 SQL 错误，不自行提交。
func refreshAggregates(ctx context.Context, tx *gorm.DB, samples []AggregateSample) error {
	if len(samples) == 0 {
		return nil
	}
	for _, spec := range []struct {
		// table 原内部聚合目标表名。
		table string
		// width 原内部聚合桶宽。
		width time.Duration
	}{{"telemetry_aggregate_15min", 15 * time.Minute}, {"telemetry_aggregate_hourly", time.Hour}} {
		if err := upsertAggregate(ctx, tx, spec.table, spec.width, samples); err != nil {
			return err
		}
	}
	return nil
}

// RefreshAggregates 接收 ctx、db 或所属 tx 与 samples，委托原聚合操作并返回错误；仅调用方传事务时参与该事务，不额外建立跨两表原子性。
func RefreshAggregates(ctx context.Context, tx *gorm.DB, samples []AggregateSample) error {
	return refreshAggregates(ctx, tx, samples)
}

// upsertAggregate 接收 ctx、所属 tx、内部 table、width 与 samples，按 UTC 桶和原空端口键批量累加计数、加权均值及极值；返回 SQL 错误，不自行提交。
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
