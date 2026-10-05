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

	"reflect"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BillingOrders 持有原计费任务、证据读取及资金结算的数据库入口。
type BillingOrders struct {
	DB *gorm.DB // central 任务及资金连接，由进程管理
}

// Due 在 ctx 下读取最多 50 笔到期 pending 订单，按执行时间和订单 ID 稳定排序。
// 返回订单集合或存储错误，不在读取时修改任务，真正入账由 Apply 的原回执仲裁。
func (s BillingOrders) Due(ctx context.Context) ([]uint64, error) {
	ids := []uint64{}
	err := s.DB.WithContext(ctx).Table("charge_billing_job").Where("status='pending' AND next_attempt_at<=UTC_TIMESTAMP(3)").Order("next_attempt_at,charge_order_id").Limit(50).Pluck("charge_order_id", &ids).Error
	return ids, err
}

// Defer 在 ctx 下将未完成订单 id 任务转为 status，记录已脱敏 reason 并延后一分钟、增加尝试次数。
// 返回更新错误，不改变 done 任务，不删除原计量或费用凭据，也不扣款。
func (s BillingOrders) Defer(ctx context.Context, id uint64, status, reason string) error {
	return s.DB.WithContext(ctx).Table("charge_billing_job").Where("charge_order_id=? AND status<>'done'", id).Updates(map[string]any{"status": status, "last_error": reason, "attempts": gorm.Expr("attempts+1"), "next_attempt_at": time.Now().UTC().Add(time.Minute)}).Error
}

// Read 在 ctx 下读取订单 id 的原价格、结束计量、截止点及已核实证据，返回 Source 或读取／证据冲突。
// 不用当前方案替代下单快照，不重算缺失计量，也不执行入账。
func (s BillingOrders) Read(ctx context.Context, id uint64) (Source, error) {
	return readBillingSource(s.DB.WithContext(ctx), id)
}

// readBillingSource 用 db 读取订单 id 的原证据，应用已批准且原始证据仍一致的分段核实。
// 返回完整 Source 或读取／快照冲突；人工最终金额优先，不修改任何持久记录。
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

// readOriginalBillingSource 用 db 读取 id 已结束订单的下单、结束、卡累计及截止凭据。
// 返回 Source 或缺行／内容冲突；截止无法通过真实计量切片时保留 ReviewRequired，不伪造金额。
// 卡未确认操作拒绝计算，冻结预算及人工最终金额均使用原持久身份，不改当前订单或方案。
func readOriginalBillingSource(db *gorm.DB, id uint64) (Source, error) {
	var order orderstore.ChargeOrderRecord
	if err := db.Where("id=? AND ended_at IS NOT NULL AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
		return Source{}, err
	}
	if !order.StartedAt.Valid || (order.Status != "completed" && order.Status != "refunding" && order.Status != "refunded") {
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
		Offer *charging.Offer `json:"offer"` // 下单时原套餐，当前计费必须存在
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
		CutoffAt      time.Time // 首次计费截止 UTC 时刻，零表示未冻结
		Reason        string    // 原截止原因，预算冻结使用 budget_exhausted
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
		RequestID     string // 原人工最终金额裁决 UUID
		ElectricCents int64  // 原裁决电费，分
		ServiceCents  int64  // 原裁决服务费，分
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

// Apply 在 ctx 下按 result 原证据幂等应用费用，并重读原身份、价格和计量核对未变化。
// 返回证据、金额或存储错误；资金、费用回执、剩余退款、订单、任务及 Outbox 同事务提交。
// 余额支付先锁钱包，再锁支付及订单；在途退款用当前读，重复回执不得再次退余款或发券。
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
		var existing struct {
			ResultJSON []byte // 已入账的原完整费用回执
		}
		// 支付方式普通读早于资金锁；当前读原回执，确保锁等待期间完成的入账仍可幂等返回。
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
			return tx.Table("charge_billing_job").Where("charge_order_id=?", order.ID).Updates(map[string]any{"status": "done", "last_error": nil}).Error
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
		// 费用计算完成并写入回执后求值订单活动，确保金额门槛准确。
		// 重放结算在回执检查时提前返回，不重复发券。
		if err := applyOrderCampaigns(tx, order, result.TotalCents); err != nil {
			return err
		}
		// 原支付方式的普通读取可能已在支付锁之前建立 RR 快照。
		// 在途聚合必须使用当前读，否则会遗漏并发已提交退款，高估可退余款。
		var reserved int64
		if err := tx.Model(&RefundRecord{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("COALESCE(SUM(refund_cents),0)").
			Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", payment.ID).
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
				refund := RefundRecord{RefundNo: fmt.Sprintf("FEE-R%020d", order.ID), PaymentOrderID: payment.ID, UserID: order.UserID, BizType: "charge", BizID: order.ID, RefundCents: surplus, Reason: sql.NullString{String: "实际计费后的预付差额", Valid: true}, Status: "pending", ExecutionPolicy: "automatic", CreatedMonth: dbutil.MonthStart()}
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
		return tx.Table("charge_billing_job").Where("charge_order_id=?", order.ID).Updates(map[string]any{"status": "done", "last_error": nil}).Error
	})
}

// settlementNotice 以 eventID、order 原身份、display 下单展示快照、fee 最终分值及 refund 待退分值构造事件。
// 返回符合原展示开关的 JSON 对象，无 IO 或错误；仅在资金已持久结算后由调用方追加 Outbox。
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
// 规则不适用时跳过；存储错误返回调用方并整体回滚资金与权益。订单事件键保证重复求值不重复发券。
// tx 为原资金事务，order 提供原用户及单号，totalCents 为确定结算金额（分）；返回存储错误，不自行提交。
func applyOrderCampaigns(tx *gorm.DB, order orderstore.ChargeOrderRecord, totalCents int64) error {
	now := time.Now().UTC()
	for _, trigger := range []string{"threshold_redeem", "holiday"} {
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

// EnqueueManualFeeReview 把一笔算不出费用的订单排进人工核实队列。
//
// 幂等键是 charge_order_id：同一笔订单重复入队只更新不新增。用 no-op upsert
// 而不是"先查再插"，两个并发的入队请求会同时查到"没有"然后都去插，靠唯一键
// 把其中一个挡下来。
// tx 为调用方上下文连接或事务，in 提供原身份、原因和证据；返回插入错误，不自行提交。
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
	ChargeOrderID uint64 // 原订单主键，入队幂等键
	OrderNo       string // 原充电单号
	Reason        string // 缺证据或价格无效的已脱敏原因
	EnergyWh      uint32 // 原已确认电量，Wh
	CreatedMonth  string // 原核实队列月份，YYYY-MM
	SourceJSON    string // 原完整计费证据 JSON
}

// FeeDeliveryRow 是一笔待送达的计费结果。送达失败会推迟重试而不是丢弃。
type FeeDeliveryRow struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id"` // 原计费订单主键
	PayloadJSON   []byte `gorm:"column:payload_json"`    // 完整冻结 Result，不能按当前规则重新构造
}

// feeDeliveryBatch 一次取多少笔待送达的计费结果。
const feeDeliveryBatch = 50

// ListPendingFeeDeliveries 返回到期待送达的计费结果，按计划时间与订单排序。
// ctx 取消 db 查询；最多 feeDeliveryBatch 笔，无待投递返回空数组，存储失败返回 error，不修改领取状态。
func ListPendingFeeDeliveries(ctx context.Context, db *gorm.DB) ([]FeeDeliveryRow, error) {
	rows := []FeeDeliveryRow{}
	err := db.WithContext(ctx).Table("fee_delivery").
		Where("delivered=0 AND scheduled_at<=UTC_TIMESTAMP(3)").
		Order("scheduled_at,charge_order_id").Limit(feeDeliveryBatch).Find(&rows).Error
	return rows, err
}

// RescheduleFeeDelivery 用 db 将 orderID 未投递结果的下次时间设为 retryAt UTC 并增加尝试次数。
// 返回更新错误，不删除原载荷；调用方保留原订单及结果重试，不改变已投递记录。
func RescheduleFeeDelivery(db *gorm.DB, orderID uint64, retryAt time.Time) error {
	return db.Table("fee_delivery").Where("charge_order_id=? AND delivered=0", orderID).
		Updates(map[string]any{"attempts": gorm.Expr("attempts+1"), "scheduled_at": retryAt}).Error
}

// EnqueueChargeBilling 把结束计量排队。计费是异步的：结束回执只负责确认
// 事实并排队，费用由 central 的结算用例算完再落账单。
// tx 是原结束回执事务，chargeOrderID 为原订单；返回唯一键或插入错误，调用方整体提交／回滚。
func EnqueueChargeBilling(tx *gorm.DB, chargeOrderID uint64) error {
	return tx.Table("charge_billing_job").Create(map[string]any{"charge_order_id": chargeOrderID}).Error
}

// BillingJobState 是计费任务的排队状态。取不到表示尚未排队。
type BillingJobState struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`      // 原排队订单主键，零表示尚未排队
	Status        string `json:"status" gorm:"column:status"` // pending、manual_review 或 done，未排队为空
}

// FindBillingJob 返回订单的计费排队状态。
// ctx 取消 db 查询，chargeOrderID 为原订单；无任务返回零值、nil，存储失败返回 error，不创建任务。
func FindBillingJob(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (BillingJobState, error) {
	var row BillingJobState
	err := db.WithContext(ctx).Table("charge_billing_job").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}
