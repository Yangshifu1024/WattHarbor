package order

import (
	"context"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	orderdomain "wattharbor/internal/domain/order"
	"wattharbor/internal/persist"
)

// FeedbackRepo 负责订单反馈的表访问，不提供表名选择能力；连接生命周期由 cmd 管理。
type FeedbackRepo struct {
	DB *gorm.DB /* 所属订单与反馈连接或调用方事务 */
}

// FeedbackOrder 是提交反馈的当前订单信息；锁定后核对用户及完成状态。
type FeedbackOrder struct {
	ID       uint64                  // 订单主键
	UserID   uint64                  // 订单所有人
	DeviceID string                  // 设备编号，写入反馈快照
	Status   orderdomain.OrderStatus // 当前订单生命周期状态，反馈准入只允许 completed、refunding、refunded
}

// ResolveOwnedReference 按 userID 和 orderNo 读取未删除订单编号，返回主键，缺失时返回错误。
func (s FeedbackRepo) ResolveOwnedReference(ctx context.Context, userID uint64, orderNo string) (uint64, error) {
	var row struct {
		ID uint64 /* 订单编号 */
	}
	err := s.DB.WithContext(ctx).Table("charge_order").Select("id").Where("order_no=? AND user_id=? AND deleted_at IS NULL", orderNo, userID).Take(&row).Error
	return row.ID, err
}

// LockOrder 在调用方事务中读取最新记录并加锁 orderID 未删除订单，返回归属、设备、状态，缺失时返回错误。
// 订单按月份分区，主键定位保持既有含义；不自行提交，后续反馈检查与插入须共用事务。
func (FeedbackRepo) LockOrder(tx *gorm.DB, orderID uint64) (FeedbackOrder, error) {
	var row FeedbackOrder
	err := tx.Table("charge_order").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", orderID).Take(&row).Error
	return row, err
}

// HasFeedback 在已锁订单的调用方 tx 下当前读 orderID 的未删除反馈，返回存在性。
// SHARE 避免旧 RR 快照漏掉等锁期间已提交反馈；不能以非锁定 COUNT 代替当前读。
func (FeedbackRepo) HasFeedback(tx *gorm.DB, orderID uint64) (bool, error) {
	var rows []struct {
		ID uint64 /* 当前可见反馈主键 */
	}
	err := tx.Table("feedback").Clauses(clause.Locking{Strength: "SHARE"}).Select("id").Where("order_id=? AND deleted_at IS NULL", orderID).Find(&rows).Error
	return len(rows) > 0, err
}

// Insert 在调用方事务中为 order 的用户与设备保存已校验 in 和 imagesJSON，返回新反馈主键或写入错误。
// 反馈初始固定写 FeedbackPending（feedback.status 的可写初始值），枚举见 feedback_status.go；
// 请求身份来自已锁订单，不自行提交或产生回复审计。
func (FeedbackRepo) Insert(tx *gorm.DB, order FeedbackOrder, in FeedbackInput, imagesJSON string) (uint64, error) {
	if err := tx.Table("feedback").Create(map[string]any{"user_id": order.UserID, "order_id": order.ID, "device_id": order.DeviceID, "rating": in.Rating, "category": in.Category, "content": in.Content, "images_json": imagesJSON, "status": FeedbackPending}).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// List 按 status 和已校验 page/pageSize 分页读取未删除反馈，返回记录、总数。
// status 是 persist.PageQuery.Status 的共用筛选项，调用方未校验白名单；
// 此处仅作为 feedback.status 的 WHERE 参数原样传递，返回行仍为 map[string]any。
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

// LockStatus 在调用方事务中按 id 读取最新记录并加锁未删除反馈，返回状态，缺失时返回错误。
// 返回值是 feedback.status 的 FeedbackStatus；读到未知值时原值返回，由回复流转判定拒绝，
// 不在此处替换或降级。
func (FeedbackRepo) LockStatus(tx *gorm.DB, id uint64) (FeedbackStatus, error) {
	var row struct {
		Status FeedbackStatus /* 当前 pending/processed/closed */
	}
	err := tx.Table("feedback").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&row).Error
	return row.Status, err
}

// Update 在调用方事务中按 id 写入用例决定的 values 状态、回复字段，返回数据库错误，不选择其他表或提交事务。
func (FeedbackRepo) Update(tx *gorm.DB, id uint64, values map[string]any) error {
	return tx.Table("feedback").Where("id=?", id).Updates(values).Error
}
