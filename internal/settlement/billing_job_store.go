package settlement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	chargingstore "github.com/ChargePilot2026/charge-pilot/internal/card"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	pay "github.com/ChargePilot2026/charge-pilot/internal/payment"
	walletpkg "github.com/ChargePilot2026/charge-pilot/internal/wallet"

	"log"
	"reflect"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BillingOrders struct{ DB *gorm.DB }

func (s BillingOrders) Due(ctx context.Context) ([]uint64, error) {
	ids := []uint64{}
	err := s.DB.WithContext(ctx).Table("charge_billing_job").Where("status='pending' AND next_attempt_at<=UTC_TIMESTAMP(3)").Order("next_attempt_at,charge_order_id").Limit(50).Pluck("charge_order_id", &ids).Error
	return ids, err
}
func (s BillingOrders) Defer(ctx context.Context, id uint64, status, reason string) error {
	return s.DB.WithContext(ctx).Table("charge_billing_job").Where("charge_order_id=? AND status<>'done'", id).Updates(map[string]any{"status": status, "last_error": reason, "attempts": gorm.Expr("attempts+1"), "next_attempt_at": time.Now().UTC().Add(time.Minute)}).Error
}
func (s BillingOrders) Read(ctx context.Context, id uint64) (Source, error) {
	return readBillingSource(s.DB.WithContext(ctx), id)
}
func readBillingSource(db *gorm.DB, id uint64) (Source, error) {
	source, err := readOriginalBillingSource(db, id)
	if err != nil {
		return source, err
	}
	if source.FinalFee != nil {
		return source, nil
	}
	var review MeterReview
	result := db.Where("charge_order_id=? AND status='approved'", id).Order("id DESC").Limit(1).Find(&review)
	if result.Error != nil {
		return source, result.Error
	}
	if result.RowsAffected == 0 {
		return source, nil
	}
	var original, corrected Source
	if json.Unmarshal(review.OriginalJSON, &original) != nil || json.Unmarshal(review.CorrectedJSON, &corrected) != nil {
		return source, ErrBillingConflict
	}
	current, _ := json.Marshal(source)
	saved, _ := json.Marshal(original)
	if string(current) != string(saved) {
		return source, ErrBillingConflict
	}
	source.Meter.Segments = corrected.Meter.Segments
	current, _ = json.Marshal(source)
	saved, _ = json.Marshal(corrected)
	if string(current) != string(saved) {
		return source, ErrBillingConflict
	}
	return source, nil
}
func readOriginalBillingSource(db *gorm.DB, id uint64) (Source, error) {
	var order orderstore.ChargeOrderRecord
	if err := db.Where("id=? AND ended_at IS NOT NULL AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
		return Source{}, err
	}
	if !order.StartedAt.Valid || (order.Status != "completed" && order.Status != "refunding" && order.Status != "refunded") {
		return Source{}, ErrBillingConflict
	}
	var snapshot orderstore.ChargePricingSnapshotRecord
	var end orderstore.EndReceiptRecord
	if err := db.Where("charge_order_id=?", id).Take(&snapshot).Error; err != nil {
		return Source{}, err
	}
	if err := db.Where("charge_order_id=?", id).Take(&end).Error; err != nil {
		return Source{}, err
	}
	var contract struct {
		Rule  charging.Rule   `json:"rule"`
		Offer *charging.Offer `json:"offer"`
	}
	var meter charging.EndMeter
	if json.Unmarshal(snapshot.PricingSnapshot, &contract) != nil || contract.Rule.Spec.Scheme == nil || contract.Offer == nil || json.Unmarshal(end.MeterJSON, &meter) != nil || !meter.EndedAt.Equal(order.EndedAt.Time) {
		return Source{}, ErrBillingConflict
	}
	source := Source{ChargeOrderID: id, OrderNo: order.OrderNo, UserID: order.UserID, Rule: contract.Rule, Offer: contract.Offer, Meter: charging.ActualMeter{StartedAt: order.StartedAt.Time, EndedAt: meter.EndedAt, ChargedWh: meter.ChargedWh, ChargedSeconds: meter.ChargedSeconds, Segments: meter.Segments}}
	var card chargingstore.CardCharge
	if err := db.Where("charge_order_id=?", id).Find(&card).Error; err != nil {
		return source, err
	}
	if card.ChargeOrderID != 0 && source.Offer != nil {
		base := *source.Offer
		if !base.Valid() || !base.ServerDuration || card.PaidCents <= 0 || card.PaidCents%base.PriceCents != 0 || card.PaidCents/base.PriceCents > 65535 || card.PurchasedMinutes > card.MaxMinutes {
			return source, ErrBillingConflict
		}
		base.PurchaseCount = uint16(card.PaidCents / base.PriceCents)
		base.PriceCents = card.PaidCents
		base.DurationMinutes = card.PurchasedMinutes
		source.Offer = &base
		var pending int64
		if err := db.Model(&chargingstore.CardOperation{}).Where("charge_order_id=? AND status='confirming'", id).Count(&pending).Error; err != nil {
			return source, err
		}
		if pending > 0 {
			return source, chargingstore.ErrCardOperation
		}
	}
	var cutoff struct {
		CutoffAt      time.Time
		Reason        string
		ElectricCents *int64
		ServiceCents  *int64
	}
	if err := db.Table("charge_billing_cutoff").Where("charge_order_id=?", id).Find(&cutoff).Error; err != nil {
		return source, err
	}
	if contract.Offer != nil && contract.Offer.Mode == "amount" && contract.Rule.Spec.TimeCharge != nil {
		limit := order.StartedAt.Time.Add(time.Duration(contract.Rule.Spec.TimeCharge.MaxMinutes) * time.Minute)
		if cutoff.CutoffAt.IsZero() || limit.Before(cutoff.CutoffAt) {
			cutoff.CutoffAt = limit
		}
	}
	if !cutoff.CutoffAt.IsZero() && cutoff.CutoffAt.Before(source.Meter.EndedAt) {
		clipped, err := charging.CutoffMeter(contract.Rule.Spec, source.Meter, cutoff.CutoffAt)
		if err != nil {
			source.Meter.ReviewRequired = true
		} else {
			source.Meter = clipped
		}
	}
	if cutoff.Reason == "budget_exhausted" && cutoff.ElectricCents != nil && cutoff.ServiceCents != nil && source.Offer != nil && source.Offer.Mode == "amount" {
		e, s := *cutoff.ElectricCents, *cutoff.ServiceCents
		if e < 0 || s < 0 || e+s != source.Offer.PriceCents {
			return source, ErrBillingConflict
		}
		source.FinalFee = &charging.Fee{ElectricCents: e, ServiceCents: s, TotalCents: e + s}
		source.ReviewID = "budget:" + cutoff.CutoffAt.UTC().Format(time.RFC3339Nano)
	}
	var manual struct {
		RequestID     string
		ElectricCents int64
		ServiceCents  int64
	}
	if err := db.Table("charge_manual_settlement").Where("charge_order_id=?", id).Find(&manual).Error; err != nil {
		return source, err
	}
	if manual.RequestID != "" {
		source.FinalFee = &charging.Fee{ElectricCents: manual.ElectricCents, ServiceCents: manual.ServiceCents, TotalCents: manual.ElectricCents + manual.ServiceCents}
		source.ReviewID = manual.RequestID
	}
	return source, nil
}
func (s BillingOrders) Apply(ctx context.Context, result Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var reference orderstore.ChargeOrderRecord
	if err := s.DB.WithContext(ctx).Select("id,payment_order_id").Where("id=? AND deleted_at IS NULL", result.Source.ChargeOrderID).Take(&reference).Error; err != nil {
		return err
	}
	if !reference.PaymentOrderID.Valid {
		return ErrBillingConflict
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var wallet walletpkg.Row
		var paymentMethod string
		if err := tx.Table("payment_order").Where("id=?", reference.PaymentOrderID.Int64).Pluck("pay_method", &paymentMethod).Error; err != nil {
			return err
		}
		if paymentMethod == "balance" {
			var err error
			wallet, err = walletpkg.Lock(tx, result.Source.UserID)
			if err != nil {
				return err
			}
		}
		var payment pay.PaymentOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", reference.PaymentOrderID.Int64).Take(&payment).Error; err != nil {
			return err
		}
		var order orderstore.ChargeOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", reference.ID).Take(&order).Error; err != nil {
			return err
		}
		if !order.PaymentOrderID.Valid || uint64(order.PaymentOrderID.Int64) != payment.ID || (payment.PayMethod != "wechat" && payment.PayMethod != "balance") || (payment.Status != "paid" && payment.Status != "partial_refunded" && payment.Status != "refunded") || payment.PaidCents != payment.TotalCents || payment.BizID != order.ID || payment.BizType != "charge" || payment.UserID != order.UserID || payment.PaidCents <= 0 || payment.RefundedCents < 0 || payment.RefundedCents > payment.PaidCents {
			return ErrBillingConflict
		}
		source, err := readBillingSource(tx, order.ID)
		if err != nil {
			return err
		}
		original, _ := json.Marshal(source)
		provided, _ := json.Marshal(result.Source)
		if string(original) != string(provided) {
			return ErrBillingConflict
		}
		fee, err := PriceSource(source)
		if err != nil || !reflect.DeepEqual(fee, result.ActualFee) || result.CalculationNo != CalculationNumber(order.OrderNo, order.ID) {
			return ErrBillingConflict
		}
		var existing struct{ ResultJSON []byte }
		previous := tx.Table("charge_fee_receipt").Where("charge_order_id=?", order.ID).Find(&existing)
		if previous.Error != nil {
			return previous.Error
		}
		if previous.RowsAffected > 0 {
			var old Result
			if json.Unmarshal(existing.ResultJSON, &old) != nil {
				return ErrBillingConflict
			}
			saved, _ := json.Marshal(old)
			if string(saved) != string(encoded) {
				return ErrBillingConflict
			}
			return tx.Table("charge_billing_job").Where("charge_order_id=?", order.ID).Updates(map[string]any{"status": "done", "last_error": nil}).Error
		}
		if result.TotalCents > payment.PaidCents-payment.RefundedCents {
			return ErrBillingConflict
		}
		shortfall := int64(0)
		if err := tx.Table("charge_fee_receipt").Create(map[string]any{"charge_order_id": order.ID, "calculation_no": result.CalculationNo, "result_json": string(encoded), "shortfall_cents": shortfall}).Error; err != nil {
			return err
		}
		if err := tx.Model(&orderstore.ChargeOrderRecord{}).Where("id=?", order.ID).Updates(map[string]any{"electric_cents": result.ElectricCents, "service_cents": result.ServiceCents, "total_cents": result.TotalCents}).Error; err != nil {
			return err
		}
		// 费用计算完成并写入回执后求值订单活动，确保金额门槛准确。
		// 重放结算在回执检查时提前返回，不重复发券。
		applyOrderCampaigns(tx, order, result.TotalCents)
		// 聚合读必须用加锁读。上面那把 payment_order 行锁只串行化同样走
		// Apply 的事务，挡不住并发的 ManualCreate 刚落库的退款单：
		// 本事务的快照在第 178 行那��普通读（Pluck pay_method）时就已建立，
		// 早于第 190 行拿到行锁，于是这里的 SUM 少算占用额，
		// surplus 被高估，多退钱进用户钱包。FOR UPDATE 是当前读，看得到最新已提交版本。
		var reserved int64
		if err := tx.Model(&pay.RefundRecord{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("COALESCE(SUM(refund_cents),0)").
			Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", payment.ID).
			Scan(&reserved).Error; err != nil {
			return err
		}
		surplus := payment.PaidCents - result.TotalCents - payment.RefundedCents - reserved
		if surplus > 0 {
			if payment.PayMethod == "balance" {
				if err := RefundBalanceInTx(tx, order, &wallet, surplus, fmt.Sprintf("CARD-R%020d", order.ID), "刷卡累计付款未使用部分退款"); err != nil {
					return err
				}
				if err := tx.Model(&orderstore.ChargeOrderRecord{}).Where("id=?", order.ID).Update("status", "refunded").Error; err != nil {
					return err
				}
			} else {
				refund := pay.RefundRecord{RefundNo: fmt.Sprintf("FEE-R%020d", order.ID), PaymentOrderID: payment.ID, UserID: order.UserID, BizType: "charge", BizID: order.ID, RefundCents: surplus, Reason: sql.NullString{String: "实际计费后的预付差额", Valid: true}, Status: "pending", ExecutionPolicy: "automatic", CreatedMonth: dbutil.MonthStart()}
				if err := tx.Create(&refund).Error; err != nil {
					return err
				}
				if err := tx.Model(&orderstore.ChargeOrderRecord{}).Where("id=? AND status='completed'", order.ID).Update("status", "refunding").Error; err != nil {
					return err
				}
				eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("billing-refund:"+result.CalculationNo)).String()
				payload, _ := json.Marshal(map[string]any{"event_id": eventID, "refund_no": refund.RefundNo, "charge_order_id": order.ID, "refund_cents": surplus})
				if err := tx.Create(&ops.EventOutboxRecord{EventID: eventID, Stream: "refund_required_stream", EnvelopeJSON: payload}).Error; err != nil {
					return err
				}
			}
		}
		eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("billing-complete:"+result.CalculationNo)).String()
		if err := tx.Create(&orderstore.ChargeEventLogRecord{ChargeOrderID: order.ID, EventID: eventID, Event: "fee_calculated", Actor: "billing", Detail: fmt.Sprintf("electric=%d service=%d total=%d shortfall=%d", result.ElectricCents, result.ServiceCents, result.TotalCents, shortfall), OccurredAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		payload, err := json.Marshal(settlementNotice(eventID, order, source.Rule.Spec.Display, result.ActualFee, payment.PaidCents-result.TotalCents))
		if err != nil {
			return err
		}
		if err := tx.Create(&ops.EventOutboxRecord{EventID: eventID, Stream: "charge_settled_stream", EnvelopeJSON: payload}).Error; err != nil {
			return err
		}
		return tx.Table("charge_billing_job").Where("charge_order_id=?", order.ID).Updates(map[string]any{"status": "done", "last_error": nil}).Error
	})
}

// Notification consumers receive final amounts only after durable settlement.
// Visibility comes from the frozen order, never the current station template.
func settlementNotice(eventID string, order orderstore.ChargeOrderRecord, display charging.Display, fee charging.Fee, refund int64) map[string]any {
	data := map[string]any{"event_id": eventID, "charge_order_id": order.ID, "order_no": order.OrderNo, "user_id": order.UserID, "status": "settled", "display": display}
	if display.ShowFeeOnEnd {
		data["total_cents"], data["refund_cents"] = fee.TotalCents, refund
		if display.ShowFeeSplit {
			data["electric_cents"], data["service_cents"] = fee.ElectricCents, fee.ServiceCents
		}
	}
	return data
}

// applyOrderCampaigns 根据已结算订单求值门槛券和节日券。
// 规则不适用时跳过；规则错误仅记录日志，不中止资金结算。订单事件键保证重复求值不重复发券。
func applyOrderCampaigns(tx *gorm.DB, order orderstore.ChargeOrderRecord, totalCents int64) {
	now := time.Now().UTC()
	for _, trigger := range []string{"threshold_redeem", "holiday"} {
		if _, err := marketing.ApplyActivityRules(tx, marketing.ActivityEvent{
			TriggerType: trigger,
			UserID:      order.UserID,
			EventKey:    marketing.ActivityEventKeyForOrder(order.OrderNo),
			AmountCents: totalCents,
			Now:         now,
		}); err != nil && !errors.Is(err, marketing.ErrActivityNotApplicable) {
			log.Printf("ACTIVITY RULE FAILED trigger=%s order=%s user=%d: %v", trigger, order.OrderNo, order.UserID, err)
		}
	}
}

// EnqueueManualFeeReview 把一笔算不出费用的订单排进人工核实队列。
//
// 幂等键是 charge_order_id：同一笔订单重复入队只更新不新增。用 no-op upsert
// 而不是"先查再插"，两个并发的入队请求会同时查到"没有"然后都去插，靠唯一键
// 把其中一个挡下来。
func EnqueueManualFeeReview(tx *gorm.DB, in ManualFeeReviewInput) error {
	return tx.Table("manual_fee_review").
		Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).
		Create(map[string]any{
			"charge_order_id": in.ChargeOrderID, "order_no": in.OrderNo, "reason": in.Reason,
			"energy_wh": in.EnergyWh, "created_month": in.CreatedMonth, "source_json": in.SourceJSON,
		}).Error
}

// ManualFeeReviewInput 是一次人工核实入队的内容。
type ManualFeeReviewInput struct {
	ChargeOrderID uint64
	OrderNo       string
	Reason        string
	EnergyWh      uint32
	CreatedMonth  string
	SourceJSON    string
}

// FeeDeliveryRow 是一笔待送达的计费结果。送达失败会推迟重试而不是丢弃。
type FeeDeliveryRow struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`
	PayloadJSON   []byte `gorm:"column:payload_json"`
}

// feeDeliveryBatch 一次取多少笔待送达的计费结果。
const feeDeliveryBatch = 50

// ListPendingFeeDeliveries 返回到期待送达的计费结果，按计划时间与订单排序。
func ListPendingFeeDeliveries(ctx context.Context, db *gorm.DB) ([]FeeDeliveryRow, error) {
	rows := []FeeDeliveryRow{}
	err := db.WithContext(ctx).Table("fee_delivery").
		Where("delivered=0 AND scheduled_at<=UTC_TIMESTAMP(3)").
		Order("scheduled_at,charge_order_id").Limit(feeDeliveryBatch).Find(&rows).Error
	return rows, err
}

// RescheduleFeeDelivery 把送达失败的计费结果推后一分钟并累加尝试次数。
// 用"推后"而不是删掉：投递可能只是暂时失败，丢掉就等于这单永远不出账。
func RescheduleFeeDelivery(db *gorm.DB, orderID uint64, retryAt time.Time) error {
	return db.Table("fee_delivery").Where("charge_order_id=? AND delivered=0", orderID).
		Updates(map[string]any{"attempts": gorm.Expr("attempts+1"), "scheduled_at": retryAt}).Error
}

// EnqueueChargeBilling 把结束计量排队。计费是异步的：结束回执只负责确认
// 事实并排队，费用由 central 的结算用例算完再落账单。
func EnqueueChargeBilling(tx *gorm.DB, chargeOrderID uint64) error {
	return tx.Table("charge_billing_job").Create(map[string]any{"charge_order_id": chargeOrderID}).Error
}

// BillingJobState 是计费任务的排队状态。取不到表示尚未排队。
type BillingJobState struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`
	Status        string `json:"status" gorm:"column:status"`
}

// FindBillingJob 返回订单的计费排队状态。
func FindBillingJob(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (BillingJobState, error) {
	var row BillingJobState
	err := db.WithContext(ctx).Table("charge_billing_job").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}
