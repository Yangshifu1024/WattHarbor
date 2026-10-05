package order

import (
	"context"
	"gorm.io/gorm"
)

// QueryRepo 读取用户拥有的原订单，表归属与请求隔离集中于所属 repo。
type QueryRepo struct {
	DB *gorm.DB /* 所属订单连接或调用方事务 */
}

// Owned 在 ctx 下按 userID 和 no 原业务号读取未删除订单，返回原事实或缺失／读取错误，无写入。
func (s QueryRepo) Owned(ctx context.Context, userID uint64, no string) (ChargeOrderRecord, error) {
	var row ChargeOrderRecord
	err := s.DB.WithContext(ctx).Where("order_no=? AND user_id=? AND deleted_at IS NULL", no, userID).Take(&row).Error
	return row, err
}

// Ongoing 在 ctx 下返回 userID 最新 paid/charging 未删除订单或缺失／读取错误，不发起充电或修改端口。
func (s QueryRepo) Ongoing(ctx context.Context, userID uint64) (ChargeOrderRecord, error) {
	var row ChargeOrderRecord
	err := s.DB.WithContext(ctx).Where("user_id=? AND deleted_at IS NULL AND status IN ('paid','charging')", userID).Order("id DESC").Take(&row).Error
	return row, err
}
