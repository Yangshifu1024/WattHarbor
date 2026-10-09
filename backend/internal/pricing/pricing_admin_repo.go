package pricing

import (
	"chargepilot/internal/persist"
	"context"
	"encoding/json"
	"errors"

	charging "chargepilot/internal/domain/charging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台管理计费模板、版本和发布记录，方案校验由 domain/charging 负责。
// pricing_template 保存模板，pricing_rule 每次发布追加新版本并停用旧版，pricing_publication 保证同请求返回首次结果。

// ErrPricingVersionConflict 表示提交时模板或规则版本已被别人改动。
var ErrPricingVersionConflict = errors.New("计费方案版本已变更")

// ListPricingTemplates 倒序读取未删除模板；历史 spec_json 下发前须重新解析校验。
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
	DisplayJSON string // 用户端展示开关 JSON
}

// SavePricingTemplate 在调用方事务中创建或更新模板，ID=0 创建版本 1。
// 更新先锁行并比对期望版本，再加一；返回新主键、版本和更新前记录供审计。
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

// ChangePricingTemplateStatus 锁定并核对期望版本后启停模板；删除时软删并置 disabled。
// 返回旧记录供审计，不修改历史发布规则。
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

// PricingPublication 保存请求号对应的发布结果，重试时核对操作者和内容摘要。
type PricingPublication struct {
	RequestID   string // 客户端请求号，同一次发布重试沿用同一个
	ActorID     uint64 // 发起发布的管理员 ID
	PayloadHash string // 请求体摘要；同一个请求号配不同内容即冲突
	RuleID      uint64 // 本次发布的定价规则 ID
	Version     uint32 // 本次发布的规则版本
}

// FindPublication 在站点锁后按请求号读取发布记录，随后比对请求目标和内容摘要。
func (s Repo) FindPublication(tx *gorm.DB, requestID string) (PricingPublication, error) {
	var row struct {
		RequestID   string // 发布请求 UUID
		ActorID     uint64 // 首次发起账号
		PayloadHash string // 首次请求 JSON 的 SHA-256 十六进制摘要
		RuleID      uint64 // 首次发布规则主键
		Version     uint32 // 首次目标规则版本
	}
	err := tx.Table("pricing_publication").Where("request_id=?", requestID).Take(&row).Error
	return PricingPublication{RequestID: row.RequestID, ActorID: row.ActorID, PayloadHash: row.PayloadHash, RuleID: row.RuleID, Version: row.Version}, err
}

// RecordPublication 在发布事务中保存请求结果，全局 request_id 唯一键拒绝同号不同目标，不覆盖首次记录。
func (s Repo) RecordPublication(tx *gorm.DB, p PricingPublication) error {
	return tx.Table("pricing_publication").Create(map[string]any{
		"request_id": p.RequestID, "actor_id": p.ActorID, "payload_hash": p.PayloadHash,
		"rule_id": p.RuleID, "version": p.Version,
	}).Error
}

// ActivePricingTemplate 在发布事务中共享锁读启用、未删模板，防止读取期间改版；调用方再比对 template_version。
func (s Repo) ActivePricingTemplate(tx *gorm.DB, id uint64) (PricingTemplateRow, error) {
	var row PricingTemplateRow
	err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id=? AND status='active' AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// PricingTemplateScheme 解析模板 JSON 并要求包含 Scheme；不检查方案数值，调用方还须 Validate。
func PricingTemplateScheme(row PricingTemplateRow) (charging.Scheme, error) {
	var spec charging.Spec
	if err := json.Unmarshal(row.SpecJSON, &spec); err != nil || spec.Scheme == nil {
		return charging.Scheme{}, charging.ErrInvalidPricing
	}
	return *spec.Scheme, nil
}

// CurrentRuleVersion 读取目标最新版本，包含非 active 记录；没有规则时返回 0，供首次发布校验。
func (s Repo) CurrentRuleVersion(tx *gorm.DB, stationID uint64, deviceID string) (uint32, error) {
	var current struct {
		Version uint32 /* 目标最新历史发布版本，零表示尚无规则 */
	}
	err := RuleScope(stationID, deviceID)(tx.Table("pricing_rule")).
		Order("version DESC,id DESC").Limit(1).Find(&current).Error
	return current.Version, err
}

// PublishPricingRule 在站点锁内停用目标旧规则，再追加 active 版本。
// 版本由调用方按期望版本加一确定，设备号为空表示整站；失败整体回滚。
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
	Name       string // 完整方案名称
	TemplateID uint64 // 来源模板 ID，0 表示直接提交完整方案
	SpecJSON   string // 完整方案及首个套餐执行视图 JSON
}

// InheritStationRule 在站点锁内停用设备自有 active 规则，使其重新使用站点定价；历史记录和订单快照不变。
func (s Repo) InheritStationRule(tx *gorm.DB, stationID uint64, deviceID string) error {
	return s.disableActivePricingRules(tx, stationID, deviceID)
}

// disableActivePricingRules 在站点锁内先普通读目标主键，再按主键顺序更新仍匹配的 active 规则。
// 空目标不做范围 UPDATE；沿用调用方 RR 快照，不刷新事务视图。
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
