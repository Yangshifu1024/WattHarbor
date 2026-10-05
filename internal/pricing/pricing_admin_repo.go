package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对计费方案模板与定价规则的管理。方案本身的规范化与校验在
// domain/charging，这里只处理模板版本、下发记录与作用域内的规则翻版。
//
// 三张表：pricing_template 是可复用的方案模板；pricing_rule 是站点或设备上
// 实际生效的定价规则，每次下发追加一版并把旧的置为 disabled；pricing_publication
// 记录下发的幂等凭据，同一请求号重放返回首次发布的结果。

// ErrPricingVersionConflict 表示提交时模板或规则版本已被别人改动。
var ErrPricingVersionConflict = errors.New("计费方案版本已变更")

// ListPricingTemplates 在 ctx 下返回全部未删除模板，按 ID 倒序，查询错误原样返回，不改变状态。
// spec_json 可能是过期口径，调用方解析后仍需重新校验才能下发。
func (s Repo) ListPricingTemplates(ctx context.Context) ([]PricingTemplateRow, error) {
	rows := []PricingTemplateRow{}
	err := s.DB.WithContext(ctx).Table("pricing_template").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error
	return rows, err
}

// PricingTemplateInput 是模板保存的写入参数，序列化由调用方完成。
type PricingTemplateInput struct {
	Name        string // 已规范化模板名称
	Remark      string // 完整方案备注
	SpecJSON    string // 已通过领域校验的完整方案 JSON
	DisplayJSON string // 展示开关 JSON，不包含渠道凭据
}

// SavePricingTemplate 新建或更新一个方案模板。id 为 0 表示新建，返回新版本号 1；
// 否则在行锁下按 expectedVersion 乐观校验后把版本 +1。返回更新前的记录供审计留痕。
// tx 由用例管理，in 是已校验的持久载荷；返回主键、新版本、原快照及版本／存储错误，不自行提交。
func (s Repo) SavePricingTemplate(tx *gorm.DB, id uint64, expectedVersion uint32, in PricingTemplateInput) (uint64, uint32, PricingTemplateRow, error) {
	var before PricingTemplateRow
	version := uint32(1)
	if id > 0 {
		if err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return 0, 0, before, err
		}
		if before.Version != expectedVersion {
			return 0, 0, before, ErrPricingVersionConflict
		}
		version = before.Version + 1
	}
	row := map[string]any{
		"name": in.Name, "remark": in.Remark, "spec_json": in.SpecJSON,
		"display_json": in.DisplayJSON, "version": version,
	}
	if id == 0 {
		row["status"] = "active"
		if err := tx.Table("pricing_template").Create(row).Error; err != nil {
			return 0, 0, before, err
		}
		created, err := persist.LastInsertID(tx)
		if err != nil {
			return 0, 0, before, err
		}
		return created, version, before, nil
	}
	if err := tx.Table("pricing_template").Where("id=?", id).Updates(row).Error; err != nil {
		return 0, 0, before, err
	}
	return id, version, before, nil
}

// ChangePricingTemplateStatus 停用或软删除一个模板，同样带版本乐观校验。
// remove 为 true 时软删除并置 disabled；否则按 status 切换启用状态。
// tx 由用例管理，id／expectedVersion 是原模板身份；返回原快照或版本／存储错误，不修改历史发布规则。
func (s Repo) ChangePricingTemplateStatus(tx *gorm.DB, id uint64, expectedVersion uint32, status string, remove bool) (PricingTemplateRow, error) {
	var before PricingTemplateRow
	if err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
		return before, err
	}
	if before.Version != expectedVersion {
		return before, ErrPricingVersionConflict
	}
	row := map[string]any{"version": before.Version + 1, "status": status}
	if remove {
		row["status"] = "disabled"
		row["deleted_at"] = gorm.Expr("UTC_TIMESTAMP(3)")
	}
	if err := tx.Table("pricing_template").Where("id=?", id).Updates(row).Error; err != nil {
		return before, err
	}
	return before, nil
}

// PricingPublication 是下发结果的幂等凭据。
type PricingPublication struct {
	RequestID   string // 客户端请求号，同一次下发重放沿用同一个
	ActorID     uint64 // 发起下发的管理员 ID
	PayloadHash string // 请求体摘要；同一个请求号配不同内容即冲突
	RuleID      uint64 // 本次发布的定价规则 ID
	Version     uint32 // 本次发布的规则版本
}

// FindPublication 在 tx 下按 requestID 读取原发布凭据，返回缺行或读取错误，不修改数据。
// 用例在默认事务的首次站点锁后读取，随后用原身份和载荷摘要比对重放。
func (s Repo) FindPublication(tx *gorm.DB, requestID string) (PricingPublication, error) {
	var row struct {
		RequestID   string // 原发布请求 UUID
		ActorID     uint64 // 首次发起账号
		PayloadHash string // 原请求 JSON 的 SHA-256 十六进制摘要
		RuleID      uint64 // 首次发布规则主键
		Version     uint32 // 首次目标规则版本
	}
	err := tx.Table("pricing_publication").Where("request_id=?", requestID).Take(&row).Error
	return PricingPublication{RequestID: row.RequestID, ActorID: row.ActorID, PayloadHash: row.PayloadHash, RuleID: row.RuleID, Version: row.Version}, err
}

// RecordPublication 在用例 tx 中保存 p 原发布凭据；返回唯一键或写入错误，不自行提交。
// 用例按站点串行化同目标请求，全局 request_id 唯一键拒绝其他目标同号，不覆盖已有凭据。
func (s Repo) RecordPublication(tx *gorm.DB, p PricingPublication) error {
	return tx.Table("pricing_publication").Create(map[string]any{
		"request_id": p.RequestID, "actor_id": p.ActorID, "payload_hash": p.PayloadHash,
		"rule_id": p.RuleID, "version": p.Version,
	}).Error
}

// ActivePricingTemplate 取一个可下发的方案模板：必须处于 active 且未删除。
// 共享锁足以保证读取期间模板不被改版本，真正的版本一致性由调用方比对
// template_version 时再确认。
// tx 属于发布事务，id 是模板主键；返回原模板或缺行／存储错误，不改变模板。
func (s Repo) ActivePricingTemplate(tx *gorm.DB, id uint64) (PricingTemplateRow, error) {
	var row PricingTemplateRow
	err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id=? AND status='active' AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// PricingTemplateScheme 解析模板里存的计费口径。口径缺失或方案为空时返回
// charging.ErrInvalidPricing：模板存在但内容不可用，调用方应拒绝下发。
// row 为原模板投影，返回完整方案或格式错误；无 IO，不执行发布或修改原快照。
func PricingTemplateScheme(row PricingTemplateRow) (charging.Scheme, error) {
	var spec charging.Spec
	if err := json.Unmarshal(row.SpecJSON, &spec); err != nil || spec.Scheme == nil {
		return charging.Scheme{}, charging.ErrInvalidPricing
	}
	return *spec.Scheme, nil
}

// CurrentRuleVersion 返回作用域内最新的规则版本号。没有规则时为 0，
// 与 expectedVersion=0 的首次下发约定一致。
// tx 与 stationID／deviceID 定义原目标，返回版本及读取错误，不只筛选 active 记录。
func (s Repo) CurrentRuleVersion(tx *gorm.DB, stationID uint64, deviceID string) (uint32, error) {
	var current struct {
		Version uint32 /* 目标最新历史发布版本，零表示尚无规则 */
	}
	err := RuleScope(stationID, deviceID)(tx.Table("pricing_rule")).
		Order("version DESC,id DESC").Limit(1).Find(&current).Error
	return current.Version, err
}

// PublishPricingRule 在作用域内把旧规则置为 disabled，再追加一版 active 规则。
// 版本号由调用方按 expectedVersion + 1 决定，这里只负责状态迁移与写入。
// tx 已锁 stationID，deviceID 空为整站，in 为完整快照；返回新主键或存储错误，失败需整体回滚。
func (s Repo) PublishPricingRule(tx *gorm.DB, stationID uint64, deviceID string, version uint32, in PublishRuleInput) (uint64, error) {
	if err := s.disableActivePricingRules(tx, stationID, deviceID); err != nil {
		return 0, err
	}
	row := map[string]any{
		"name": in.Name, "station_id": stationID, "device_id": NullableDevice(deviceID),
		"spec_json": in.SpecJSON, "channel": "default", "version": version, "status": "active",
	}
	if in.TemplateID > 0 {
		row["template_id"] = in.TemplateID
	}
	if err := tx.Table("pricing_rule").Create(row).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// PublishRuleInput 是一次规则发布要写入的内容，序列化由调用方完成。
type PublishRuleInput struct {
	Name       string // 原完整方案名称
	TemplateID uint64 // 来源模板 ID，0 表示直接提交完整方案
	SpecJSON   string // 原完整方案及首个套餐执行视图 JSON
}

// InheritStationRule 让设备继承所在站点的定价：作用域内没有 active 规则时，
// 站点规则自然对设备生效，这里只需要把该设备自己历史上的 active 规则停用。
// tx 已锁 stationID，deviceID 为目标覆盖规则；返回写入错误，不删除历史记录或冻结快照。
func (s Repo) InheritStationRule(tx *gorm.DB, stationID uint64, deviceID string) error {
	return s.disableActivePricingRules(tx, stationID, deviceID)
}

// disableActivePricingRules 在已锁 stationID 的 tx 中停用 deviceID 对应的现有 active 规则。
// 先按调用方事务视图非锁读取主键，再按主键顺序保留原作用域及状态逐行更新；空目标不执行范围 UPDATE。
// 返回查询或更新错误，不提交或重试；沿用原 context 和事务视图，不刷新调用方已有 RR 快照。
func (s Repo) disableActivePricingRules(tx *gorm.DB, stationID uint64, deviceID string) error {
	var ids []uint64
	if err := RuleScope(stationID, deviceID)(tx.Table("pricing_rule")).
		Where("status='active'").Order("id").Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := RuleScope(stationID, deviceID)(tx.Table("pricing_rule")).
			Where("id=? AND status='active'", id).Update("status", "disabled").Error; err != nil {
			return err
		}
	}
	return nil
}
