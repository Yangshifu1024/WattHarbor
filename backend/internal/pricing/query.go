package pricing

import (
	"context"
	"encoding/json"
	"errors"
	devicepkg "wattharbor/internal/device"

	"gorm.io/gorm"
	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/protocol"
)

// QueryService 组合模板、实际生效方案和协议能力，不修改历史订单快照。
type QueryService struct {
	DB *gorm.DB // 中央计费模板、规则和设备资料连接，由进程管理
}

// SchemeView 是计费模板的公开字段；JSON 无法解析或缺少 Scheme 的记录跳过。
type SchemeView struct {
	ID      uint64                `json:"id"`      // 模板主键
	Scheme  *charging.Scheme      `json:"scheme"`  // 已存模板的完整方案
	Version uint32                `json:"version"` // 当前模板版本
	Status  PricingTemplateStatus `json:"status"`  // active／disabled
}

// EffectiveScheme 分别保存设备覆盖版本和实际生效规则，不能用继承版本代替设备覆盖版本。
type EffectiveScheme struct {
	Scheme      *charging.Scheme `json:"scheme"`                // 当前方案，无规则为 null
	Version     uint32           `json:"version"`               // 目标已保存的最新规则版本，包含停用规则；没有记录为零
	RuleID      uint64           `json:"rule_id,omitempty"`     // 实际生效规则主键，无规则省略
	Inherited   bool             `json:"inherited"`             // 设备实际使用站点规则时为真，无规则为假
	Permissions []string         `json:"permissions,omitempty"` // 传入账号的权限；无规则时省略
}

// ExecutionCapabilities 保存设备适配器和支持的功能。
type ExecutionCapabilities struct {
	ProtocolAdapter       protocol.AdapterID    `json:"protocol_adapter"`        // 中央设备选定的适配器
	Capabilities          charging.Capabilities `json:"capabilities"`            // 设备协议支持的计费模式、数值上限和控制方法
	ReportsEnergy         bool                  `json:"reports_energy"`          // 协议是否支持电量上报，来自 Capabilities
	ReportsSegmentedPower bool                  `json:"reports_segmented_power"` // 协议是否支持分段功率上报，来自 Capabilities
}

// List 核对读取权限后查询模板，跳过无法解析或缺少 Scheme 的 JSON；不校验方案数值。
func (s QueryService) List(ctx context.Context, actor operations.Profile) ([]SchemeView, error) {
	if !actor.HasPermission("pricing.read") {
		return nil, operations.ErrForbidden
	}
	rows, err := (Repo{DB: s.DB}).ListPricingTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := []SchemeView{}
	for _, row := range rows {
		var spec charging.Spec
		if json.Unmarshal(row.SpecJSON, &spec) != nil || spec.Scheme == nil {
			continue
		}
		out = append(out, SchemeView{ID: row.ID, Scheme: spec.Scheme, Version: row.Version, Status: row.Status})
	}
	return out, nil
}

// Effective 核对当前权限和范围后读取实际方案及设备覆盖版本，没有方案时保留空结果。
func (s QueryService) Effective(ctx context.Context, actor operations.Profile, scope operations.DataScope, station uint64, device string) (EffectiveScheme, error) {
	if !actor.HasPermission("pricing.read") {
		return EffectiveScheme{}, operations.ErrForbidden
	}
	if err := CheckTargetScope(ctx, s.DB, scope, station, device); err != nil {
		return EffectiveScheme{}, err
	}
	db := s.DB.WithContext(ctx)
	repo := Repo{DB: db}
	if device != "" {
		count, err := (devicepkg.OperationRepo{DB: db}).CountStationDevice(ctx, station, device)
		if err != nil {
			return EffectiveScheme{}, err
		}
		if count != 1 {
			return EffectiveScheme{}, gorm.ErrRecordNotFound
		}
	}
	version, err := repo.CurrentRuleVersion(db, station, device)
	if err != nil {
		return EffectiveScheme{}, err
	}
	out := EffectiveScheme{Version: version}
	rule, err := repo.ActiveDeviceRule(ctx, station, device)
	if errors.Is(err, charging.ErrRuleUnavailable) {
		return out, nil
	}
	if err != nil {
		return EffectiveScheme{}, err
	}
	out.Scheme, out.RuleID, out.Inherited = rule.Spec.Scheme, rule.ID, device != "" && rule.DeviceID == ""
	out.Permissions = append([]string(nil), actor.Permissions...)
	return out, nil
}

// Capabilities 核对当前权限和范围后查询设备协议支持的功能，不按型号或客户端声明推测。
func (s QueryService) Capabilities(ctx context.Context, actor operations.Profile, scope operations.DataScope, station uint64, device string) (ExecutionCapabilities, error) {
	if !actor.HasPermission("pricing.read") {
		return ExecutionCapabilities{}, operations.ErrForbidden
	}
	if err := CheckTargetScope(ctx, s.DB, scope, station, device); err != nil {
		return ExecutionCapabilities{}, err
	}
	adapter, cap, err := (devicepkg.OperationRepo{DB: s.DB}).Capabilities(ctx, station, device)
	return ExecutionCapabilities{ProtocolAdapter: adapter, Capabilities: cap, ReportsEnergy: cap.ReportsEnergy, ReportsSegmentedPower: cap.ReportsSegmentedPower}, err
}
