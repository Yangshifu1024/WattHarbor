package settlement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	chargingstore "wattharbor/internal/card"
	carddomain "wattharbor/internal/domain/card"
	"wattharbor/internal/domain/money"
	"wattharbor/internal/marketing"
	"wattharbor/internal/ops"
	orderstore "wattharbor/internal/order"
	pay "wattharbor/internal/payment"
	walletpkg "wattharbor/internal/wallet"

	"reflect"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/platform/dbutil"
)

// BillingOrders 查询计费任务、订单价格和计量，保存费用并退回未使用金额。
type BillingOrders struct {
	DB *gorm.DB // central 任务及资金连接，由进程管理
}

// Due 读取最多 50 笔到期 pending 任务，按执行时间、订单 ID 排序。
// 读取不认领任务，Apply 通过结算回执保证入账幂等。
func (s BillingOrders) Due(ctx context.Context) ([]BillingTask, error) {
	ids := []BillingTask{}
	err := s.DB.WithContext(ctx).Table("charge_billing_job").Select("charge_order_id,generation").Where("status=? AND next_attempt_at<=UTC_TIMESTAMP(3)", BillingJobPending).Order("next_attempt_at,charge_order_id").Limit(50).Find(&ids).Error
	return ids, err
}

// Defer 在 ctx 管理的短事务内核对 task 的 pending 状态及预期代际，保存脱敏 reason 和 status。
// status 只允许 pending/manual_review；人工队列入队同事务，RC 重读来源并执行纯计价规则。
// 失配返回 ErrStaleBillingTask，调用方停止旧执行；其他错误回滚，不跨 Calculate/Apply 持有任务锁。
// status 是 charge_billing_job.status 的待写取值；进入本方法即已是列类型，
// 判定与写入一律使用具名常量，不接受任意裸串。
func (s BillingOrders) Defer(ctx context.Context, task BillingTask, target BillingJobStatus, reason string) error {
	if target != BillingJobPending && target != BillingJobManualReview {
		return ErrBillingConflict
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct {
			Status     BillingJobStatus // 当前状态，仅 pending 可由本轮更新
			Generation uint64           // 人工决策及状态转换代际
		}
		if err := tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", task.ChargeOrderID).Take(&job).Error; err != nil {
			return err
		}
		if job.Status != BillingJobPending || job.Generation != task.Generation {
			return ErrStaleBillingTask
		}
		if target == BillingJobManualReview {
			source, err := readBillingSource(tx, task.ChargeOrderID)
			if err != nil {
				return err
			}
			_, err = PriceSource(source)
			if err == nil {
				return ErrStaleBillingTask
			}
			if !errors.Is(err, charging.ErrMeterReview) && !errors.Is(err, charging.ErrInvalidPricing) {
				return err
			}
			payload, err := json.Marshal(source)
			if err != nil {
				return err
			}
			if err := EnqueueManualFeeReview(tx, ManualFeeReviewInput{ChargeOrderID: task.ChargeOrderID, OrderNo: source.OrderNo, Reason: reason, EnergyWh: source.Meter.ChargedWh, CreatedMonth: time.Now().UTC().Format("2006-01"), SourceJSON: string(payload)}); err != nil {
				return err
			}
		}
		res := tx.Table("charge_billing_job").Where("charge_order_id=? AND status=? AND generation=?", task.ChargeOrderID, BillingJobPending, task.Generation).
			Updates(map[string]any{"status": target, "generation": gorm.Expr("generation+1"), "last_error": reason, "attempts": gorm.Expr("attempts+1"), "next_attempt_at": time.Now().UTC().Add(time.Minute)})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrStaleBillingTask
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// Read 读取下单价格、结束计量、截止点及人工核实结果；不补造缺失计量。
func (s BillingOrders) Read(ctx context.Context, id uint64) (Source, error) {
	return readBillingSource(s.DB.WithContext(ctx), id)
}

// readBillingSource 读取价格和计量；已有最终金额时直接返回，否则使用经批准且原数据未变的修正分段。
func readBillingSource(db *gorm.DB, id uint64) (Source, error) {
	source, err := readOriginalBillingSource(db, id)
	if err != nil {
		return source, err
	}
	if source.FinalFee != nil {
		return source, nil
	}
	var review MeterReview
	result := db.Where("charge_order_id=? AND status=?", id, MeterReviewApproved).Order("id DESC").Limit(1).Find(&review)
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

// readOriginalBillingSource 读取已结束订单的价格快照、结束计量、刷卡累计购买和计费截止记录。
// 截止点无法用真实计量确定时保留 ReviewRequired；卡操作未确认时拒绝计算。
func readOriginalBillingSource(db *gorm.DB, id uint64) (Source, error) {
	var order orderstore.ChargeOrderRecord
	if err := db.Where("id=? AND ended_at IS NOT NULL AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
		return Source{}, err
	}
	if !order.StartedAt.Valid || !invoiceableStatus(order.Status) {
		return Source{}, ErrBillingConflict
	}
	var snapshot orderstore.ChargePricingSnapshotRecord
	var end endReceiptView
	if err := db.Where("charge_order_id=?", id).Take(&snapshot).Error; err != nil {
		return Source{}, err
	}
	if err := db.Where("charge_order_id=?", id).Take(&end).Error; err != nil {
		return Source{}, err
	}
	var contract struct {
		Rule  charging.Rule   `json:"rule"`  // 下单时完整规则快照
		Offer *charging.Offer `json:"offer"` // 下单时套餐，当前计费必须存在
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
		if err := db.Model(&chargingstore.CardOperation{}).Where("charge_order_id=? AND status=?", id, carddomain.CardStatusConfirming).Count(&pending).Error; err != nil {
			return source, err
		}
		if pending > 0 {
			return source, chargingstore.ErrCardOperation
		}
	}
	var cutoff struct {
		CutoffAt      time.Time // 首次计费截止 UTC 时刻，零表示未冻结
		Reason        string    // 停止原因；预算耗尽时为 budget_exhausted
		ElectricCents *int64    // 已冻结预算电费，分，无预算冻结为空
		ServiceCents  *int64    // 已冻结预算服务费，分，无预算冻结为空
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
		RequestID     string // 人工最终金额请求 UUID
		ElectricCents int64  // 人工确定电费，分
		ServiceCents  int64  // 人工确定服务费，分
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

// Apply 核对 result 的订单、用户、价格、计量和费用，再保存结算结果；重复调用不重复退款。
// 资金、费用回执、余款退款、订单、任务和 Outbox 同事务提交，错误整体回滚。
// 余额支付按钱包→支付→订单加锁；在途退款用当前读，重放不重复退余款或发券。
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
		if !order.PaymentOrderID.Valid || uint64(order.PaymentOrderID.Int64) != payment.ID || (payment.PayMethod != "wechat" && payment.PayMethod != "balance") || (!oneOf(payment.Status, money.PaidLikeStatuses)) || payment.PaidCents != payment.TotalCents || payment.BizID != order.ID || payment.BizType != money.PaymentBizCharge || payment.UserID != order.UserID || payment.PaidCents <= 0 || payment.RefundedCents < 0 || payment.RefundedCents > payment.PaidCents {
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
		var existing struct {
			ResultJSON []byte // 已入账的完整费用回执
		}
		// 支付方式查询可能建立旧快照；费用回执用当前读，确保锁等待期间入账后仍可重放。
		previous := tx.Table("charge_fee_receipt").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", order.ID).Find(&existing)
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
			return tx.Table("charge_billing_job").Where("charge_order_id=?", order.ID).Updates(map[string]any{"status": BillingJobDone, "last_error": nil}).Error
		}
		if result.TotalCents > payment.PaidCents-payment.RefundedCents {
			return ErrBillingConflict
		}
		shortfall := int64(0)
		if err := tx.Table("charge_fee_receipt").Create(map[string]any{"charge_order_id": order.ID, "calculation_no": result.CalculationNo, "result_json": string(encoded), "shortfall_cents": shortfall}).Error; err != nil {
			return err
		}
		if err := orderstore.SetSettledFeeInTx(tx, order.ID, result.ElectricCents, result.ServiceCents, result.TotalCents); err != nil {
			return err
		}
		// 费用保存后再检查订单活动金额门槛；重放提前返回，不重复发券。
		if err := applyOrderCampaigns(tx, order, result.TotalCents); err != nil {
			return err
		}
		// 支付锁前的普通查询可能建立旧 RR 快照。
		// 在途退款用当前读，避免漏掉并发提交的退款而高估可退金额。
		var reserved int64
		if err := tx.Model(&RefundRecord{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("COALESCE(SUM(refund_cents),0)").
			Where("payment_order_id=? AND status IN ? AND deleted_at IS NULL", payment.ID, unsettledRefundStatuses()).
			Scan(&reserved).Error; err != nil {
			return err
		}
		surplus := payment.PaidCents - result.TotalCents - payment.RefundedCents - reserved
		if surplus > 0 {
			if payment.PayMethod == "balance" {
				source := BalanceRefundSource{UserID: order.UserID, OrderID: order.ID, PaymentOrderID: order.PaymentOrderID.Int64}
				if err := RefundBalanceInTx(tx, source, &wallet, surplus, fmt.Sprintf("CARD-R%020d", order.ID), "刷卡累计付款未使用部分退款"); err != nil {
					return err
				}
				if err := orderstore.SetStatusRefunded(tx, order.ID); err != nil {
					return err
				}
			} else {
				refund := RefundRecord{RefundNo: fmt.Sprintf("FEE-R%020d", order.ID), PaymentOrderID: payment.ID, UserID: order.UserID, BizType: money.PaymentBizCharge, BizID: order.ID, RefundCents: surplus, Reason: sql.NullString{String: "实际计费后的预付差额", Valid: true}, Status: money.RefundPending, ExecutionPolicy: money.ExecutionAutomatic, CreatedMonth: dbutil.MonthStart()}
				if err := tx.Create(&refund).Error; err != nil {
					return err
				}
				if err := orderstore.RequestChannelRefundInTx(tx, order.ID); err != nil {
					return err
				}
				eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("billing-refund:"+result.CalculationNo)).String()
				payload, _ := json.Marshal(map[string]any{"event_id": eventID, "refund_no": refund.RefundNo, "charge_order_id": order.ID, "refund_cents": surplus})
				if err := ops.AppendEventInTx(tx, ops.OutboxEvent{EventID: eventID, Stream: "refund_required_stream", EnvelopeJSON: payload}); err != nil {
					return err
				}
			}
		}
		eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("billing-complete:"+result.CalculationNo)).String()
		if err := orderstore.AppendEventInTx(tx, orderstore.ChargeEventLogRecord{ChargeOrderID: order.ID, EventID: eventID, Event: "fee_calculated", Actor: "billing", Detail: fmt.Sprintf("electric=%d service=%d total=%d shortfall=%d", result.ElectricCents, result.ServiceCents, result.TotalCents, shortfall), OccurredAt: time.Now().UTC()}); err != nil {
			return err
		}
		payload, err := json.Marshal(settlementNotice(eventID, order, source.Rule.Spec.Display, result.ActualFee, payment.PaidCents-result.TotalCents))
		if err != nil {
			return err
		}
		if err := ops.AppendEventInTx(tx, ops.OutboxEvent{EventID: eventID, Stream: "charge_settled_stream", EnvelopeJSON: payload}); err != nil {
			return err
		}
		return tx.Table("charge_billing_job").Where("charge_order_id=?", order.ID).Updates(map[string]any{"status": BillingJobDone, "last_error": nil}).Error
	})
}

// settlementNotice 用订单编号、下单展示开关和结算金额生成通知；金额单位分。
// 遵守下单时保存的展示开关；调用方在资金结算后追加 Outbox。
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

// applyOrderCampaigns 按已结算金额发放门槛券和节日券，金额单位为分。
// 不适用规则跳过；存储失败须连同资金回滚，订单事件键防止重复发券。
func applyOrderCampaigns(tx *gorm.DB, order orderstore.ChargeOrderRecord, totalCents int64) error {
	now := time.Now().UTC()
	for _, trigger := range []marketing.TriggerType{marketing.TriggerThresholdRedeem, marketing.TriggerHoliday} {
		if _, err := (marketing.ActivityService{}).ApplyInTx(tx, marketing.ActivityEvent{
			TriggerType: trigger,
			UserID:      order.UserID,
			EventKey:    marketing.ActivityEventKeyForOrder(order.OrderNo),
			AmountCents: totalCents,
			Now:         now,
		}); err != nil && !errors.Is(err, marketing.ErrActivityNotApplicable) {
			return err
		}
	}
	return nil
}

// EnqueueManualFeeReview 将无法计费的订单加入人工核实队列。
// 按 charge_order_id 执行 no-op upsert，唯一键保证并发入队不新增重复记录。
// 使用调用方事务，不自行提交。
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
	ChargeOrderID uint64 // 同一订单主键，入队幂等键
	OrderNo       string // 充电单号
	Reason        string // 计量不足或价格无效的原因，不包含敏感信息
	EnergyWh      uint32 // 已确认电量，Wh
	CreatedMonth  string // 核实队列月份，YYYY-MM
	SourceJSON    string // 下单价格和实际计量 JSON
}

// FeeDeliveryRow 是一笔待送达的计费结果。送达失败会推迟重试而不是丢弃。
type FeeDeliveryRow struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id"` // 计费订单主键
	PayloadJSON   []byte `gorm:"column:payload_json"`    // 完整冻结 Result，不能按当前规则重新构造
}

// feeDeliveryBatch 一次取多少笔待送达的计费结果。
const feeDeliveryBatch = 50

// ListPendingFeeDeliveries 按计划时间、订单顺序读取到期投递，每批不超过 feeDeliveryBatch 笔。
func ListPendingFeeDeliveries(ctx context.Context, db *gorm.DB) ([]FeeDeliveryRow, error) {
	rows := []FeeDeliveryRow{}
	err := db.WithContext(ctx).Table("fee_delivery").
		Where("delivered=0 AND scheduled_at<=UTC_TIMESTAMP(3)").
		Order("scheduled_at,charge_order_id").Limit(feeDeliveryBatch).Find(&rows).Error
	return rows, err
}

// RescheduleFeeDelivery 延后未投递结果的重试时间并增加尝试次数，保留同一订单和载荷。
func RescheduleFeeDelivery(db *gorm.DB, orderID uint64, retryAt time.Time) error {
	return db.Table("fee_delivery").Where("charge_order_id=? AND delivered=0", orderID).
		Updates(map[string]any{"attempts": gorm.Expr("attempts+1"), "scheduled_at": retryAt}).Error
}

// EnqueueChargeBilling 在结束回执事务中登记异步计费任务；结算用例稍后计算费用并出账。
func EnqueueChargeBilling(tx *gorm.DB, chargeOrderID uint64) error {
	return tx.Table("charge_billing_job").Create(map[string]any{"charge_order_id": chargeOrderID}).Error
}

// BillingJobState 是计费任务的排队状态。取不到表示尚未排队。
type BillingJobState struct {
	ChargeOrderID uint64           `gorm:"column:charge_order_id"`      // 排队订单主键，零表示尚未排队
	Status        BillingJobStatus `json:"status" gorm:"column:status"` // pending、manual_review 或 done，未排队为空串
}

// FindBillingJob 读取订单计费任务；没有任务时返回零值和 nil。
func FindBillingJob(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (BillingJobState, error) {
	var row BillingJobState
	err := db.WithContext(ctx).Table("charge_billing_job").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}
