package database

import (
	"context"
	"encoding/json"
	"errors"

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

// ListPricingTemplates 返回全部未删除的方案模板，按 ID 倒序。
// spec_json 可能是过期口径，调用方解析后仍需重新校验才能下发。
func (s PricingStore) ListPricingTemplates(ctx context.Context) ([]PricingTemplateRow, error) {
	rows := []PricingTemplateRow{}
	err := s.DB.WithContext(ctx).Table("pricing_template").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error
	return rows, err
}

// PricingTemplateInput 是模板保存的写入参数，序列化由调用方完成。
type PricingTemplateInput struct {
	Name        string
	Remark      string
	SpecJSON    string
	DisplayJSON string
}

// SavePricingTemplate 新建或更新一个方案模板。id 为 0 表示新建，返回新版本号 1；
// 否则在行锁下按 expectedVersion 乐观校验后把版本 +1。返回更新前的记录供审计留痕。
func (s PricingStore) SavePricingTemplate(tx *gorm.DB, id uint64, expectedVersion uint32, in PricingTemplateInput) (uint64, uint32, PricingTemplateRow, error) {
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
		created, err := LastInsertID(tx)
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
func (s PricingStore) ChangePricingTemplateStatus(tx *gorm.DB, id uint64, expectedVersion uint32, status string, remove bool) (PricingTemplateRow, error) {
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

// FindPublication 按请求号取下发凭据。取不到返回 gorm.ErrRecordNotFound。
func (s PricingStore) FindPublication(tx *gorm.DB, requestID string) (PricingPublication, error) {
	var row struct {
		RequestID   string
		ActorID     uint64
		PayloadHash string
		RuleID      uint64
		Version     uint32
	}
	err := tx.Table("pricing_publication").Where("request_id=?", requestID).Take(&row).Error
	return PricingPublication{RequestID: row.RequestID, ActorID: row.ActorID, PayloadHash: row.PayloadHash, RuleID: row.RuleID, Version: row.Version}, err
}

// RecordPublication 记下一次发布。同请求号并发到达时靠唯一键拦下重复写入。
func (s PricingStore) RecordPublication(tx *gorm.DB, p PricingPublication) error {
	return tx.Table("pricing_publication").Create(map[string]any{
		"request_id": p.RequestID, "actor_id": p.ActorID, "payload_hash": p.PayloadHash,
		"rule_id": p.RuleID, "version": p.Version,
	}).Error
}

// ActivePricingTemplate 取一个可下发的方案模板：必须处于 active 且未删除。
// 共享锁足以保证读取期间模板不被改版本，真正的版本一致性由调用方比对
// template_version 时再确认。
func (s PricingStore) ActivePricingTemplate(tx *gorm.DB, id uint64) (PricingTemplateRow, error) {
	var row PricingTemplateRow
	err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id=? AND status='active' AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// PricingTemplateScheme 解析模板里存的计费口径。口径缺失或方案为空时返回
// charging.ErrInvalidPricing：模板存在但内容不可用，调用方应拒绝下发。
func PricingTemplateScheme(row PricingTemplateRow) (charging.Scheme, error) {
	var spec charging.Spec
	if err := json.Unmarshal(row.SpecJSON, &spec); err != nil || spec.Scheme == nil {
		return charging.Scheme{}, charging.ErrInvalidPricing
	}
	return *spec.Scheme, nil
}

// CurrentRuleVersion 返回作用域内最新的规则版本号。没有规则时为 0，
// 与 expectedVersion=0 的首次下发约定一致。
func (s PricingStore) CurrentRuleVersion(tx *gorm.DB, stationID uint64, deviceID string) (uint32, error) {
	var current struct{ Version uint32 }
	err := RuleScope(stationID, deviceID)(tx.Table("pricing_rule")).
		Order("version DESC,id DESC").Limit(1).Find(&current).Error
	return current.Version, err
}

// PublishPricingRule 在作用域内把旧规则置为 disabled，再追加一版 active 规则。
// 版本号由调用方按 expectedVersion + 1 决定，这里只负责状态迁移与写入。
func (s PricingStore) PublishPricingRule(tx *gorm.DB, stationID uint64, deviceID string, version uint32, in PublishRuleInput) (uint64, error) {
	if err := RuleScope(stationID, deviceID)(tx.Table("pricing_rule")).
		Where("status='active'").Update("status", "disabled").Error; err != nil {
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
	return LastInsertID(tx)
}

// PublishRuleInput 是一次规则发布要写入的内容，序列化由调用方完成。
type PublishRuleInput struct {
	Name       string
	TemplateID uint64 // 来源模板 ID，0 表示直接提交完整方案
	SpecJSON   string
}

// InheritStationRule 让设备继承所在站点的定价：作用域内没有 active 规则时，
// 站点规则自然对设备生效，这里只需要把该设备自己历史上的 active 规则停用。
func (s PricingStore) InheritStationRule(tx *gorm.DB, stationID uint64, deviceID string) error {
	return RuleScope(stationID, deviceID)(tx.Table("pricing_rule")).
		Where("status='active'").Update("status", "disabled").Error
}

// LockStation 在下发前锁住目标站点，让并发的相同请求串行化，
// 后到的那个才能看到先到者写入的下发凭据。
func (s PricingStore) LockStation(tx *gorm.DB, stationID uint64) error {
	var station Station
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", stationID).Take(&station).Error
}

// CountStationDevice 确认设备确实挂在该站点下，命中数必须为 1。
// 查不到与查到多条都按未找到处理：设备归属不唯一时不该继续读生效方案。
func (s PricingStore) CountStationDevice(ctx context.Context, stationID uint64, deviceID string) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Table("device_meta").
		Where("station_id=? AND device_id=? AND deleted_at IS NULL", stationID, deviceID).Count(&count).Error
	return count, err
}
