package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"chargepilot/internal/device"
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PublicationService 在同一事务内检查范围、请求重放、设备兼容，再保存规则和审计。
type PublicationService struct {
	DB *gorm.DB /* central 规则连接，由进程管理 */
}

// PublishInput 保存发布请求 JSON，重放须逐项比对，不重新读取当前模板替换首次内容。
type PublishInput struct {
	RequestID       string           `json:"request_id"`       // 客户端 UUID，同号同内容恢复发布
	StationID       uint64           `json:"station_id"`       // 目标所属站点
	DeviceID        string           `json:"device_id"`        // 设备覆盖规则；空为整站默认
	TemplateID      uint64           `json:"template_id"`      // 非零使用现存模板，与 Scheme 二选一
	TemplateVersion uint32           `json:"template_version"` // 使用模板时必须匹配共享锁下当前版本
	ExpectedVersion uint32           `json:"expected_version"` // 目标规则版本，零为首次发布
	Scheme          *charging.Scheme `json:"scheme,omitempty"` // 直接发布的完整方案；模板发布为空
}

// PublicationResult 返回新发布或同请求恢复的首次结果。
type PublicationResult struct {
	ID       uint64 `json:"id"`       // 本次发布的规则 ID
	Version  uint32 `json:"version"`  // 本次发布的规则版本
	Replayed bool   `json:"replayed"` // true 表示返回首次发布记录，没有重复保存规则或审计
}

// InheritInput 是设备撤销自有覆盖、重新继承站点默认的请求内容。
type InheritInput struct {
	DeviceID        string `json:"device_id"`        // 所属站点中的目标设备
	ExpectedVersion uint32 `json:"expected_version"` // 当前设备自有规则版本
}

// TargetScopeError 表示目标超出权限范围，提示不含数据库错误。
type TargetScopeError struct {
	Reason string /* 站点或厂商范围提示 */
}

// Error 返回范围提示。
func (e TargetScopeError) Error() string { return e.Reason }

// Unwrap 返回共享权限哨兵，接口可据此映射 403。
func (e TargetScopeError) Unwrap() error { return operations.ErrForbidden }

// CheckTargetScope 检查站点和设备范围；没有厂商限制可管理站点默认，厂商受限时须选择所属设备。
func CheckTargetScope(ctx context.Context, db *gorm.DB, scope operations.DataScope, station uint64, deviceID string) error {
	if !scope.AllowsStation(station) {
		return TargetScopeError{Reason: "该站点不在您的数据范围内"}
	}
	// 没有厂商限制时允许管理整站默认规则。
	if scope.Unrestricted || len(scope.VendorIDs) == 0 {
		return nil
	}
	if deviceID == "" {
		return TargetScopeError{Reason: "厂商数据范围受限，不能修改整站默认配置"}
	}
	vendor, err := (device.OperationRepo{DB: db}).VendorID(ctx, station, deviceID)
	if err != nil {
		return err
	}
	if !scope.AllowsVendor(vendor) {
		return TargetScopeError{Reason: "该设备厂商不在您的数据范围内"}
	}
	return nil
}

// Publish 先核对当前权限和范围，再锁站点、检查请求重放、模板版本及设备兼容。
// 规则、请求记录和审计同事务，失败整体回滚；重试返回首次规则，不重新读取模板，也不下发设备指令。
func (s PublicationService) Publish(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, in PublishInput) (PublicationResult, error) {
	if !audit.Actor.HasPermission("pricing.rule.create") {
		return PublicationResult{}, operations.ErrForbidden
	}
	if uuid.Validate(in.RequestID) != nil || in.StationID == 0 || in.DeviceID != "" && !device.ValidDeviceID(in.DeviceID) || (in.TemplateID == 0) == (in.Scheme == nil) {
		return PublicationResult{}, InputError{Reason: "请选择模板或完整方案，并提供请求编号与站点"}
	}
	if err := CheckTargetScope(ctx, s.DB, scope, in.StationID, in.DeviceID); err != nil {
		return PublicationResult{}, err
	}
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	out := PublicationResult{}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := Repo{DB: tx}
		if _, err := device.LockStationForUpdate(tx, in.StationID); err != nil {
			return err
		}
		receipt, err := repo.FindPublication(tx, in.RequestID)
		if err == nil {
			if receipt.ActorID != audit.Actor.ID || receipt.PayloadHash != hash {
				return operations.ErrConflict
			}
			out = PublicationResult{ID: receipt.RuleID, Version: receipt.Version, Replayed: true}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		scheme := in.Scheme
		if in.TemplateID > 0 {
			tmpl, err := repo.ActivePricingTemplate(tx, in.TemplateID)
			if err != nil {
				return err
			}
			if tmpl.Version != in.TemplateVersion {
				return operations.ErrConflict
			}
			parsed, err := PricingTemplateScheme(tmpl)
			if err != nil {
				return err
			}
			scheme = &parsed
		}
		normalized := scheme.Normalized()
		scheme = &normalized
		if err := scheme.Validate(); err != nil {
			return fmt.Errorf("%w: %s", operations.ErrConflict, err)
		}
		if err := CheckSchemeTargets(tx, in.StationID, in.DeviceID, *scheme); err != nil {
			return fmt.Errorf("%w: %s", operations.ErrConflict, err)
		}
		current, err := repo.CurrentRuleVersion(tx, in.StationID, in.DeviceID)
		if err != nil {
			return err
		}
		if current != in.ExpectedVersion {
			return operations.ErrConflict
		}
		out.Version = current + 1
		snapshot, _ := json.Marshal(scheme.SpecFor(scheme.Packages[0]))
		out.ID, err = repo.PublishPricingRule(tx, in.StationID, in.DeviceID, out.Version, PublishRuleInput{Name: scheme.Name, TemplateID: in.TemplateID, SpecJSON: string(snapshot)})
		if err != nil {
			return err
		}
		if err := repo.RecordPublication(tx, PricingPublication{RequestID: in.RequestID, ActorID: audit.Actor.ID, PayloadHash: hash, RuleID: out.ID, Version: out.Version}); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "charging_scheme.apply", Target: "pricing_rule", ID: out.ID, After: map[string]any{"name": scheme.Name, "station_id": in.StationID, "device_id": in.DeviceID, "template_id": in.TemplateID, "spec_json": string(snapshot), "channel": "default", "version": out.Version, "status": "active"}})
	})
	return out, err
}

// Assign 核对当前权限和范围，锁站点并检查站点规则、设备兼容和期望版本后停用设备规则。
// 设备恢复继承站点规则，停用和审计同事务；已有订单及支付快照不变。
func (s PublicationService) Assign(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, station uint64, in InheritInput) error {
	if !audit.Actor.HasPermission("pricing.rule.update") {
		return operations.ErrForbidden
	}
	if in.DeviceID == "" || !device.ValidDeviceID(in.DeviceID) {
		return InputError{Reason: "请选择设备"}
	}
	if err := CheckTargetScope(ctx, s.DB, scope, station, in.DeviceID); err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := Repo{DB: tx}
		if _, err := device.LockStationForUpdate(tx, station); err != nil {
			return err
		}
		rule, err := repo.ActiveStationRule(ctx, station)
		if err != nil {
			return err
		}
		if err := CheckSchemeTargets(tx, station, in.DeviceID, *rule.Spec.Scheme); err != nil {
			return fmt.Errorf("%w: %s", operations.ErrConflict, err)
		}
		current, err := repo.CurrentRuleVersion(tx, station, in.DeviceID)
		if err != nil {
			return err
		}
		if current != in.ExpectedVersion {
			return operations.ErrConflict
		}
		if err := repo.InheritStationRule(tx, station, in.DeviceID); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "charging_scheme.inherit", Target: "station", ID: station, After: in})
	})
}
