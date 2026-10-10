package device

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 导入先按请求号保存清单，再调用网关开通并补建设备资料；重试只用已保存清单。
// device_import 保存任务，device_import_identity 保存设备首次登记资料，device_meta 保存中央设备信息。

// ImportRepo 是批量导入的读写入口。
type ImportRepo struct {
	DB *gorm.DB /* central 导入任务连接，由进程管理；写方法使用调用方 tx */
}

// ListImportJobs 按创建时间倒序返回最近任务，最多 limit 条，不返回设备清单。
func (s ImportRepo) ListImportJobs(ctx context.Context, limit int) ([]ImportJob, error) {
	rows := []ImportJob{}
	err := s.DB.WithContext(ctx).Table("device_import").Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ErrImportConflict 表示同一导入号或设备号被用于不同内容，或设备已属于其他站点、厂商；HTTP 返回 409。
var ErrImportConflict = errors.New("导入内容与既有记录不一致")

// LockImportJob 在调用方事务中锁定导入任务；锁保持到网关调用和中央状态保存结束。
func (s ImportRepo) LockImportJob(tx *gorm.DB, importID string) (ImportJob, error) {
	var row ImportJob
	err := tx.Table("device_import").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("import_id=?", importID).Take(&row).Error
	return row, err
}

// ClaimImportJob 按唯一导入号保存首次清单，不覆盖已有任务。
// 重复插入等待提交后，再由 LockImportJob 锁读核对；不要先锁缺失行。
// 资料和兼容检查、审计同事务，后续失败须回滚 pending 登记。
// SQL 内的 'pending' 是 device_import.status 的字面量（ImportStatusPending），保留在语法内。
func (s ImportRepo) ClaimImportJob(tx *gorm.DB, importID string, actorID uint64, requestJSON string) (bool, error) {
	res := tx.Exec("INSERT IGNORE INTO device_import(import_id,actor_id,request_json,status) VALUES (?,?,?,'pending')", importID, actorID, requestJSON)
	return res.RowsAffected == 1, res.Error
}

// FindActiveStation 检查站点存在、未删除且为 active。
// status='active' 是 station.status 的字面量（StationActive），不是 device_meta.status。
func (s ImportRepo) FindActiveStation(tx *gorm.DB, stationID uint64) (Station, error) {
	var row Station
	err := tx.Where("id=? AND deleted_at IS NULL AND status='active'", stationID).Take(&row).Error
	return row, err
}

// RegisterDeviceIdentity 保存设备编号首次登记内容，INSERT IGNORE 不覆盖已有记录。
// 随后 AssertDeviceIdentity 比对内容，拒绝同号更改归属或配置。
func (s ImportRepo) RegisterDeviceIdentity(tx *gorm.DB, deviceID, requestJSON string) error {
	return tx.Exec("INSERT IGNORE INTO device_import_identity(device_id,request_json) VALUES (?,?)", deviceID, requestJSON).Error
}

// AssertDeviceIdentity 比对设备编号首次登记内容，不一致返回 ErrImportConflict，不覆盖旧记录。
func (s ImportRepo) AssertDeviceIdentity(tx *gorm.DB, deviceID string, want ImportDevice) error {
	var identity struct {
		RequestJSON string /* 设备首次受理清单 JSON，不能覆盖 */
	}
	if err := tx.Table("device_import_identity").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id=?", deviceID).Take(&identity).Error; err != nil {
		return err
	}
	var registered ImportDevice
	// 登记 JSON 无法解析表示数据损坏，不能按内容一致处理。
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

// ImportedDevice 保存已登记设备的主键和归属，用于检查是否可复用。
type ImportedDevice struct {
	ID        uint64  // 中央设备元数据主键
	VendorID  uint64  // 已登记的厂商
	StationID uint64  // 已登记的站点
	Model     *string // 已登记的型号，重新导入时不覆盖
}

// LockImportedDevice 锁读已有设备，包含历史记录；缺失返回 gorm.ErrRecordNotFound，供调用方决定新建。
func (s ImportRepo) LockImportedDevice(tx *gorm.DB, deviceID string) (ImportedDevice, error) {
	var row ImportedDevice
	err := tx.Table("device_meta").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id=?", deviceID).Take(&row).Error
	return row, err
}

// CreateImportedDevice 仅在确认设备不存在后新建资料，不能用默认字段覆盖已有能力或计费配置。
func (s ImportRepo) CreateImportedDevice(tx *gorm.DB, d ImportDevice) error {
	row := map[string]any{
		"device_id": d.DeviceID, "vendor_id": d.VendorID, "station_id": d.StationID,
		// "enabled" 写入的是 device_meta.status（devices.DeviceMetaStatus），不是 station.status；
		// 由 devices 包批次统一，本批次保留裸字面量。
		"model": d.Model, "status": "enabled", "protocol_adapter": d.ProtocolAdapter,
	}
	// 新建设备未提交计费模式时，使用 device_meta.charge_mode 的数据库默认值。
	if d.ChargeMode != "" {
		row["charge_mode"] = d.ChargeMode
	}
	return tx.Table("device_meta").Create(row).Error
}

// MarkImportFailed 保存脱敏失败原因并增加尝试次数，保留首次清单供重试。
// 写入 device_import.status 的 failed（ImportStatusFailed）。
func (s ImportRepo) MarkImportFailed(tx *gorm.DB, importID, reason string) error {
	return tx.Table("device_import").Where("import_id=?", importID).
		Updates(map[string]any{"status": ImportStatusFailed, "last_error": reason, "attempts": gorm.Expr("attempts+1")}).Error
}

// MarkImportCompleted 清空失败原因、增加尝试次数并关闭重试，与设备资料和完成审计同事务提交。
// 写入 device_import.status 的 completed（ImportStatusCompleted）。
func (s ImportRepo) MarkImportCompleted(tx *gorm.DB, importID string) error {
	return tx.Table("device_import").Where("import_id=?", importID).
		Updates(map[string]any{"status": ImportStatusCompleted, "last_error": nil, "attempts": gorm.Expr("attempts+1"), "retryable": false}).Error
}
