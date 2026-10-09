package card

import (
	"context"
	"gorm.io/gorm"
)

// ListOrderOperations 用 db 读取已授权 orderID 的刷卡操作，返回创建顺序的数组。
// 无操作返回空数组；不重新扣款、确认回执或改写操作状态。
func ListOrderOperations(ctx context.Context, db *gorm.DB, orderID uint64) ([]CardOperation, error) {
	rows := []CardOperation{}
	err := db.WithContext(ctx).Where("charge_order_id=?", orderID).Order("created_at").Find(&rows).Error
	return rows, err
}
