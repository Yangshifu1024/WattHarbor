package pricing

import (
	devicepkg "chargepilot/internal/device"
	"context"
	"encoding/json"
	"errors"

	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"gorm.io/gorm"
)

// QueryService 组合后台模板、有效方案和协议能力的只读结果，不改动历史冻结方案。
type QueryService struct {
	DB *gorm.DB // central 方案及目标事实连接，由进程管理
}

// SchemeView 是一个当前模板的公开视图，非法历史方案沿用原列表行为跳过。
type SchemeView struct {
	ID      uint64           `json:"id"`      // 模板主键
	Scheme  *charging.Scheme `json:"scheme"`  // 已存模板的完整方案
	Version uint32           `json:"version"` // 当前模板版本
	Status  string           `json:"status"`  // active／disabled
}

// EffectiveScheme 保留当前设备覆盖版本与实际生效的规则，不以继承规则版本替代覆盖版本。
type EffectiveScheme struct {
	Scheme      *charging.Scheme `json:"scheme"`                // 当前方案，无规则为 null
	Version     uint32           `json:"version"`               // 目标自有规则版本，空覆盖可为零
	RuleID      uint64           `json:"rule_id,omitempty"`     // 实际生效规则主键，无规则省略
	Inherited   bool             `json:"inherited"`             // 设备实际使用站点规则时为真，无规则为假
	Permissions []string         `json:"permissions,omitempty"` // 当前后台权限；无规则沿用原省略行为
}

// ExecutionCapabilities 是原适配器及领域执行能力的只读结果。
type ExecutionCapabilities struct {
	ProtocolAdapter       string                `json:"protocol_adapter"`        // 中央设备选定的适配器
	Capabilities          charging.Capabilities `json:"capabilities"`            // 领域支持的模式、限制与控制能力
	ReportsEnergy         bool                  `json:"reports_energy"`          // 协议电量能力，来自 Capabilities
	ReportsSegmentedPower bool                  `json:"reports_segmented_power"` // 协议分段功率能力，来自 Capabilities
}

// List 在 ctx 下核对 actor 读取权限后返回模板视图或存储错误；非法历史 JSON 跳过，只读。
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

// Effective 在 ctx 下以 actor 当前权限和 scope 查询 station/device 的实际方案及覆盖版本。
// 返回原空方案或视图，权限、归属、版本读取和存储错误原样返回；只读，不使用历史订单快照。
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

// Capabilities 在 ctx 下以 actor 当前权限和 scope 查询 station/device 的适配器能力。
// 返回完整原能力或权限、归属、未知适配器、存储错误；只读，不按设备型号或客户端声明推测。
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
