package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	dbstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Orders interface {
	Due(context.Context) ([]uint64, error)
	Read(context.Context, uint64) (dbstore.Source, error)
	Apply(context.Context, dbstore.Result) error
	Defer(context.Context, uint64, string, string) error
}
type Service struct {
	Store        dbstore.Store
	Orders       Orders
	Splits       dbstore.SplitResolver
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
			var result dbstore.Result
			result, err = s.Store.Calculate(ctx, source)
			if errors.Is(err, charging.ErrMeterReview) || errors.Is(err, charging.ErrInvalidPricing) {
				payload, _ := json.Marshal(source)
				err = s.Store.DB.WithContext(ctx).Table("manual_fee_review").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).Create(map[string]any{"charge_order_id": id, "order_no": source.OrderNo, "reason": "实际计量或计费快照需核实", "energy_wh": source.Meter.ChargedWh, "created_month": time.Now().UTC().Format("2006-01"), "source_json": string(payload)}).Error
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
					// 分账在 fee 持久化确定之后才记录，
					// 这样每次结算都有一份已落库的 calculation 可以挂靠。
					if splitErr := s.settleCalculation(ctx, source, result); splitErr != nil && first == nil {
						first = splitErr
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
	if settled, splitErr := s.settleBacklog(ctx); splitErr != nil {
		if first == nil {
			first = splitErr
		}
	} else {
		count += settled
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

// settleCalculation 为已计算费用生成分账记录；settlement_no 与 calculation_no 使用同一订单派生标识。
func (s Service) settleCalculation(ctx context.Context, source dbstore.Source, result dbstore.Result) error {
	// 分账基于有效费用结果追加台账；未配置 resolver 时跳过分账，不使已完成计费失败。
	if source.Rule.StationID == 0 || s.Splits.AdminDB == nil {
		return nil
	}
	calculationID, err := s.Store.CalculationID(ctx, source.ChargeOrderID)
	if err != nil {
		return err
	}
	template, err := s.Splits.Resolve(ctx, source.Rule.StationID)
	if err != nil {
		if errors.Is(err, dbstore.ErrNoSplitTemplate) || errors.Is(err, gorm.ErrRecordNotFound) {
			// 缺少生效分账模板时保留已计算费用，等待后续结算重试。
			return nil
		}
		return err
	}
	month, err := s.Store.CalculationMonth(ctx, calculationID)
	if err != nil {
		return err
	}
	_, _, err = s.Store.Settle(ctx, calculationID, template, result.ActualFee, month)
	return err
}

// settleBacklog 覆盖结算功能上线前就已算出的 fee，
// 或后来才配上分账模板的站点。它绝不改写已存在的结算。
func (s Service) settleBacklog(ctx context.Context) (int, error) {
	if s.Splits.AdminDB == nil {
		return 0, nil
	}
	pending, err := s.Store.SettlementsDue(ctx, 50)
	if err != nil {
		return 0, err
	}
	count, first := 0, error(nil)
	for _, row := range pending {
		if row.StationID == nil {
			continue
		}
		template, err := s.Splits.Resolve(ctx, *row.StationID)
		if err != nil {
			if errors.Is(err, dbstore.ErrNoSplitTemplate) || errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if first == nil {
				first = err
			}
			continue
		}
		month, err := s.Store.CalculationMonth(ctx, row.CalculationID)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		_, created, err := s.Store.Settle(ctx, row.CalculationID, template, charging.ActualFee{
			ElectricCents: row.ElectricCents, ServiceCents: row.ServiceCents, TotalCents: row.TotalCents,
		}, month)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if created {
			count++
		}
	}
	return count, first
}

func (s Service) deliverPending(ctx context.Context) (int, error) {
	var rows []struct {
		ChargeOrderID uint64
		PayloadJSON   []byte
	}
	if err := s.Store.DB.WithContext(ctx).Table("fee_delivery").Where("delivered=0 AND scheduled_at<=UTC_TIMESTAMP(3)").Order("scheduled_at,charge_order_id").Limit(50).Find(&rows).Error; err != nil {
		return 0, err
	}
	count := 0
	var first error
	for _, row := range rows {
		var result dbstore.Result
		err := json.Unmarshal(row.PayloadJSON, &result)
		if err == nil && result.Source.ChargeOrderID != row.ChargeOrderID {
			err = dbstore.ErrBillingConflict
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
		_ = s.Store.DB.WithContext(ctx).Table("fee_delivery").Where("charge_order_id=? AND delivered=0", row.ChargeOrderID).Updates(map[string]any{"attempts": gorm.Expr("attempts+1"), "scheduled_at": time.Now().UTC().Add(time.Minute)}).Error
		if first == nil {
			first = err
		}
	}
	return count, first
}
