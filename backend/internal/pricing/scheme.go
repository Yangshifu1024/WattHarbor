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

// InputError 保留方案输入错误的原提示，供接口映射为 400；不包含存储细节。
type InputError struct {
	Reason string /* 原输入校验提示 */
}

// Error 返回输入提示，无 IO 或其他状态变更。
func (e InputError) Error() string { return e.Reason }

// SchemeService 拥有方案规范化、版本变更及同事务审计，不改动已冻结的订单快照。
type SchemeService struct {
	DB *gorm.DB /* central 方案连接，由进程管理 */
}

// SchemeInput 是完整方案及原期望版本，零版本用于首次创建。
type SchemeInput struct {
	Scheme          charging.Scheme `json:"scheme"`           // 全部套餐及展示约束，保存前规范化和领域校验
	ExpectedVersion uint32          `json:"expected_version"` // 修改时必须匹配持久版本
}

// SchemeResult 返回原保存结果，不带 HTTP 或数据库映射。
type SchemeResult struct {
	ID      uint64 `json:"id"`      // 方案模板主键
	Version uint32 `json:"version"` // 保存后的模板版本
}

// ChangeSchemeInput 保留启停或删除的原公开输入；删除时 Status 不参与状态选择但保留审计载荷。
type ChangeSchemeInput struct {
	ExpectedVersion uint32 `json:"expected_version"` // 当前模板版本
	Status          string `json:"status"`           // active／disabled；删除时由 repo 固定为 disabled
}

// Save 在 ctx 下保存 id 的完整 in；id=0 为创建，否则更新。audit 提供实时身份和审计事实。
// 返回新主键及版本；权限不足、非法方案、乐观冲突或存储失败均不留下半写或成功审计。
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

// ChangeStatus 在 ctx 下按 id、in 原版本启停或 remove 软删除，audit 提供实时权限。
// 返回版本或存储错误；模板状态与审计同事务，已有规则及历史订单继续保留原价格。
func (s SchemeService) ChangeStatus(ctx context.Context, audit ops.AuditContext, id uint64, in ChangeSchemeInput, remove bool) error {
	if !audit.Actor.HasPermission("pricing.rule.update") {
		return operations.ErrForbidden
	}
	if !remove && in.Status != "active" && in.Status != "disabled" {
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
