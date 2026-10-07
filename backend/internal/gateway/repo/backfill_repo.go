package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 补采的设备与端口准入、已存读数去重、原始插入和聚合刷新由本仓储完整执行；协议解析与输入预算仍由控制层负责。

// deviceStatus 接收 ctx、所属事务与 deviceID，以当前读锁定未删除设备，串行化实时写入与补传。
func deviceStatus(ctx context.Context, db *gorm.DB, deviceID string) (string, error) {
	var row struct {
		// Status 所属记录的原状态值。
		Status string
	}
	err := db.WithContext(ctx).Table("device").Clauses(clause.Locking{Strength: "UPDATE"}).Select("status").
		Where("device_id = ? AND deleted_at IS NULL", deviceID).Take(&row).Error
	return row.Status, err
}

// devicePortNumbers 接收 ctx、db 与 deviceID，读取未删除端口号；返回列表或 SQL 错误，无写入。
func devicePortNumbers(ctx context.Context, db *gorm.DB, deviceID string) ([]int16, error) {
	var rows []struct {
		// PortNo 原协议端口序号。
		PortNo int16 `gorm:"column:port_no"`
	}
	err := db.WithContext(ctx).Table("device_port").Select("port_no").
		Where("device_id = ? AND deleted_at IS NULL", deviceID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]int16, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.PortNo)
	}
	return out, nil
}

// existingTelemetryKey 是原 telemetry 去重查询的持久投影，包含可空端口、指标与原采样时刻。
type existingTelemetryKey struct {
	// PortNo 原可空协议端口，NULL 表示设备级读数。
	PortNo sql.NullInt16 `gorm:"column:port_no"`
	// Metric 原遥测指标名。
	Metric string `gorm:"column:metric"`
	// TS 数据库保存的原采样时刻，仍以毫秒键参与去重。
	TS time.Time `gorm:"column:ts"`
}

// listExistingTelemetry 接收 ctx、db、deviceID、metrics 与 from/to，读取闭区间原读数键供调用方去重；返回键及 SQL 错误，无写入。
func listExistingTelemetry(ctx context.Context, db *gorm.DB, deviceID string, metrics []string, from, to time.Time) ([]existingTelemetryKey, error) {
	rows := []existingTelemetryKey{}
	err := db.WithContext(ctx).Table("telemetry").
		Select("port_no, metric, ts").
		Where("device_id = ? AND metric IN ? AND ts >= ? AND ts <= ?", deviceID, metrics, from, to).
		Find(&rows).Error
	return rows, err
}

// telemetryInsertBatch 保留原始遥测每批五百行的 CreateInBatches 预算。
const telemetryInsertBatch = 500

// insertTelemetryBatch 接收 ctx、db 与 rows，按五百行预算插入原遥测列值，空列表返回 nil；返回 SQL／取消错误，沿用 GORM 原批插入行为，不包裹聚合刷新。
func insertTelemetryBatch(ctx context.Context, db *gorm.DB, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	return db.WithContext(ctx).Table("telemetry").CreateInBatches(rows, telemetryInsertBatch).Error
}

// backfillMetrics 限定 telemetry 支持的指标集合；未知指标在写入前拒绝，避免形成无法解释的计量。
var backfillMetrics = map[string]struct{}{
	"voltage_v": {}, "current_a": {}, "temperature_c": {},
	"battery_soc": {}, "power_w": {}, "meter_kwh": {},
}

// BackfillSample 是解析层已校验的单条补采读数，供完整持久操作核对设备、端口并写入原始遥测与聚合。
type BackfillSample struct {
	// DeviceID 原设备业务编号；完整操作的设备准入查询仍使用单独的 deviceID 参数。
	DeviceID string
	// PortNo 原可空端口号；Valid=false 表示设备级读数，非空端口须属于未删除设备端口。
	PortNo sql.NullInt16
	// Metric 原遥测指标名，解析层按 IsBackfillMetric 的原六项集合校验。
	Metric string
	// Value 原 decimal 归一后的六位小数文本，按原列映射交数据库，不经 float64 转换。
	Value string
	// TS 原采样时刻；解析层转换为 UTC，去重键仍取 UnixMilli。
	TS time.Time
}

// ErrBackfillUnknownDevice 表示补采设备不存在、已删除或已退役；保留原准入失败分类。
var ErrBackfillUnknownDevice = errors.New("backfill device is unknown or retired")

// ErrBackfillUnknownPort 表示补采非空端口不属于该设备的未删除端口；保留原端口失败分类。
var ErrBackfillUnknownPort = errors.New("backfill port does not belong to the device")

// IsBackfillMetric 接收 name，按原六项指标集合返回是否支持补采；不解析载荷、不执行 IO 或修改集合。
func IsBackfillMetric(name string) bool {
	_, ok := backfillMetrics[name]
	return ok
}

// PersistBackfill 在同一 READ COMMITTED 事务中执行准入、去重、原始插入和两类聚合。
// 设备行锁使实时写入与补传串行，重放保留首次数值；失败回滚全部写入并返回零计数。
func PersistBackfill(ctx context.Context, db *gorm.DB, deviceID string, samples []BackfillSample) (int, int, error) {
	if len(samples) == 0 {
		return 0, 0, nil
	}
	inserted, skipped := 0, 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		inserted, skipped, err = persistBackfill(ctx, tx, deviceID, samples)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, 0, err
	}
	return inserted, skipped, nil
}

// persistBackfill 在调用方事务内锁定设备、批量去重并写入，禁止单独提交原始读数。
func persistBackfill(ctx context.Context, db *gorm.DB, deviceID string, samples []BackfillSample) (int, int, error) {
	// 仅接受已开通设备及其有效端口的补传，防止写入无法关联硬件来源的历史读数。
	status, err := deviceStatus(ctx, db, deviceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, 0, ErrBackfillUnknownDevice
		}
		return 0, 0, err
	}
	// 停用设备可补传停用前缓存的读数；退役设备不接受补传。
	if status == "retired" {
		return 0, 0, ErrBackfillUnknownDevice
	}
	ports, err := devicePortNumbers(ctx, db, deviceID)
	if err != nil {
		return 0, 0, err
	}
	known := make(map[int16]struct{}, len(ports))
	for _, p := range ports {
		known[p] = struct{}{}
	}
	allowed := make([]BackfillSample, 0, len(samples))
	for _, s := range samples {
		s.DeviceID = deviceID
		s.TS = s.TS.UTC().Truncate(time.Millisecond)
		if !s.PortNo.Valid {
			allowed = append(allowed, s)
			continue
		}
		if _, ok := known[s.PortNo.Int16]; !ok {
			return 0, 0, ErrBackfillUnknownPort
		}
		allowed = append(allowed, s)
	}
	samples = allowed
	metrics := make([]string, 0, len(backfillMetrics))
	for metric := range backfillMetrics {
		metrics = append(metrics, metric)
	}
	from, to := samples[0].TS, samples[0].TS
	for _, s := range samples[1:] {
		if s.TS.Before(from) {
			from = s.TS
		}
		if s.TS.After(to) {
			to = s.TS
		}
	}
	// 批量查询现有读数，避免对最多 6000 条补传逐行查重。
	stored, err := listExistingTelemetry(ctx, db, deviceID, metrics, from, to)
	if err != nil {
		return 0, 0, err
	}
	present := make(map[string]struct{}, len(stored))
	for _, row := range stored {
		present[BackfillKey(deviceID, row.PortNo, row.Metric, row.TS)] = struct{}{}
	}
	rows := make([]map[string]any, 0, len(samples))
	newSamples := make([]AggregateSample, 0, len(samples))
	skipped := 0
	for _, s := range samples {
		if _, dup := present[BackfillKey(s.DeviceID, s.PortNo, s.Metric, s.TS)]; dup {
			skipped++
			continue
		}
		present[BackfillKey(deviceID, s.PortNo, s.Metric, s.TS)] = struct{}{}
		rows = append(rows, map[string]any{
			"device_id": s.DeviceID, "port_no": s.PortNo, "metric": s.Metric,
			"value_num": s.Value, "ts": s.TS,
		})
		newSamples = append(newSamples, AggregateSample{
			DeviceID: s.DeviceID, Port: s.PortNo, Metric: s.Metric, Value: s.Value, TS: s.TS,
		})
	}
	if len(rows) == 0 {
		return 0, skipped, nil
	}
	if err := insertTelemetryBatch(ctx, db, rows); err != nil {
		return 0, 0, err
	}
	// 原始读数与两个汇总表共同提交，聚合失败使原始插入回滚。
	if err := RefreshAggregates(ctx, db, newSamples); err != nil {
		return 0, 0, err
	}
	return len(rows), skipped, nil
}

// BackfillKey 接收 deviceID、可空 port、metric 与 ts，返回原设备、端口整数、指标和 UnixMilli 格式的去重文本。
// 原格式只取 port.Int16，不另外编码 Valid；函数不校验输入或执行 IO。
func BackfillKey(deviceID string, port sql.NullInt16, metric string, ts time.Time) string {
	return fmt.Sprintf("%s|%d|%s|%d", deviceID, port.Int16, metric, ts.UnixMilli())
}
