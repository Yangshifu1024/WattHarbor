package stop

import (
	"context"
	"time"

	"chargepilot/internal/card"
	"chargepilot/internal/order"
	"gorm.io/gorm"
)

// FreezeDeadline 锁定已购时长对应的计费截止点：仍在充电且截止点已到则写入
// charge_billing_cutoff，重复冻结沿用记录，并返回是否应触发停机。
// ctx 取消整个事务；db 为连接或外层事务，id 为订单主键，now 为 UTC 判定时刻。
// 返回 false、nil 表示订单已结束或尚未到期；错误表示冻结失败，不应据此下发 STOP。
// 加锁顺序与 Swipe 一致：先 card_charge 会话行，后 charge_order 行。
func FreezeDeadline(ctx context.Context, db *gorm.DB, id uint64, now time.Time) (bool, error) {
	stop := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		session, err := card.LockPurchaseForOrder(tx, id)
		if err != nil {
			return err
		}
		state, err := order.LockChargingState(tx, id)
		if err != nil {
			return err
		}
		if state.Status != "charging" {
			return nil
		}
		frozen, err := BillingCutoffFrozen(ctx, tx, id)
		if err != nil {
			return err
		}
		if frozen > 0 {
			stop = true
			return nil
		}
		deadline := state.StartedAt.Add(time.Duration(session.PurchasedMinutes) * time.Minute)
		if now.Before(deadline) {
			return nil
		}
		if err := FreezeBillingCutoff(tx, id, map[string]any{"charge_order_id": id, "cutoff_at": deadline, "reason": "card_duration_exhausted"}); err != nil {
			return err
		}
		stop = true
		return nil
	})
	return stop, err
}
