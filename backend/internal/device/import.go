package device

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ImportGateway 只提供导入所需的厂商公开事实及原身份同步开通，不暴露 URL、令牌或通用 HTTP。
type ImportGateway interface {
	// Vendor 返回 id 的公开协议事实或原服务错误，requestID 仅用于服务追踪。
	Vendor(context.Context, uint64, string) (contract.VendorReply, error)
	// Provision 同步确认原设备清单已持久开通，失败不得改用新的设备身份。
	Provision(context.Context, []contract.ProvisionDevice) error
}

// ImportCompatibility 由 pricing 提供所属站点的计费兼容判断，保持 device 不反向引用 pricing。
type ImportCompatibility interface {
	// CheckImportAgainstStation 在原 tx 中批量核对首次 devices，返回兼容或存储错误，不建设备。
	CheckImportAgainstStation(*gorm.DB, []ImportDevice) error
}

// ImportService 拥有受理及同步执行两段事务，未知结果从原任务与原设备身份恢复。
type ImportService struct {
	DB            *gorm.DB                                                                // central 导入任务与运营元数据连接，由进程管理
	Gateway       ImportGateway                                                           // 厂商读取及同步开通客户端，由 cmd 注入
	Compatibility ImportCompatibility                                                     // 所属站点规则兼容能力，由 cmd 注入 pricing repo
	LoadScope     func(context.Context, operations.Profile) (operations.DataScope, error) // 执行前重读当前范围，保留受理与执行之间的原二次核对
}

// List 在ctx下返回最近100个原导入任务或读取／取消错误，保持原全局device.import查询。
// 不暴露持久清单、不重试或执行任务，不把受理／执行时的数据范围条件扩为新的列表过滤。
func (s ImportService) List(ctx context.Context) ([]ImportJob, error) {
	return (ImportRepo{DB: s.DB}).ListImportJobs(ctx, 100)
}

// ImportInput 是原公开批量请求；协议及能力由厂商字典覆盖，不信任客户端声明。
type ImportInput struct {
	ImportID string         `json:"import_id"` // 请求 UUID，原任务及重试身份
	Devices  []ImportDevice `json:"devices"`   // 一至一百台原设备身份，批内大小写不敏感判重
}

// ImportInputError 是导入格式或准入失败的原提示，可由 HTTP 映射为 400。
type ImportInputError struct {
	Message string /* 原公开输入错误 */
}

// Error 返回原输入提示，不执行 IO。
func (e ImportInputError) Error() string { return e.Message }

// ImportScopeError 标识原设备清单超出当前范围；适配器映射 403，不写入失败任务。
type ImportScopeError struct{}

// Error 返回原范围拒绝提示，不执行 IO。
func (ImportScopeError) Error() string {
	return "导入设备的站点或厂商不在您的数据范围内"
}

// Unwrap 返回共享权限哨兵，供错误分类，不执行 IO。
func (ImportScopeError) Unwrap() error { return operations.ErrForbidden }

// checkImportScope 依据显式 scope 核对 devices 的原站点及厂商，返回范围错误；无 IO 或修改。
func checkImportScope(scope operations.DataScope, devices []ImportDevice) error {
	for _, d := range devices {
		if !scope.AllowsStation(d.StationID) || !scope.AllowsVendor(d.VendorID) {
			return ImportScopeError{}
		}
	}
	return nil
}

// ValidateImportInput 校验 in 的请求号、批量边界、设备身份和声明模式，返回原输入提示。
// 无 IO，不信任声明的协议或计量能力；厂商事实、范围与目标兼容在 Create 内继续校验。
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

// Create 在 ctx 下按 audit 实时身份与 scope 接受 in，冻结厂商协议、身份及原任务，再同步执行。
// 返回原任务或输入／范围／兼容／依赖／存储错误；受理与审计同事务，执行使用原清单和原导入号。
// gateway 失败允许提交 failed 并返回该任务；中央执行写入失败整体回滚，保留先前 pending 供原号重试。
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
	// 原请求切片属于调用方；冻结服务事实只改本用例副本，避免重试持有的输入被隐式修改。
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
		// 先竞争任务唯一键；默认 RR 下对缺失任务 FOR UPDATE 会取得间隙锁，
		// 两个首次申请再插入会互相等待。登记和后续身份核对仍在一个事务中。
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

// Retry 在 ctx 下用 audit 当前权限及 scope 执行 id 原任务，返回原状态或范围／存储错误。
// 在十秒有界开通期间保持任务行锁，中央元数据和完成审计同事务；不覆盖已有设备配置。
// 失败开通提交 failed、递增尝试次数，可按原身份恢复；completed 重放不重复网络调用或审计。
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

// importLockOrder 复制 devices 并按大小写不敏感设备号排序，返回持锁及下发顺序，不修改冻结清单或执行 IO。
// 不同任务的重叠设备以相同顺序取得身份、网关开通和中央元数据锁，避免反向清单互相等待。
func importLockOrder(devices []ImportDevice) []ImportDevice {
	ordered := append([]ImportDevice(nil), devices...)
	sort.Slice(ordered, func(i, j int) bool {
		return strings.ToLower(ordered[i].DeviceID) < strings.ToLower(ordered[j].DeviceID)
	})
	return ordered
}
