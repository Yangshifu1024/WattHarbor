package pricing

import (
	"context"
	"encoding/json"
	"strings"

	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"gorm.io/gorm"
)

// InputError 表示方案参数不合法，HTTP 映射为 400，不含存储细节。
type InputError struct {
	Reason string /* 输入校验提示 */
}

// Error 返回输入提示。
func (e InputError) Error() string { return e.Reason }

// SchemeService 校验完整方案，保存版本变化和审计，不修改历史订单快照。
type SchemeService struct {
	DB *gorm.DB /* central 方案连接，由进程管理 */
}

// SchemeInput 是完整方案和期望版本；创建时忽略期望版本，更新时须与已存版本一致。
type SchemeInput struct {
	Scheme          charging.Scheme `json:"scheme"`           // 全部套餐及展示约束，保存前规范化和领域校验
	ExpectedVersion uint32          `json:"expected_version"` // 修改时必须匹配保存版本
}

// SchemeResult 返回保存后的主键和版本。
type SchemeResult struct {
	ID      uint64 `json:"id"`      // 方案模板主键
	Version uint32 `json:"version"` // 保存后的模板版本
}

// PricingRuleStatus 是 pricing_rule.status 列的计费规则启停状态。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（active／disabled）。
// 与 PricingTemplateStatus 分立：同词表但不同表的两个独立列，状态可各自变化。
type PricingRuleStatus string

const (
	// PricingRuleActive 表示规则可用于实际计费。
	PricingRuleActive PricingRuleStatus = "active"
	// PricingRuleDisabled 表示规则已停用，不能用于新发布，历史订单快照不变。
	PricingRuleDisabled PricingRuleStatus = "disabled"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
func (s PricingRuleStatus) Known() bool {
	switch s {
	case PricingRuleActive, PricingRuleDisabled:
		return true
	default:
		return false
	}
}

// PricingTemplateStatus 是 pricing_template.status 列的计费模板启停状态。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（active／disabled）。
// 与 PricingRuleStatus 分立：词表相同但列不同，不能互相赋值。
type PricingTemplateStatus string

const (
	// PricingTemplateActive 表示模板可用于发布规则。
	PricingTemplateActive PricingTemplateStatus = "active"
	// PricingTemplateDisabled 表示模板已停用；软删也固定置为此值。
	PricingTemplateDisabled PricingTemplateStatus = "disabled"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
func (s PricingTemplateStatus) Known() bool {
	switch s {
	case PricingTemplateActive, PricingTemplateDisabled:
		return true
	default:
		return false
	}
}

// ChangeSchemeInput 是启停或删除参数；删除时忽略 Status，但审计保留提交内容。
type ChangeSchemeInput struct {
	ExpectedVersion uint32                `json:"expected_version"` // 当前模板版本
	Status          PricingTemplateStatus `json:"status"`           // active／disabled；删除时由 repo 固定为 disabled
}

// Save 核对当前权限后保存完整方案，ID=0 创建，否则检查期望版本。
// 返回主键和新版本，方案和审计同事务，失败整体回滚。
func (s SchemeService) Save(ctx context.Context, audit ops.AuditContext, id uint64, in SchemeInput) (SchemeResult, error) {
	permission := "pricing.rule.create"
	if id != 0 {
		permission = "pricing.rule.update"
	}
	if !audit.Actor.HasPermission(permission) {
		return SchemeResult{}, operations.ErrForbidden
	}
	in.Scheme = in.Scheme.Normalized()
	in.Scheme.Name = strings.TrimSpace(in.Scheme.Name)
	if err := in.Scheme.Validate(); err != nil {
		return SchemeResult{}, InputError{Reason: err.Error()}
	}
	raw, _ := json.Marshal(in.Scheme.SpecFor(in.Scheme.Packages[0]))
	display, _ := json.Marshal(in.Scheme.Display)
	out := SchemeResult{}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created, version, before, err := (Repo{}).SavePricingTemplate(tx, id, in.ExpectedVersion, PricingTemplateInput{Name: in.Scheme.Name, Remark: in.Scheme.Remark, SpecJSON: string(raw), DisplayJSON: string(display)})
		if err != nil {
			return err
		}
		out = SchemeResult{ID: created, Version: version}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "charging_scheme.save", Target: "pricing_template", ID: out.ID, Before: before, After: in.Scheme})
	})
	return out, err
}

// ChangeStatus 核对当前权限和期望版本后启停或软删模板，与审计同事务；已发布规则和历史订单价格不变。
// 删除路径不校验 Status：remove=true 时 in.Status 允许是空串或未知值，仓储会固定写成 disabled。
// 这里不得改成无条件校验，否则删除请求会被提前拒绝，属于行为变更。
func (s SchemeService) ChangeStatus(ctx context.Context, audit ops.AuditContext, id uint64, in ChangeSchemeInput, remove bool) error {
	if !audit.Actor.HasPermission("pricing.rule.update") {
		return operations.ErrForbidden
	}
	if !remove && !in.Status.Known() {
		return InputError{Reason: "状态无效"}
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before, err := (Repo{}).ChangePricingTemplateStatus(tx, id, in.ExpectedVersion, in.Status, remove)
		if err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "charging_scheme.status", Target: "pricing_template", ID: id, Before: before, After: in})
	})
}
