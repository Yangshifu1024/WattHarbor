package payment

import (
	"context"

	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
)

// 支付所属的余额充值历史，通过原用户归属读取，不由身份模块写入支付记录。

// ChargeUserRecharges 只返回未删除用户的余额充值，不把未支付订单视为到账。
// ctx 取消 db 查询，id 为用户身份，page 为已校验分页条件；返回支付页或用户缺失／读取错误。
func ChargeUserRecharges(ctx context.Context, db *gorm.DB, id uint64, page persist.PageQuery) (persist.Page[PaymentOrderView], error) {
	var user struct {
		ID uint64 // 原用户主键，读取时已排除软删除
	}
	if err := db.WithContext(ctx).Table("user").Select("id").Where("id=? AND deleted_at IS NULL", id).Take(&user).Error; err != nil {
		return persist.Page[PaymentOrderView]{}, err
	}
	return PaymentOrders(ctx, db, PaymentOrderQuery{PageQuery: page, UserID: id, BizType: "wallet_recharge"})
}
