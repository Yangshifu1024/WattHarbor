package database

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 分账模板与站点绑定的读取。模板与参与方的写入语义归属 settlement 家族，
// 这里只承接列表、详情回显与绑定前的历史核对。
//
// 绑定不是随手改字段：站点必须停用、近期没有支付意图、且站下设备没有产生过
// 订单，否则历史订单的分账口径会与新的模板对不上。

// splitTemplateSelect 是模板列表与详情共用的列，银行账号等敏感列不取。
const splitTemplateSelect = "id, code, name, mode, status"

// ListSplitTemplates 分页返回未删除的分账模板，可按状态与编码/名称关键字过滤。
func ListSplitTemplates(ctx context.Context, db *gorm.DB, q PageQuery) (Page[SplitTemplateRow], error) {
	out := Page[SplitTemplateRow]{Items: []SplitTemplateRow{}, Page: q.Page, PageSize: q.PageSize}
	query := db.WithContext(ctx).Table("split_template").Where("deleted_at IS NULL")
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		pattern := dbutil.LikePattern(q.Keyword)
		query = query.Where("(code LIKE ? ESCAPE '!' OR name LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select(splitTemplateSelect).Order("id DESC").
		Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// FindSplitTemplate 取一份未删除的模板。
func FindSplitTemplate(ctx context.Context, db *gorm.DB, id uint64) (SplitTemplateRow, error) {
	var row SplitTemplateRow
	err := db.WithContext(ctx).Table("split_template").Select(splitTemplateSelect).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// ListSplitParties 返回模板下的参与方，按 ID 升序，输出前需按脱敏规则裁剪账号。
func ListSplitParties(ctx context.Context, db *gorm.DB, templateID uint64) ([]SplitPartyRow, error) {
	rows := []SplitPartyRow{}
	err := db.WithContext(ctx).Table("split_party").
		Where("split_template_id = ?", templateID).Order("id").Find(&rows).Error
	return rows, err
}

// StationTemplateBinding 是站点绑定分账模板所需的站点字段。
type StationTemplateBinding struct {
	ID              uint64
	Status          string    // 站点状态；绑定要求已停用
	SplitTemplateID *uint64   // 当前绑定的模板 ID，nil 表示未绑定
	UpdatedAt       time.Time // 最近更新时间；站下有设备时要求静默一段时间
}

// LockStationTemplateBinding 在行锁下取站点绑定信息，供乐观比对与审计留痕。
func LockStationTemplateBinding(tx *gorm.DB, id uint64) (StationTemplateBinding, error) {
	var row StationTemplateBinding
	err := tx.Table("station").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// BindSplitTemplate 写入站点的分账模板。
func BindSplitTemplate(tx *gorm.DB, stationID uint64, templateID uint64) error {
	return tx.Table("station").Where("id = ?", stationID).Update("split_template_id", templateID).Error
}

// StationDeviceIDs 返回站下所有未删除设备的编号。
func StationDeviceIDs(tx *gorm.DB, stationID uint64) ([]string, error) {
	ids := []string{}
	err := tx.Table("device_meta").Where("station_id = ? AND deleted_at IS NULL", stationID).Pluck("device_id", &ids).Error
	return ids, err
}

// CountStationPaymentIntents 数出该站点产生过的支付意图。绑定模板会让历史订单
// 与新模板口径脱节，所以只要有过支付意图就拒绝绑定。
func CountStationPaymentIntents(ctx context.Context, db *gorm.DB, stationID uint64) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Table("charge_payment_intent").
		Where("station_id = ?", stationID).Count(&count).Error
	return count, err
}

// CountDeviceOrders 数出这些设备产生过的充电订单。设备列表为空时返回 0。
func CountDeviceOrders(ctx context.Context, db *gorm.DB, deviceIDs []string) (int64, error) {
	if len(deviceIDs) == 0 {
		return 0, nil
	}
	var count int64
	err := db.WithContext(ctx).Table("charge_order").
		Where("device_id IN ?", deviceIDs).Count(&count).Error
	return count, err
}
