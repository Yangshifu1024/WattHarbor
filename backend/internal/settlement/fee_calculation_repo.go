package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	charging "chargepilot/internal/domain/charging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrBillingConflict 表示计费证据、人工金额或已保存结果不一致，不能覆盖后重新收费。
var ErrBillingConflict = errors.New("billing source conflicts with persisted calculation")

// Source 保存下单价格、计量和人工裁决，重放须比对完整内容。
type Source struct {
	FinalFee      *charging.Fee        `json:"final_fee,omitempty"` // 已持久审计的最终金额；自动计费为空
	ReviewID      string               `json:"review_id,omitempty"` // 人工最终金额请求身份；未裁决为空
	ChargeOrderID uint64               `json:"charge_order_id"`     // 充电订单主键
	OrderNo       string               `json:"order_no"`            // 充电单号，派生稳定计费编号
	UserID        uint64               `json:"user_id"`             // 同一订单所属用户，内部证据保留历史数值编码
	Rule          charging.Rule        `json:"rule"`                // 下单时完整计价规则快照
	Offer         *charging.Offer      `json:"offer,omitempty"`     // 下单套餐，历史非套餐可空
	Meter         charging.ActualMeter `json:"meter"`               // 截止时已验证计量，Wh／秒及分段，不使用当前遥测
}

// Result 是按 Source 计算并保存的费用，金额单位分，重放保持计算号和金额不变。
type Result struct {
	CalculationNo      string `json:"calculation_no"` // 首次计费编号
	Source             Source `json:"source"`         // 首次计费证据，不以当前规则替换
	charging.ActualFee        // 领域确定的电费、服务费、总费及明细，金额为分
}

// FeeCalculator 计算领域费用，并在同一事务保存回执和可重试投递内容。
type FeeCalculator struct {
	DB *gorm.DB // central 计费连接，由进程管理
}

// Calculate 按计费证据生成并保存结果，首次回执锁行后比对完整证据。
// 相同请求复用计算号和投递载荷；计算、回执、投递同事务，不在此扣款。
func (s FeeCalculator) Calculate(ctx context.Context, source Source) (Result, error) {
	if source.ChargeOrderID == 0 || source.OrderNo == "" || source.UserID == 0 || source.Rule.StationID == 0 {
		return Result{}, ErrBillingConflict
	}
	fee, err := PriceSource(source)
	if err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(source)
	if err != nil {
		return Result{}, err
	}
	out := Result{CalculationNo: CalculationNumber(source.OrderNo, source.ChargeOrderID), Source: source, ActualFee: fee}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("fee_receipt").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).Create(map[string]any{"charge_order_id": source.ChargeOrderID, "source_json": string(payload)}).Error; err != nil {
			return err
		}
		var receipt struct {
			SourceJSON    []byte  // 首次计费证据 JSON
			CalculationID *uint64 // 计算主键，尚未计算为空
			CalculationNo *string // 计算编号，尚未计算为空
		}
		if err := tx.Table("fee_receipt").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", source.ChargeOrderID).Take(&receipt).Error; err != nil {
			return err
		}
		var previous Source
		if json.Unmarshal(receipt.SourceJSON, &previous) != nil {
			return ErrBillingConflict
		}
		prior, _ := json.Marshal(previous)
		if string(prior) != string(payload) {
			return ErrBillingConflict
		}
		if receipt.CalculationID != nil {
			var delivery struct {
				PayloadJSON []byte // 完整 Result 投递载荷
			}
			if err := tx.Table("fee_delivery").Where("charge_order_id=?", source.ChargeOrderID).Take(&delivery).Error; err != nil {
				return err
			}
			return json.Unmarshal(delivery.PayloadJSON, &out)
		}
		month := time.Date(source.Meter.EndedAt.Year(), source.Meter.EndedAt.Month(), 1, 0, 0, 0, 0, time.UTC)
		resultJSON, _ := json.Marshal(out)
		row := map[string]any{"calculation_no": out.CalculationNo, "order_no": source.OrderNo, "charge_order_id": source.ChargeOrderID, "user_id": source.UserID, "station_id": source.Rule.StationID, "pricing_rule_id": source.Rule.ID, "pricing_rule_version": source.Rule.Version, "charged_kwh": fmt.Sprintf("%d.%03d", source.Meter.ChargedWh/1000, source.Meter.ChargedWh%1000), "charged_seconds": source.Meter.ChargedSeconds, "electric_cents": fee.ElectricCents, "service_cents": fee.ServiceCents, "total_cents": fee.TotalCents, "breakdown_json": string(resultJSON), "created_month": month}
		if err := tx.Table("fee_calculation").Create(row).Error; err != nil {
			return err
		}
		var id uint64
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		if err := tx.Table("fee_receipt").Where("charge_order_id=?", source.ChargeOrderID).Updates(map[string]any{"calculation_id": id, "calculation_no": out.CalculationNo}).Error; err != nil {
			return err
		}
		return tx.Table("fee_delivery").Create(map[string]any{"charge_order_id": source.ChargeOrderID, "payload_json": string(resultJSON)}).Error
	})
	return out, err
}

// PriceSource 根据下单价格和计量计算费用；人工 FinalFee 须带 ReviewID，且不得超过套餐金额。
func PriceSource(source Source) (charging.Fee, error) {
	if source.FinalFee != nil {
		f := *source.FinalFee
		if source.ReviewID == "" || source.Offer == nil || f.ElectricCents < 0 || f.ServiceCents < 0 || f.TotalCents != f.ElectricCents+f.ServiceCents || f.TotalCents > source.Offer.PriceCents {
			return charging.Fee{}, ErrBillingConflict
		}
		return f, nil
	}
	return charging.PriceOfferActual(source.Rule, source.Offer, source.Meter)
}

// MarkDelivered 在确认资金已幂等入账后，将投递和待核实队列一起标记完成。
func (s FeeCalculator) MarkDelivered(ctx context.Context, id uint64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("fee_delivery").Where("charge_order_id=?", id).Updates(map[string]any{"delivered": true, "delivered_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error; err != nil {
			return err
		}
		return tx.Table("manual_fee_review").Where("charge_order_id=? AND status='pending'", id).Updates(map[string]any{"status": "resolved", "resolved_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error
	})
}

// CalculationID 读取费用回执对应的计价主键；主键为空返回 ErrBillingConflict。
func (s FeeCalculator) CalculationID(ctx context.Context, chargeOrderID uint64) (uint64, error) {
	var row struct {
		CalculationID *uint64 `gorm:"column:calculation_id"` // 计算主键，尚未计算为空
	}
	if err := s.DB.WithContext(ctx).Table("fee_receipt").Select("calculation_id").Where("charge_order_id = ?", chargeOrderID).Take(&row).Error; err != nil {
		return 0, err
	}
	if row.CalculationID == nil {
		return 0, ErrBillingConflict
	}
	return *row.CalculationID, nil
}
