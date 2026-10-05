package settlement

import (
	"context"

	"gorm.io/gorm"
)

// 后台订单详情所需的只读投影，订单基础模型及主查询由 order 持有，退款由 settlement 持有。

// ListOrderRefunds 返回订单产生的退款记录，按 ID 倒序。
// ctx 取消 db 查询，orderID 为已授权原订单；无记录返回空数组，读取失败返回 error，不执行退款。
func ListOrderRefunds(ctx context.Context, db *gorm.DB, orderID uint64) ([]map[string]any, error) {
	rows := []map[string]any{}
	err := db.WithContext(ctx).Table("refund_record").
		Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", orderID).
		Select("refund_no, refund_cents, status, reason, created_at").Order("id DESC").Find(&rows).Error
	return rows, err
}
