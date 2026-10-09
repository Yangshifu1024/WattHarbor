package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	charging "chargepilot/internal/domain/charging"
)

// BillingOrderProcessor 是计费调度所需的订单结算与持久任务能力。
type BillingOrderProcessor interface {
	// Due 在 ctx 下返回当前可执行的订单 ID，错误表示队列读取失败；不认领资金写入。
	Due(context.Context) ([]uint64, error)
	// Read 在 ctx 下读取给定订单 ID 的原计量和价格快照；缺证据或存储失败返回错误。
	Read(context.Context, uint64) (Source, error)
	// Apply 在 ctx 下幂等应用 Result 的原费用；资金、回执与事件同事务，错误不得标记已投递。
	Apply(context.Context, Result) error
	// Defer 在 ctx 下将订单 ID 任务置为给定状态及安全错误说明，返回持久化错误。
	Defer(context.Context, uint64, string, string) error
}

// BillingService 根据原计量、快照及持久费用回执调度入账和补账单。
type BillingService struct {
	Calculator FeeCalculator         // 纯规则计算及原费用回执的存档能力
	Orders     BillingOrderProcessor // 订单入账、任务恢复与人工核实入口
	Bills      BillIssuer            // central 装配的账单能力；nil 仅支持不产生账单的独立调用
}

// BillIssuer 以已入账费用回执生成账单；调度不重算资金或依赖 HTTP。
type BillIssuer interface {
	// Issue 在 ctx 下对 chargeOrderID 幂等生成或读取账单；返回账单、可出账标志及错误。
	// 无可开票费用时不创建，失败可通过 Missing 恢复；不重新应用扣款。
	Issue(ctx context.Context, chargeOrderID uint64) (Bill, bool, error)
	// Missing 在 ctx 下返回最多 limit 笔已入账且缺少账单的订单 ID，或读取错误。
	Missing(ctx context.Context, limit int) ([]uint64, error)
}

// Run 在 ctx 下先恢复未投递费用，再处理到期订单，最后补齐已入账的缺失账单。
// 返回已成功投递的笔数及失败；账单失败不减去已入账计数，后续轮次只补账单。
// 计量缺证据转人工核实，依赖失败保留原任务及回执重试；取消后停止发起后续操作。
func (s BillingService) Run(ctx context.Context) (int, error) {
	delivered, deliveryErr := s.deliverPending(ctx)
	ids, err := s.Orders.Due(ctx)
	if err != nil {
		return delivered, errors.Join(deliveryErr, err)
	}
	count := delivered
	first := deliveryErr
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return count, errors.Join(first, err)
		}
		source, err := s.Orders.Read(ctx, id)
		if err == nil {
			var result Result
			result, err = s.Calculator.Calculate(ctx, source)
			if errors.Is(err, charging.ErrMeterReview) || errors.Is(err, charging.ErrInvalidPricing) {
				payload, _ := json.Marshal(source)
				err = EnqueueManualFeeReview(s.Calculator.DB.WithContext(ctx), ManualFeeReviewInput{
					ChargeOrderID: id, OrderNo: source.OrderNo, Reason: "实际计量或计费快照需核实",
					EnergyWh: source.Meter.ChargedWh, CreatedMonth: time.Now().UTC().Format("2006-01"),
					SourceJSON: string(payload),
				})
				if err == nil {
					err = s.Orders.Defer(ctx, id, "manual_review", "实际计量或计费快照需核实")
				}
				if err == nil {
					continue
				}
			} else if err == nil {
				err = s.Orders.Apply(ctx, result)
				if err == nil {
					err = s.Calculator.MarkDelivered(ctx, id)
				}
				if err == nil {
					// 账单使用已提交的同一份费用结果，保持展示金额与扣费一致。
					if s.Bills != nil {
						if _, _, billErr := s.Bills.Issue(ctx, id); billErr != nil && first == nil {
							first = billErr
						}
					}
					count++
					continue
				}
			}
		}
		if err != nil {
			// 不要把存储错误暴露到运维可见的队列里。
			_ = s.Orders.Defer(ctx, id, "pending", "计费依赖暂不可用，等待重试")
			if first == nil {
				first = fmt.Errorf("bill order %d: %w", id, err)
			}
		}
	}
	return count, errors.Join(first, s.recoverBills(ctx))
}

// deliverPending 在 ctx 下重放已计算但尚未确认投递的原费用回执。
// 返回成功笔数和首个错误；失败按原订单重排队，Apply 幂等保证响应丢失不重复扣费。
func (s BillingService) deliverPending(ctx context.Context) (int, error) {
	rows, err := ListPendingFeeDeliveries(ctx, s.Calculator.DB)
	if err != nil {
		return 0, err
	}
	count := 0
	var first error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return count, errors.Join(first, err)
		}
		var result Result
		err := json.Unmarshal(row.PayloadJSON, &result)
		if err == nil && result.Source.ChargeOrderID != row.ChargeOrderID {
			err = ErrBillingConflict
		}
		if err == nil {
			err = s.Orders.Apply(ctx, result)
		}
		if err == nil {
			err = s.Calculator.MarkDelivered(ctx, row.ChargeOrderID)
		}
		if err == nil {
			if s.Bills != nil {
				if _, _, issueErr := s.Bills.Issue(ctx, row.ChargeOrderID); issueErr != nil && first == nil {
					first = issueErr
				}
			}
			count++
			continue
		}
		_ = RescheduleFeeDelivery(s.Calculator.DB.WithContext(ctx), row.ChargeOrderID, time.Now().UTC().Add(time.Minute))
		if first == nil {
			first = err
		}
	}
	return count, first
}

// recoverBills 补发已完成入账但尚未生成的账单。
// ctx 取消查询与文件外的数据库操作；返回聚合错误，成功或未配置账单能力返回 nil。
// 每轮最多 50 笔，只使用持久费用回执和原唯一键，不重新计算或应用资金。
func (s BillingService) recoverBills(ctx context.Context) error {
	if s.Bills == nil {
		return nil
	}
	ids, err := s.Bills.Missing(ctx, 50)
	if err != nil {
		return err
	}
	var failures error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		if _, _, err := s.Bills.Issue(ctx, id); err != nil {
			failures = errors.Join(failures, fmt.Errorf("issue bill order %d: %w", id, err))
		}
	}
	return failures
}
