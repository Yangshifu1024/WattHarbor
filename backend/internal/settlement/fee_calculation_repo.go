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

// ErrBillingConflict 表示原费用证据、人工裁决或持久结果不一致，不可覆盖后重新收费。
var ErrBillingConflict = errors.New("billing source conflicts with persisted calculation")

// Source 是订单原价格、计量与人工裁决的完整计费证据，重放逐内容比对。
type Source struct {
	FinalFee      *charging.Fee        `json:"final_fee,omitempty"` // 已持久审计的最终金额；自动计费为空
	ReviewID      string               `json:"review_id,omitempty"` // 人工最终金额请求身份；未裁决为空
	ChargeOrderID uint64               `json:"charge_order_id"`     // 原充电订单主键
	OrderNo       string               `json:"order_no"`            // 原充电单号，派生稳定计费编号
	UserID        uint64               `json:"user_id"`             // 原订单所属用户，内部证据保留历史数值编码
	Rule          charging.Rule        `json:"rule"`                // 下单时完整计价规则快照
	Offer         *charging.Offer      `json:"offer,omitempty"`     // 下单套餐，历史非套餐可空
	Meter         charging.ActualMeter `json:"meter"`               // 截止时已验证计量，Wh／秒及原分段，不使用当前遥测
}

// Result 是以 Source 原证据计算并持久化的费用，投递重放保持原编号和分值。
type Result struct {
	CalculationNo      string `json:"calculation_no"` // 原计费编号
	Source             Source `json:"source"`         // 原证据，不以当前规则替换
	charging.ActualFee        // 领域确定的电费、服务费、总费及明细，金额为分
}

// FeeCalculator 调用领域计价并原子保存计算回执与可恢复投递载荷。
type FeeCalculator struct {
	DB *gorm.DB // central 计费连接，由进程管理
}

// Calculate 在 ctx 下按 source 计价并保存原结果；返回稳定 Result 或证据、计量、价格及存储错误。
// 原费用凭据首写后锁定并比对完整证据，相同请求复用已有投递载荷，不重复生成费用记录。
// 计算、回执及待投递载荷同事务；本方法不扣款，取消或写入失败要求回滚。
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
			CalculationID *uint64 // 原计算主键，尚未计算为空
			CalculationNo *string // 原计算编号，尚未计算为空
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
				PayloadJSON []byte // 完整原 Result 投递载荷
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

// PriceSource 按 source 原规则和计量返回领域费用；人工 FinalFee 必须带 ReviewID 且不超过原套餐。
// 返回证据、计量或价格错误，失败结果不可使用；无 IO，不在此信任客户端金额或执行扣款。
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

// MarkDelivered 在 ctx 下将订单 id 的原投递标为已确认，同时解决其待核实队列。
// 调用方必须已成功幂等应用资金；两项更新同事务，返回写入错误可重试，不重新扣款。
func (s FeeCalculator) MarkDelivered(ctx context.Context, id uint64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("fee_delivery").Where("charge_order_id=?", id).Updates(map[string]any{"delivered": true, "delivered_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error; err != nil {
			return err
		}
		return tx.Table("manual_fee_review").Where("charge_order_id=? AND status='pending'", id).Updates(map[string]any{"status": "resolved", "resolved_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error
	})
}

// CalculationID 从计费回执读取 fee_calculation 的实际主键，供结算台账引用。
// ctx 取消查询，chargeOrderID 为原订单；返回主键或读取错误，空主键返回 ErrBillingConflict，不写入。
func (s FeeCalculator) CalculationID(ctx context.Context, chargeOrderID uint64) (uint64, error) {
	var row struct {
		CalculationID *uint64 `gorm:"column:calculation_id"` // 原计算主键，尚未计算为空
	}
	if err := s.DB.WithContext(ctx).Table("fee_receipt").Select("calculation_id").Where("charge_order_id = ?", chargeOrderID).Take(&row).Error; err != nil {
		return 0, err
	}
	if row.CalculationID == nil {
		return 0, ErrBillingConflict
	}
	return *row.CalculationID, nil
}
