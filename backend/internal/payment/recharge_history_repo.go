package payment

import (
	"chargepilot/internal/domain/money"
	"chargepilot/internal/persist"
	"chargepilot/internal/wallet"
	"context"
	"gorm.io/gorm"
)

// RechargeHistoryRepo 实现钱包所需的充值页读取，不持有身份或 HTTP 实现。
type RechargeHistoryRepo struct {
	DB *gorm.DB /* 进程负责的支付连接 */
}

// WalletRecharges 仅查询 userID 的 page 充值支付，返回公开查询结果；无资金写入。
func (s RechargeHistoryRepo) WalletRecharges(ctx context.Context, userID uint64, page persist.PageQuery) (persist.Page[wallet.AdminRechargeRow], error) {
	original, err := PaymentOrders(ctx, s.DB, PaymentOrderQuery{PageQuery: page, UserID: userID, BizType: string(money.PaymentBizWalletRecharge)})
	out := persist.Page[wallet.AdminRechargeRow]{Items: []wallet.AdminRechargeRow{}, Total: original.Total, Page: original.Page, PageSize: original.PageSize}
	if err != nil {
		return out, err
	}
	for _, v := range original.Items {
		out.Items = append(out.Items, wallet.AdminRechargeRow{PaymentOrderID: v.PaymentOrderID, OrderNo: v.OrderNo, UserID: v.UserID, BizType: v.BizType, PayMethod: v.PayMethod, Status: v.Status, PaymentStatus: v.PaymentStatus, TotalCents: v.TotalCents, PaidCents: v.PaidCents, RefundedCents: v.RefundedCents, PaidAt: v.PaidAt, CreatedAt: v.CreatedAt, ChargeOrderID: v.ChargeOrderID, ChargeOrderNo: v.ChargeOrderNo})
	}
	return out, nil
}
