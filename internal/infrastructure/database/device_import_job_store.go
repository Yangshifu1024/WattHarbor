package database

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 设备批量导入的读写。受理申请与执行导入是两段：先把设备清单按幂等键存档，
// 再调网关开通，最后补建设备元数据。存档的清单同时是重试的唯一数据源。
//
// 三张表：device_import 是任务与状态；device_import_identity 记录每个设备编号
// 首次登记时的内容快照，用来拒绝同一编号被改成不同内容；device_meta 是开通
// 后的设备主数据。

// DeviceImportStore 是批量导入的读写入口。
type DeviceImportStore struct{ DB *gorm.DB }

// ListImportJobs 返回最近的导入任务，最多 limit 条，按创建时间倒序。
// 列表页看不到设备明细——原始清单只存库不外发。
func (s DeviceImportStore) ListImportJobs(ctx context.Context, limit int) ([]ImportJob, error) {
	rows := []ImportJob{}
	err := s.DB.WithContext(ctx).Table("device_import").Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ErrImportConflict 表示同一导入号或设备编号被用于不同的内容，或设备已归属
// 别的站点与厂商。处理器据此返回 409。
var ErrImportConflict = errors.New("导入内容与既有记录不一致")

// LockImportJob 在行锁下取一个导入任务。执行导入时全程持锁：崩溃会连同
// 设备元数据一起回滚，重放则能看到网关那边已经持久占用的设备身份。
func (s DeviceImportStore) LockImportJob(tx *gorm.DB, importID string) (ImportJob, error) {
	var row ImportJob
	err := tx.Table("device_import").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("import_id=?", importID).Take(&row).Error
	return row, err
}

// CreateImportJob 落一条 pending 任务，存档设备清单原文。
func (s DeviceImportStore) CreateImportJob(tx *gorm.DB, importID string, actorID uint64, requestJSON string) error {
	return tx.Table("device_import").Create(map[string]any{
		"import_id": importID, "actor_id": actorID, "request_json": requestJSON, "status": "pending",
	}).Error
}

// FindActiveStation 确认站点存在、未删除且处于 active。
func (s DeviceImportStore) FindActiveStation(tx *gorm.DB, stationID uint64) (Station, error) {
	var row Station
	err := tx.Where("id=? AND deleted_at IS NULL AND status='active'", stationID).Take(&row).Error
	return row, err
}

// RegisterDeviceIdentity 登记设备编号与其内容快照。
//
// 已登记的编号不会被覆盖：INSERT IGNORE 命中既有行时保持原快照，随后由
// AssertDeviceIdentity 比对。这样同一编号改了内容会在这里被拒，而不是
// 悄悄改写历史归属。
func (s DeviceImportStore) RegisterDeviceIdentity(tx *gorm.DB, deviceID, requestJSON string) error {
	return tx.Exec("INSERT IGNORE INTO device_import_identity(device_id,request_json) VALUES (?,?)", deviceID, requestJSON).Error
}

// AssertDeviceIdentity 取出该编号首次登记的快照并与当前内容比对。
// 不一致时返回 ErrImportConflict：设备编号是全局唯一的，不能被改写成另一个设备。
func (s DeviceImportStore) AssertDeviceIdentity(tx *gorm.DB, deviceID string, want ImportDevice) error {
	var identity struct{ RequestJSON string }
	if err := tx.Table("device_import_identity").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id=?", deviceID).Take(&identity).Error; err != nil {
		return err
	}
	var registered ImportDevice
	// 快照解析不出来说明库里的内容已损坏，此时不能当成"一致"放行。
	if json.Unmarshal([]byte(identity.RequestJSON), &registered) != nil {
		return ErrImportConflict
	}
	got, _ := json.Marshal(registered)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		return ErrImportConflict
	}
	return nil
}

// ImportedDevice 是已登记设备的主键与归属，用于判断能否覆盖。
type ImportedDevice struct {
	ID        uint64
	VendorID  uint64  // 已登记的厂商
	StationID uint64  // 已登记的站点
	Model     *string // 已登记的型号，重新导入时不覆盖
}

// LockImportedDevice 在行锁下取已登记的设备。取不到返回 gorm.ErrRecordNotFound，
// 调用方据此走"新建"分支。
func (s DeviceImportStore) LockImportedDevice(tx *gorm.DB, deviceID string) (ImportedDevice, error) {
	var row ImportedDevice
	err := tx.Table("device_meta").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id=?", deviceID).Take(&row).Error
	return row, err
}

// CreateImportedDevice 建一条设备主数据。重复导入不得以缺省字段覆盖已有设备
// 的能力或计费设置，所以调用方只在确认设备不存在时才调这里。
func (s DeviceImportStore) CreateImportedDevice(tx *gorm.DB, d ImportDevice) error {
	row := map[string]any{
		"device_id": d.DeviceID, "vendor_id": d.VendorID, "station_id": d.StationID,
		"model": d.Model, "status": "enabled", "protocol_adapter": d.ProtocolAdapter,
	}
	// 计费方式留空表示不覆盖，建库时由设备侧默认口径决定。
	if d.ChargeMode != "" {
		row["charge_mode"] = d.ChargeMode
	}
	return tx.Table("device_meta").Create(row).Error
}

// MarkImportFailed 记一次失败并累加尝试次数，保留可重试标记。
func (s DeviceImportStore) MarkImportFailed(tx *gorm.DB, importID, reason string) error {
	return tx.Table("device_import").Where("import_id=?", importID).
		Updates(map[string]any{"status": "failed", "last_error": reason, "attempts": gorm.Expr("attempts+1")}).Error
}

// MarkImportCompleted 标记导入完成：清空失败原因、累加尝试次数并关闭重试。
func (s DeviceImportStore) MarkImportCompleted(tx *gorm.DB, importID string) error {
	return tx.Table("device_import").Where("import_id=?", importID).
		Updates(map[string]any{"status": "completed", "last_error": nil, "attempts": gorm.Expr("attempts+1"), "retryable": false}).Error
}
