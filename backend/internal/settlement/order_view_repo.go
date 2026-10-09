package settlement

import (
	"context"

	"gorm.io/gorm"
)

// 本文件读取订单详情所需结算字段，订单基础查询由 order 负责，退款由 settlement 负责。

// endReceiptView 只读取 charging/finish 保存的结束计量，并与订单 ended_at 核对。
type endReceiptView struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id;primaryKey"` // 充电订单主键
	StopCommandID string `gorm:"column:stop_command_id"`            // 停止命令号
	MeterJSON     []byte `gorm:"column:meter_json"`                 // 结束计量证据 JSON，计费的原始输入
}

// TableName 返回结束回执所属固定表名，无参数、错误或 IO。
func (endReceiptView) TableName() string { return "charge_end_receipt" }

// ListOrderRefunds 按订单 ID 倒序读取退款记录；调用方负责订单授权。
func ListOrderRefunds(ctx context.Context, db *gorm.DB, orderID uint64) ([]map[string]any, error) {
	rows := []map[string]any{}
	err := db.WithContext(ctx).Table("refund_record").
		Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", orderID).
		Select("refund_no, refund_cents, status, reason, created_at").Order("id DESC").Find(&rows).Error
	return rows, err
}
