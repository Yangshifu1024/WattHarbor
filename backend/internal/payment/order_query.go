package payment

import (
	"context"

	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
)

// OrderQueryService 提供后台支付订单的原只读分页及关联订单投影，不确认支付或退款。
type OrderQueryService struct {
	DB *gorm.DB // central支付记录连接，由进程管理。
}

// List 在ctx下按已解析q查询支付订单，返回原页、空页或读取／取消错误。
// 不改变原order.read路由准入、排序、状态、可空关联、身份编码与时间边界。
func (s OrderQueryService) List(ctx context.Context, q PaymentOrderQuery) (persist.Page[PaymentOrderView], error) {
	return PaymentOrders(ctx, s.DB, q)
}
