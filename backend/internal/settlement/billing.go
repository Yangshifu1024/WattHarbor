// Package settlement 按订单价格和计量结算费用，处理余款退款、账单和人工审核。
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
	// Due 返回当前待执行订单 ID。
	Due(context.Context) ([]uint64, error)
	// Read 读取订单下单价格和结束计量。
	Read(context.Context, uint64) (Source, error)
	// Apply 幂等入账，资金、回执和事件同事务；失败不能标记已投递。
	Apply(context.Context, Result) error
	// Defer 更新任务状态并保存脱敏后的失败原因。
	Defer(context.Context, uint64, string, string) error
}

// BillingService 按订单计量、下单快照和费用回执完成入账及补账单。
type BillingService struct {
	Calculator FeeCalculator         // 计费计算和费用回执保存接口
	Orders     BillingOrderProcessor // 订单入账、任务恢复与人工核实入口
	Bills      BillIssuer            // central 装配的账单能力；nil 仅支持不产生账单的独立调用
}

// BillIssuer 以已入账费用回执生成账单；调度不重算资金或依赖 HTTP。
type BillIssuer interface {
	// Issue 根据已入账费用幂等生成账单；尚无正费用时不创建，失败由 Missing 恢复。
	Issue(ctx context.Context, chargeOrderID uint64) (Bill, bool, error)
	// Missing 返回最多 limit 笔已入账但缺少账单的订单。
	Missing(ctx context.Context, limit int) ([]uint64, error)
}

// Run 依次恢复待投递费用、结算到期订单、补齐缺失账单。
// 返回成功投递笔数；补账单失败不扣减该计数，下次只补账单。
// 计量不足或价格无效转人工核实，数据库等依赖失败保留原任务重试；ctx 取消后停止后续操作。
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

// deliverPending 重放已计算但未确认投递的费用，返回成功笔数和首个错误。
// 失败保留同一订单重试，Apply 保证响应丢失时不会重复扣费。
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

// recoverBills 每轮最多补发 50 笔已入账但缺失的账单；只复用费用回执，不重新扣款。
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
