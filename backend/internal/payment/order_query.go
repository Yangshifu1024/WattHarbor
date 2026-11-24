package payment

import (
	"context"

	"gorm.io/gorm"
	"wattharbor/internal/persist"
)

// OrderQueryService 提供后台支付订单的只读分页及关联订单查询结果，不确认支付或退款。
type OrderQueryService struct {
	DB *gorm.DB // central支付记录连接，由进程管理。
}

// List 按已校验的查询条件读取支付订单页，保留状态、排序与可选充电订单关联。
func (s OrderQueryService) List(ctx context.Context, q PaymentOrderQuery) (persist.Page[PaymentOrderView], error) {
	return PaymentOrders(ctx, s.DB, q)
}
