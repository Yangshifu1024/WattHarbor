package card

import (
	"context"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
)

// PurchaseQueryRepo 批量提供刷卡累计购买事实，只读卡表，不重算支付金额。
type PurchaseQueryRepo struct {
	DB *gorm.DB // 所属卡连接或调用方只读事务，由进程管理
}

// Purchases 在 ctx 下按 ids 原订单读取累计实付分值、分钟及上限，返回身份映射或存储错误。
// 空 ids 不查询，非卡订单不进入映射；没有卡扣款、续购或截止写入。
func (r PurchaseQueryRepo) Purchases(ctx context.Context, ids []uint64) (map[uint64]charging.OrderCardPurchase, error) {
	out := make(map[uint64]charging.OrderCardPurchase, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ChargeOrderID              uint64 // 原刷卡充电订单身份
		charging.OrderCardPurchase        // 原累计实付分值、分钟与冻结上限
	}
	if err := r.DB.WithContext(ctx).Table("card_charge").Select("charge_order_id,paid_cents,purchased_minutes,max_minutes").Where("charge_order_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ChargeOrderID] = row.OrderCardPurchase
	}
	return out, nil
}
