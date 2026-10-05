package order

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FeedbackRepo 拥有订单反馈的表访问，不提供表名选择能力；连接生命周期由 cmd 管理。
type FeedbackRepo struct {
	DB *gorm.DB /* 所属订单与反馈连接或调用方事务 */
}

// FeedbackOrder 是提交反馈的当前订单事实；锁定后核对原用户及完成状态。
type FeedbackOrder struct {
	ID       uint64 // 原订单主键
	UserID   uint64 // 原订单所有人
	DeviceID string // 原设备身份，写入反馈快照
	Status   string // 当前 completed/refunding/refunded 等生命周期
}

// ResolveOwnedReference 在 ctx 下按 userID 和原 orderNo 读取未删除订单身份，返回主键或缺失／读取错误，无写入。
func (s FeedbackRepo) ResolveOwnedReference(ctx context.Context, userID uint64, orderNo string) (uint64, error) {
	var row struct {
		ID uint64 /* 原订单身份 */
	}
	err := s.DB.WithContext(ctx).Table("charge_order").Select("id").Where("order_no=? AND user_id=? AND deleted_at IS NULL", orderNo, userID).Take(&row).Error
	return row.ID, err
}

// LockOrder 在调用方 tx 中当前读锁定 orderID 未删除订单，返回归属／设备／状态或缺失／读取错误。
// 订单按月份分区，主键定位保持原语义；不自行提交，后续反馈检查与插入须共用事务。
func (FeedbackRepo) LockOrder(tx *gorm.DB, orderID uint64) (FeedbackOrder, error) {
	var row FeedbackOrder
	err := tx.Table("charge_order").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", orderID).Take(&row).Error
	return row, err
}

// HasFeedback 在已锁订单的调用方 tx 下当前读 orderID 的未删除反馈，返回存在性或读取错误。
// SHARE 避免旧 RR 快照漏掉等锁期间已提交反馈；不能以非锁定 COUNT 代替当前读。
func (FeedbackRepo) HasFeedback(tx *gorm.DB, orderID uint64) (bool, error) {
	var rows []struct {
		ID uint64 /* 当前可见反馈主键 */
	}
	err := tx.Table("feedback").Clauses(clause.Locking{Strength: "SHARE"}).Select("id").Where("order_id=? AND deleted_at IS NULL", orderID).Find(&rows).Error
	return len(rows) > 0, err
}

// Insert 在调用方 tx 下为 order 的原用户与设备保存已校验 in 和 imagesJSON，返回新反馈主键或写入错误。
// 反馈初始为 pending；请求身份来自已锁订单，不自行提交或产生回复审计。
func (FeedbackRepo) Insert(tx *gorm.DB, order FeedbackOrder, in FeedbackInput, imagesJSON string) (uint64, error) {
	if err := tx.Table("feedback").Create(map[string]any{"user_id": order.UserID, "order_id": order.ID, "device_id": order.DeviceID, "rating": in.Rating, "category": in.Category, "content": in.Content, "images_json": imagesJSON, "status": "pending"}).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// List 在 ctx 下按 status 和已校验 page/pageSize 分页读取原未删除反馈，返回原行、总数或读取错误，无写入。
func (s FeedbackRepo) List(ctx context.Context, status string, page, pageSize int) ([]map[string]any, int64, error) {
	q := s.DB.WithContext(ctx).Table("feedback").Where("deleted_at IS NULL")
	if status != "" {
		q = q.Where("status=?", status)
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []map[string]any{}
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

// LockStatus 在调用方 tx 中按 id 当前读锁定未删除反馈，返回原状态或缺失／读取错误，无写入。
func (FeedbackRepo) LockStatus(tx *gorm.DB, id uint64) (string, error) {
	var row struct {
		Status string /* 当前 pending/processed/closed */
	}
	err := tx.Table("feedback").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&row).Error
	return row.Status, err
}

// Update 在调用方 tx 下按 id 写入用例决定的 values 状态／回复字段，返回存储错误，不选择其他表或提交事务。
func (FeedbackRepo) Update(tx *gorm.DB, id uint64, values map[string]any) error {
	return tx.Table("feedback").Where("id=?", id).Updates(values).Error
}
