package stop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"chargepilot/internal/card"
	rules "chargepilot/internal/domain/charging"
	orderrepo "chargepilot/internal/order"
	"chargepilot/internal/protocol"
	"gorm.io/gorm"
)

// AutoStopper 在 central 侧根据持久化订单与设备证据判定停机。
// 停机判定通过后先持久化计费截止点，再向 gateway 请求停机。
// 设备心跳证据经 gateway 批量端点按订单拉取，不直读 gateway 库。
type AutoStopper struct {
	DB      *gorm.DB // central 订单及截止点事务连接，生命周期由进程管理
	Gateway Gateway  // 提供端口证据及持久受理 STOP 的设备侧能力
}

// Run 根据持久化订单和设备证据停机。
// ctx 取消取证、数据库事务及请求；返回本轮已受理的 STOP 数及汇总错误。
// 单笔失败继续扫描；系统性取证失败保留已完成数，下一轮从持久截止点恢复。
// 断电判定要求设备心跳未过期，不能仅凭网络断开认定用户拔枪。
func (s AutoStopper) Run(ctx context.Context) (int, error) {
	if s.DB == nil || s.Gateway == nil {
		return 0, errors.New("auto stop is not configured")
	}
	now := time.Now().UTC()
	stopped := 0
	var failures error
	var afterID uint64
	for {
		orders, err := ListChargingOrdersAfter(ctx, s.DB, afterID)
		if err != nil {
			return stopped, errors.Join(failures, err)
		}
		if len(orders) == 0 {
			break
		}
		// 已提交截止点就是恢复凭据。先恢复决定，再对尚未冻结的订单取证。
		// 网关取证不可用或历史快照缺失不能阻断订单的 STOP 重试。
		pending := make([]AutoStopOrder, 0, len(orders))
		for _, row := range orders {
			if err := ctx.Err(); err != nil {
				return stopped, errors.Join(failures, err)
			}
			if row.CutoffAt == nil {
				pending = append(pending, row)
				continue
			}
			if err := s.requestStop(ctx, row); err != nil {
				failures = errors.Join(failures, fmt.Errorf("resume stop order %s: %w", row.OrderNo, err))
			} else {
				stopped++
			}
		}
		evidence, err := s.evidence(ctx, pending)
		if err != nil {
			return stopped, errors.Join(failures, err)
		}
		for _, row := range pending {
			if err := ctx.Err(); err != nil {
				return stopped, errors.Join(failures, err)
			}
			accepted, err := s.stopOrder(ctx, row, evidence[row.ID], now)
			if err != nil {
				failures = errors.Join(failures, fmt.Errorf("stop order %s: %w", row.OrderNo, err))
			}
			if accepted {
				stopped++
			}
		}
		afterID = orders[len(orders)-1].ID
	}
	return stopped, failures
}

// stopOrder 判定一笔尚未冻结的订单，并在提交截止点后请求网关持久受理。
// ctx 取消数据库及网关调用；row 是订单快照，samples 是完整分页证据，now 为 UTC 判定时刻。
// 返回 true、nil 仅表示 STOP 已受理；false、nil 表示尚不应停机，错误保留下一轮恢复入口。
// 受理不更新订单结束；单笔错误由 Run 汇总，不阻挡其他独立订单。
func (s AutoStopper) stopOrder(ctx context.Context, row AutoStopOrder, samples []protocol.Event, now time.Time) (bool, error) {
	order := row
	var contract struct {
		Rule  rules.Rule   `json:"rule"`  // 下单时冻结的计价规则
		Offer *rules.Offer `json:"offer"` // 下单套餐，金额单位分、时长单位分钟
	}
	if json.Unmarshal(order.PricingSnapshot, &contract) != nil || contract.Offer == nil || !contract.Offer.Valid() {
		return false, nil
	}
	stop := false
	var frozenFee *rules.Fee
	cutoff, reason := now, "automatic_stop"
	if contract.Offer.Mode == "duration" {
		purchased, err := card.PurchasedMinutesForOrder(ctx, s.DB, order.ID)
		if err != nil {
			return false, err
		}
		if purchased > 0 {
			contract.Offer.DurationMinutes = purchased
			if !now.Before(order.StartedAt.Add(time.Duration(purchased) * time.Minute)) {
				shouldStop, err := FreezeDeadline(ctx, s.DB, order.ID, now)
				if err != nil {
					return false, err
				}
				if shouldStop {
					if err := s.requestStop(ctx, order); err != nil {
						return false, err
					}
					return true, nil
				}
				return false, nil
			}
		}
	}
	if contract.Offer.Mode == "duration" && !now.Before(order.StartedAt.Add(time.Duration(contract.Offer.DurationMinutes)*time.Minute)) {
		stop = true
		cutoff = order.StartedAt.Add(time.Duration(contract.Offer.DurationMinutes) * time.Minute)
		reason = "duration_exhausted"
	}
	if contract.Offer.Mode == "amount" && contract.Rule.Spec.TimeCharge != nil && contract.Rule.Spec.TimeCharge.MaxMinutes > 0 {
		limit := order.StartedAt.Add(time.Duration(contract.Rule.Spec.TimeCharge.MaxMinutes) * time.Minute)
		if !now.Before(limit) {
			stop = true
			cutoff = limit
			reason = "duration_limit"
		}
	}
	if !stop {
		if noPowerForMinute(samples, order.PortNo, now) {
			stop = true
		}
		if !stop && contract.Offer.Mode == "amount" {
			if latest, ok := latestMeter(samples, order.PortNo, now); ok {
				plan, capped, feeErr := rules.BudgetStopAtMeter(contract.Rule, contract.Offer, measuredMeter(order, latest, samples))
				if feeErr != nil {
					log.Printf("charge budget unavailable order=%s device=%s port=%d sampled_at=%s error=%v", order.OrderNo, order.DeviceID, order.PortNo, latest.at.Format(time.RFC3339), feeErr)
				} else if plan.ShouldStop {
					frozenFee = capped
					stop = true
					cutoff = latest.at
					reason = "budget_exhausted"
					log.Printf("charge budget exhausted order=%s device=%s port=%d budget_cents=%d fee_lower_bound_cents=%d exact_split=%t sampled_at=%s", order.OrderNo, order.DeviceID, order.PortNo, contract.Offer.PriceCents, plan.AccruedCents, capped != nil, latest.at.Format(time.RFC3339))
				}
			}
		}
	}
	if stop {
		row := map[string]any{"charge_order_id": order.ID, "cutoff_at": cutoff, "reason": reason}
		if frozenFee != nil {
			row["electric_cents"] = frozenFee.ElectricCents
			row["service_cents"] = frozenFee.ServiceCents
		}
		// 锁定当前订单状态后原子首写。并发冻结保留费用与截止点。
		// 事务提交失败不得请求 STOP，提交后请求失败由下一轮 cutoff 恢复。
		eligible := false
		err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			state, err := orderrepo.LockChargingState(tx, order.ID)
			if err != nil {
				return err
			}
			if state.Status != "charging" {
				return nil
			}
			if err := FreezeBillingCutoff(tx, order.ID, row); err != nil {
				return err
			}
			eligible = true
			return nil
		})
		if err != nil {
			return false, err
		}
		if !eligible {
			return false, nil
		}
		if err := s.requestStop(ctx, order); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
