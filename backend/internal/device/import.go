package device

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	contract "chargepilot/internal/contracts/gateway"
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ImportGateway 提供公开厂商资料和同步开通方法，不暴露 URL、令牌或通用 HTTP 客户端。
type ImportGateway interface {
	// Vendor 读取公开厂商协议资料，请求号只用于追踪。
	Vendor(context.Context, uint64, string) (contract.VendorReply, error)
	// Provision 确认设备清单已保存并开通；失败后必须使用同一设备编号重试。
	Provision(context.Context, []contract.ProvisionDevice) error
}

// ImportCompatibility 检查目标站点计费规则是否支持待导入设备协议，由 pricing 注入实现。
type ImportCompatibility interface {
	// CheckImportAgainstStation 在事务中检查新设备与站点计费规则是否兼容。
	CheckImportAgainstStation(*gorm.DB, []ImportDevice) error
}

// ImportService 分两段事务受理和同步执行导入，结果未知时用已有任务和设备编号恢复。
type ImportService struct {
	DB            *gorm.DB                                                                // central 导入任务与运营元数据连接，由进程管理
	Gateway       ImportGateway                                                           // 厂商读取及同步开通客户端，由 cmd 注入
	Compatibility ImportCompatibility                                                     // 所属站点规则兼容能力，由 cmd 注入 pricing repo
	LoadScope     func(context.Context, operations.Profile) (operations.DataScope, error) // 执行前重读当前范围，保留受理与执行之间的二次核对
}

// List 按全局 device.import 权限返回最近 100 个任务；不返回清单，不执行导入，不追加受理时的范围过滤。
func (s ImportService) List(ctx context.Context) ([]ImportJob, error) {
	return (ImportRepo{DB: s.DB}).ListImportJobs(ctx, 100)
}

// ImportInput 是批量导入请求，协议和能力以服务端厂商字典为准。
type ImportInput struct {
	ImportID string         `json:"import_id"` // 请求 UUID，同一任务及重试身份
	Devices  []ImportDevice `json:"devices"`   // 一至一百台设备编号，批内大小写不敏感判重
}

// ImportInputError 表示导入格式或校验失败，HTTP 映射为 400。
type ImportInputError struct {
	Message string /* 公开输入错误 */
}

// Error 返回导入输入提示。
func (e ImportInputError) Error() string { return e.Message }

// ImportScopeError 表示设备清单超出当前范围，HTTP 映射为 403，不创建失败任务。
type ImportScopeError struct{}

// Error 返回范围拒绝提示。
func (ImportScopeError) Error() string {
	return "导入设备的站点或厂商不在您的数据范围内"
}

// Unwrap 返回共享权限哨兵，供错误分类，不执行 IO。
func (ImportScopeError) Unwrap() error { return operations.ErrForbidden }

// checkImportScope 检查待导入设备的站点和厂商均在当前范围内。
func checkImportScope(scope operations.DataScope, devices []ImportDevice) error {
	for _, d := range devices {
		if !scope.AllowsStation(d.StationID) || !scope.AllowsVendor(d.VendorID) {
			return ImportScopeError{}
		}
	}
	return nil
}

// ValidateImportInput 检查请求号、批量大小、设备编号和声明模式。
// 服务端厂商协议、权限范围和计费兼容由 Create 继续校验。
func ValidateImportInput(in ImportInput) error {
	if uuid.Validate(in.ImportID) != nil || len(in.Devices) == 0 || len(in.Devices) > 100 {
		return ImportInputError{"导入编号无效或设备数不在 1–100 范围"}
	}
	seen := map[string]bool{}
	for _, d := range in.Devices {
		key := strings.ToLower(d.DeviceID)
		if !ValidDeviceID(d.DeviceID) || d.PortCount == 0 || d.VendorID == 0 || d.StationID == 0 || seen[key] || (d.Model != nil && utf8.RuneCountInString(*d.Model) > 128) {
			return ImportInputError{"设备编号、端口数、厂商或站点无效/重复"}
		}
		if d.ChargeMode != "" && !charging.ValidDeviceChargeMode(d.ChargeMode) {
			return ImportInputError{"设备 " + d.DeviceID + " 的计费方式无效"}
		}
		seen[key] = true
	}
	return nil
}

// Create 核对权限和范围，保存厂商协议、设备资料、任务和受理审计，再同步执行清单。
// 网关失败可保存 failed 任务；中央写入失败整体回滚，保留 pending 供同号重试。
func (s ImportService) Create(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, in ImportInput) (ImportJob, error) {
	if !audit.Actor.HasPermission("device.import") {
		return ImportJob{}, operations.ErrForbidden
	}
	if err := ValidateImportInput(in); err != nil {
		return ImportJob{}, err
	}
	if err := checkImportScope(scope, in.Devices); err != nil {
		return ImportJob{}, err
	}
	// 复制请求清单后再补充服务端协议，避免修改调用方用于重试的输入。
	in.Devices = append([]ImportDevice(nil), in.Devices...)
	adapters := map[uint64]string{}
	for i := range in.Devices {
		d := &in.Devices[i]
		adapter, found := adapters[d.VendorID]
		if !found {
			vendor, err := s.Gateway.Vendor(ctx, d.VendorID, audit.RequestID)
			if err != nil {
				return ImportJob{}, err
			}
			if vendor.Status != "enabled" || vendor.Protocol != "tcp" {
				return ImportJob{}, ImportInputError{"请选择已启用且支持的通信协议厂商"}
			}
			if _, err := charging.ProtocolCapabilities(vendor.AdapterClass); err != nil {
				return ImportJob{}, ImportInputError{err.Error()}
			}
			adapter = vendor.AdapterClass
			adapters[d.VendorID] = adapter
		}
		d.ProtocolAdapter = adapter
		cap, _ := charging.ProtocolCapabilities(adapter)
		d.ReportsEnergy, d.ReportsSegmentedPower = cap.ReportsEnergy, cap.ReportsSegmentedPower
	}
	body, _ := json.Marshal(in.Devices)
	repo := ImportRepo{DB: s.DB}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先登记唯一任务号，再锁读核对；RR 下先锁缺失任务会持间隙锁，并发首次插入可能互相等待。
		created, err := repo.ClaimImportJob(tx, in.ImportID, audit.Actor.ID, string(body))
		if err != nil {
			return err
		}
		prior, err := repo.LockImportJob(tx, in.ImportID)
		if err != nil {
			return err
		}
		if !created {
			var old []ImportDevice
			if json.Unmarshal([]byte(prior.RequestJSON), &old) != nil {
				return operations.ErrConflict
			}
			original, _ := json.Marshal(old)
			if string(original) != string(body) || prior.ActorID != audit.Actor.ID {
				return operations.ErrConflict
			}
			return nil
		}
		for _, d := range importLockOrder(in.Devices) {
			if _, err := repo.FindActiveStation(tx, d.StationID); err != nil {
				return err
			}
			raw, _ := json.Marshal(d)
			if err := repo.RegisterDeviceIdentity(tx, d.DeviceID, string(raw)); err != nil {
				return err
			}
			if err := repo.AssertDeviceIdentity(tx, d.DeviceID, d); err != nil {
				return err
			}
		}
		arriving, err := (OperationRepo{}).NewDevicesOnly(tx, in.Devices)
		if err != nil {
			return err
		}
		if err := s.Compatibility.CheckImportAgainstStation(tx, arriving); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "create", Target: "device_import", After: in, RequestID: in.ImportID})
	})
	if err != nil {
		return ImportJob{}, err
	}
	current, err := s.LoadScope(ctx, audit.Actor)
	if err != nil {
		return ImportJob{}, err
	}
	return s.Retry(ctx, audit, current, in.ImportID)
}

// Retry 核对当前权限和范围，在十秒开通过程中持有任务锁，不覆盖已有配置。
// 设备资料和完成审计同事务；开通失败保存 failed 和尝试次数，completed 重放不重复开通或审计。
func (s ImportService) Retry(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, id string) (ImportJob, error) {
	if !audit.Actor.HasPermission("device.import") {
		return ImportJob{}, operations.ErrForbidden
	}
	if uuid.Validate(id) != nil {
		return ImportJob{}, ImportInputError{"导入编号无效"}
	}
	var result ImportJob
	repo := ImportRepo{DB: s.DB}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := repo.LockImportJob(tx, id)
		if err != nil {
			return err
		}
		result = locked
		var devices []ImportDevice
		if err := json.Unmarshal([]byte(result.RequestJSON), &devices); err != nil {
			return err
		}
		if err := checkImportScope(scope, devices); err != nil {
			return err
		}
		if result.Status == "completed" {
			return nil
		}
		batch := make([]contract.ProvisionDevice, 0, len(devices))
		ordered := importLockOrder(devices)
		for _, d := range ordered {
			batch = append(batch, contract.ProvisionDevice{DeviceID: d.DeviceID, VendorID: d.VendorID, StationID: d.StationID, PortCount: d.PortCount, Model: d.Model})
		}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = s.Gateway.Provision(callCtx, batch)
		cancel()
		if err != nil {
			message := "网关未确认导入，请核对厂商与设备参数后重试；dc589 最多 20 路"
			result.Status = "failed"
			result.LastError = &message
			return repo.MarkImportFailed(tx, id, message)
		}
		for _, d := range ordered {
			existing, err := repo.LockImportedDevice(tx, d.DeviceID)
			if err == nil {
				if existing.VendorID != d.VendorID || existing.StationID != d.StationID {
					return operations.ErrConflict
				}
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err := repo.CreateImportedDevice(tx, d); err != nil {
				return err
			}
		}
		result.Status = "completed"
		result.LastError = nil
		if err := repo.MarkImportCompleted(tx, id); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "complete", Target: "device_import", After: map[string]any{"import_id": id, "count": len(devices)}, RequestID: id})
	})
	return result, err
}

// importLockOrder 复制并按忽略大小写的设备号排序，确定锁定和下发顺序。
// 重叠任务使用相同锁序，避免反向清单死锁；保存的首次清单不变。
func importLockOrder(devices []ImportDevice) []ImportDevice {
	ordered := append([]ImportDevice(nil), devices...)
	sort.Slice(ordered, func(i, j int) bool {
		return strings.ToLower(ordered[i].DeviceID) < strings.ToLower(ordered[j].DeviceID)
	})
	return ordered
}
