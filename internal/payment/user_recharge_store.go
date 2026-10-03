package payment

import (
	"context"

	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
)

// 用户的余额充值记录：查的是支付单，归属支付域而不是用户域。
// 原先与用户资料查询混在 identity/charge_user.go 里，那一处是全仓
// 唯一让 identity 依赖 payment 的地方，也是 redis→identity→payment 成环的原因。

// ChargeUserRecharges 只返回未删除用户的余额充值，不把未支付订单视为到账。
func ChargeUserRecharges(ctx context.Context, db *gorm.DB, id uint64, page persist.PageQuery) (persist.Page[PaymentOrderView], error) {
	var user struct{ ID uint64 }
	if err := db.WithContext(ctx).Table("user").Select("id").Where("id=? AND deleted_at IS NULL", id).Take(&user).Error; err != nil {
		return persist.Page[PaymentOrderView]{}, err
	}
	return PaymentOrders(ctx, db, PaymentOrderQuery{PageQuery: page, UserID: id, BizType: "wallet_recharge"})
}
