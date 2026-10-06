package device

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

// ImportRepo 是批量导入的读写入口。
type ImportRepo struct {
	DB *gorm.DB /* central 导入任务连接，由进程管理；写方法使用调用方 tx */
}

// ListImportJobs 返回最近的导入任务，最多 limit 条，按创建时间倒序。
// 列表页看不到设备明细——原始清单只存库不外发。
// ctx 控制读取，limit 是调用方原上限；返回任务集合或存储错误，不领取或执行任务。
func (s ImportRepo) ListImportJobs(ctx context.Context, limit int) ([]ImportJob, error) {
	rows := []ImportJob{}
	err := s.DB.WithContext(ctx).Table("device_import").Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ErrImportConflict 表示同一导入号或设备编号被用于不同的内容，或设备已归属
// 别的站点与厂商。处理器据此返回 409。
var ErrImportConflict = errors.New("导入内容与既有记录不一致")

// LockImportJob 在行锁下取一个导入任务。执行导入时全程持锁：崩溃会连同
// 设备元数据一起回滚，重放则能看到网关那边已经持久占用的设备身份。
// tx 是调用方执行事务，importID 是原请求号；返回原任务或缺行／读取错误，不启动后台任务。
func (s ImportRepo) LockImportJob(tx *gorm.DB, importID string) (ImportJob, error) {
	var row ImportJob
	err := tx.Table("device_import").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("import_id=?", importID).Take(&row).Error
	return row, err
}

// ClaimImportJob 在 tx 下先按 importID 唯一键登记 actorID 的冻结 requestJSON，不覆盖已有任务。
// 返回是否首次登记及存储错误；重复申请等原写入提交后再由 LockImportJob 当前读比对，不先锁缺失行。
// 调用方必须将身份、兼容核对与审计纳入同一事务；任何后续失败都回滚本次 pending 登记。
func (s ImportRepo) ClaimImportJob(tx *gorm.DB, importID string, actorID uint64, requestJSON string) (bool, error) {
	res := tx.Exec("INSERT IGNORE INTO device_import(import_id,actor_id,request_json,status) VALUES (?,?,?,'pending')", importID, actorID, requestJSON)
	return res.RowsAffected == 1, res.Error
}

// FindActiveStation 确认站点存在、未删除且处于 active。
// tx 是原受理事务，stationID 是目标身份；返回站点事实或缺行／读取错误，不修改站点。
func (s ImportRepo) FindActiveStation(tx *gorm.DB, stationID uint64) (Station, error) {
	var row Station
	err := tx.Where("id=? AND deleted_at IS NULL AND status='active'", stationID).Take(&row).Error
	return row, err
}

// RegisterDeviceIdentity 登记设备编号与其内容快照。
//
// 已登记的编号不会被覆盖：INSERT IGNORE 命中既有行时保持原快照，随后由
// AssertDeviceIdentity 比对。这样同一编号改了内容会在这里被拒，而不是
// 悄悄改写历史归属。
// tx 由用例管理，deviceID 为原编号、requestJSON 为原完整身份；返回存储错误，不自行提交。
func (s ImportRepo) RegisterDeviceIdentity(tx *gorm.DB, deviceID, requestJSON string) error {
	return tx.Exec("INSERT IGNORE INTO device_import_identity(device_id,request_json) VALUES (?,?)", deviceID, requestJSON).Error
}

// AssertDeviceIdentity 取出该编号首次登记的快照并与当前内容比对。
// 不一致时返回 ErrImportConflict：设备编号是全局唯一的，不能被改写成另一个设备。
// tx 为已登记身份的受理事务，deviceID／want 为原请求内容；返回缺行／内容／读取错误，不覆盖原快照。
func (s ImportRepo) AssertDeviceIdentity(tx *gorm.DB, deviceID string, want ImportDevice) error {
	var identity struct {
		RequestJSON string /* 原设备首次受理清单 JSON，不能覆盖 */
	}
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
	ID        uint64  // 原中央设备元数据主键
	VendorID  uint64  // 已登记的厂商
	StationID uint64  // 已登记的站点
	Model     *string // 已登记的型号，重新导入时不覆盖
}

// LockImportedDevice 在行锁下取已登记的设备。取不到返回 gorm.ErrRecordNotFound，
// 调用方据此走"新建"分支。
// tx 是已锁原任务的执行事务，deviceID 是原编号；查询沿用原行为含历史行，不修改已有配置。
func (s ImportRepo) LockImportedDevice(tx *gorm.DB, deviceID string) (ImportedDevice, error) {
	var row ImportedDevice
	err := tx.Table("device_meta").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id=?", deviceID).Take(&row).Error
	return row, err
}

// CreateImportedDevice 建一条设备主数据。重复导入不得以缺省字段覆盖已有设备
// 的能力或计费设置，所以调用方只在确认设备不存在时才调这里。
// tx 为执行事务，d 提供原冻结身份及中央配置；返回写入错误，不修改 gateway 表或提交事务。
func (s ImportRepo) CreateImportedDevice(tx *gorm.DB, d ImportDevice) error {
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
// tx 为原执行事务，importID 是原任务、reason 为公开脱敏提示；返回写入错误，不改变原清单。
func (s ImportRepo) MarkImportFailed(tx *gorm.DB, importID, reason string) error {
	return tx.Table("device_import").Where("import_id=?", importID).
		Updates(map[string]any{"status": "failed", "last_error": reason, "attempts": gorm.Expr("attempts+1")}).Error
}

// MarkImportCompleted 标记导入完成：清空失败原因、累加尝试次数并关闭重试。
// tx 为原执行事务，importID 是原任务；返回写入错误，与设备元数据及完成审计一起提交。
func (s ImportRepo) MarkImportCompleted(tx *gorm.DB, importID string) error {
	return tx.Table("device_import").Where("import_id=?", importID).
		Updates(map[string]any{"status": "completed", "last_error": nil, "attempts": gorm.Expr("attempts+1"), "retryable": false}).Error
}
