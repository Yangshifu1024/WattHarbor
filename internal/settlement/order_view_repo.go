package settlement

import (
	"context"

	"gorm.io/gorm"
)

// 后台订单详情所需的只读投影，订单基础模型及主查询由 order 持有，退款由 settlement 持有。

// endReceiptView 是计费读取结束凭据所需的私有只读投影。
// 结束回执由 charging/finish 拥有写模型；计费只按原订单主键读取计量 JSON，
// 并与订单 ended_at 交叉核对，不写入也不重建该凭据。
type endReceiptView struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id;primaryKey"` // 原充电订单主键
	StopCommandID string `gorm:"column:stop_command_id"`            // 原停止命令身份
	MeterJSON     []byte `gorm:"column:meter_json"`                 // 原结束计量证据 JSON，计费的原始输入
}

// TableName 返回结束回执所属固定表名，无参数、错误或 IO。
func (endReceiptView) TableName() string { return "charge_end_receipt" }

// ListOrderRefunds 返回订单产生的退款记录，按 ID 倒序。
// ctx 取消 db 查询，orderID 为已授权原订单；无记录返回空数组，读取失败返回 error，不执行退款。
func ListOrderRefunds(ctx context.Context, db *gorm.DB, orderID uint64) ([]map[string]any, error) {
	rows := []map[string]any{}
	err := db.WithContext(ctx).Table("refund_record").
		Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", orderID).
		Select("refund_no, refund_cents, status, reason, created_at").Order("id DESC").Find(&rows).Error
	return rows, err
}
