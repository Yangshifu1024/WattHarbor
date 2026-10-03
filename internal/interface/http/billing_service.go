package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

type Orders interface {
	Due(context.Context) ([]uint64, error)
	Read(context.Context, uint64) (settlement.Source, error)
	Apply(context.Context, settlement.Result) error
	Defer(context.Context, uint64, string, string) error
}
type Service struct {
	Store        settlement.Store
	Orders       Orders
	ServiceToken string
	// Bills 在费用确定后开具用户可见的账单。它是可选的，
	// 这样在还没迁移账单表的部署里，计费任务照样能跑。
	Bills BillIssuer
}

// BillIssuer 是计费所需的账单存储子集，抽象成接口，
// 以便本包不依赖 charge 模块。
type BillIssuer interface {
	Issue(ctx context.Context, chargeOrderID uint64) (uint64, error)
}

func (s Service) Run(ctx context.Context) (int, error) {
	delivered, deliveryErr := s.deliverPending(ctx)
	ids, err := s.Orders.Due(ctx)
	if err != nil {
		return 0, err
	}
	count := delivered
	first := deliveryErr
	for _, id := range ids {
		source, err := s.Orders.Read(ctx, id)
		if err == nil {
			var result settlement.Result
			result, err = s.Store.Calculate(ctx, source)
			if errors.Is(err, charging.ErrMeterReview) || errors.Is(err, charging.ErrInvalidPricing) {
				payload, _ := json.Marshal(source)
				err = settlement.EnqueueManualFeeReview(s.Store.DB.WithContext(ctx), settlement.ManualFeeReviewInput{
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
					err = s.Store.MarkDelivered(ctx, id)
				}
				if err == nil {
					// 账单使用已提交的同一份费用结果，保持展示金额与扣费一致。
					if s.Bills != nil {
						if _, billErr := s.Bills.Issue(ctx, id); billErr != nil && first == nil {
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
	return count, first
}
func (s Service) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/billing/dispatch", func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(s.ServiceToken, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			return
		}
		n, err := s.Run(c.Request.Context())
		if err != nil {
			httpapi.Write(c, 503, 5003, "计费任务稍后重试", gin.H{"completed": n})
			return
		}
		httpapi.OK(c, gin.H{"completed": n})
	})
}

// settleBacklog 覆盖结算功能上线前就已算出的 fee，

func (s Service) deliverPending(ctx context.Context) (int, error) {
	rows, err := settlement.ListPendingFeeDeliveries(ctx, s.Store.DB)
	if err != nil {
		return 0, err
	}
	count := 0
	var first error
	for _, row := range rows {
		var result settlement.Result
		err := json.Unmarshal(row.PayloadJSON, &result)
		if err == nil && result.Source.ChargeOrderID != row.ChargeOrderID {
			err = settlement.ErrBillingConflict
		}
		if err == nil {
			err = s.Orders.Apply(ctx, result)
		}
		if err == nil {
			err = s.Store.MarkDelivered(ctx, row.ChargeOrderID)
		}
		if err == nil {
			count++
			continue
		}
		_ = settlement.RescheduleFeeDelivery(s.Store.DB.WithContext(ctx), row.ChargeOrderID, time.Now().UTC().Add(time.Minute))
		if first == nil {
			first = err
		}
	}
	return count, first
}
