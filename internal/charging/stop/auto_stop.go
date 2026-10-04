package stop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	rules "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
)

// AutoStopper 在 central 侧根据持久化订单与设备证据判定停机。
// 停机判定通过后先持久化计费截止点，再向 gateway 请求停机。
// 设备心跳证据经 gateway 批量端点按订单拉取，不直读 gateway 库。
type AutoStopper struct {
	DB      *gorm.DB
	Gateway Gateway
}

// Run 根据持久化订单和设备证据停机。
// 断电判定要求设备心跳未过期，不能仅凭网络断开认定用户拔枪。
func (s AutoStopper) Run(ctx context.Context) (int, error) {
	if s.DB == nil || s.Gateway == nil {
		return 0, errors.New("auto stop is not configured")
	}
	now := time.Now().UTC()
	stopped := 0
	var afterID uint64
	for {
		orders, err := ListChargingOrdersAfter(ctx, s.DB, afterID)
		if err != nil {
			return stopped, err
		}
		if len(orders) == 0 {
			break
		}
		evidence, err := s.evidence(ctx, orders)
		if err != nil {
			return stopped, err
		}
		for _, order := range orders {
			var contract struct {
				Rule  rules.Rule   `json:"rule"`
				Offer *rules.Offer `json:"offer"`
			}
			if json.Unmarshal(order.PricingSnapshot, &contract) != nil || contract.Offer == nil || !contract.Offer.Valid() {
				continue
			}
			samples := evidence[order.ID]
			stop := false
			var frozenFee *rules.Fee
			cutoff, reason := now, "automatic_stop"
			if contract.Offer.Mode == "duration" {
				purchased, err := PurchasedMinutesForOrder(ctx, s.DB, order.ID)
				if err != nil {
					return stopped, err
				}
				if purchased > 0 {
					contract.Offer.DurationMinutes = purchased
					if !now.Before(order.StartedAt.Add(time.Duration(purchased) * time.Minute)) {
						shouldStop, err := FreezeDeadline(ctx, s.DB, order.ID, now)
						if err != nil {
							return stopped, err
						}
						if shouldStop {
							if err := s.requestStop(ctx, order); err != nil {
								return stopped, err
							}
							stopped++
						}
						continue
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
				// 计费截止点按订单主键首写冻结：已有记录表明此前已冻结，
				// 视为冲突返回错误交由外层定时循环重试，不做静默空更新。
				frozen, err := BillingCutoffFrozen(ctx, s.DB, order.ID)
				if err != nil {
					return stopped, err
				}
				if frozen > 0 {
					return stopped, fmt.Errorf("charge billing cutoff already frozen for order %s", order.OrderNo)
				}
				if err := FreezeBillingCutoff(s.DB.WithContext(ctx), order.ID, row); err != nil {
					return stopped, err
				}
				if err := s.requestStop(ctx, order); err != nil {
					return stopped, err
				}
				stopped++
			}
		}
		afterID = orders[len(orders)-1].ID
	}
	return stopped, nil
}
