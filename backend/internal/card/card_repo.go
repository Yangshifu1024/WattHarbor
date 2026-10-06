package card

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	pay "github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"strconv"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrCardOperation = errors.New("刷卡不可用：卡状态、余额、端口、累计上限或设备确认状态不满足要求")
var ErrCardBalance = fmt.Errorf("%w：钱包可用余额不足", ErrCardOperation)

// Repo 在调用方连接或事务中访问刷卡、钱包与订单表；刷卡写入由用例统一提交，仓储不自行确认设备执行。
type Repo struct {
	DB     *gorm.DB          // 原卡事务或连接，提交由用例管理
	Cutoff SwipeCutoffReader // 所属停止用例的当前截止事实
}

// mapWalletMoveError 把钱包账本的错误映射回刷卡业务的哨兵错误。
// err 为原钱包错误，返回不足余额／不可操作哨兵或其他原错误；nil 保持 nil，无 IO。
func mapWalletMoveError(err error) error {
	if errors.Is(err, wallet.ErrInsufficient) {
		return ErrCardBalance
	}
	if errors.Is(err, wallet.ErrInactive) {
		return ErrCardOperation
	}
	return err
}

// applyInTx 在调用方 tx 中按 cardNo/eventID、port 原物理身份和 rule 首次报价执行原刷卡状态机。
// 返回原 CardOperation 或准入／资金／唯一键／存储错误；本方法不提交，所有失败必须由用例回滚。
// 卡→钱包→会话→订单依序加锁，原操作及待确认状态当前读；截止通过所属窄能力当前读取。
// 首购同时写支付、订单、方案、会话和扣款凭据；续购使用原套餐，并原子累计资金和时长。
func (s Repo) applyInTx(tx *gorm.DB, cardNo, eventID string, port money.PortRef, rule charging.Rule) (CardOperation, error) {
	var out CardOperation
	if n, err := strconv.ParseUint(cardNo, 10, 32); err != nil || n == 0 || strconv.FormatUint(n, 10) != cardNo {
		return out, ErrCardOperation
	}
	if cardNo == "" || eventID == "" || len(eventID) > 128 || !port.Found || !port.Online || port.Kind != "port" {
		return out, ErrCardOperation
	}
	err := func() error {
		var card OnlineCard
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("card_no=?", cardNo).Take(&card).Error; err != nil {
			return err
		}
		var previous CardOperation
		// 卡锁只串行同卡操作，不能刷新已建立的 RR 快照；原操作必须使用当前读恢复。
		found := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("device_id=? AND event_id=?", port.DeviceID, eventID).Find(&previous)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected > 0 {
			if previous.CardID != card.ID {
				return ErrCardOperation
			}
			var session CardCharge
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("charge_order_id=? AND port_code=?", previous.ChargeOrderID, port.PortID).Take(&session).Error; err != nil {
				return ErrCardOperation
			}
			out = previous
			return nil
		}
		if card.Status != "active" {
			return ErrCardOperation
		}
		if err := identity.RequireActiveUserInTx(tx, card.UserID); err != nil {
			return err
		}
		w, err := wallet.Lock(tx, card.UserID)
		if err != nil {
			return err
		}
		var session CardCharge
		active := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("active_port=?", port.PortID).Find(&session)
		if active.Error != nil {
			return active.Error
		}
		var p charging.Package
		out = CardOperation{OperationID: uuid.NewString(), DeviceID: port.DeviceID, EventID: eventID, CardID: card.ID, Status: "confirming"}
		if active.RowsAffected > 0 {
			if json.Unmarshal(session.PackageJSON, &p) != nil {
				return ErrCardOperation
			}
			order, err := orderstore.LockCardChargingInTx(tx, session.ChargeOrderID)
			if err != nil {
				return ErrCardOperation
			}
			if !order.StartedAt.Valid || !time.Now().UTC().Before(order.StartedAt.Time.Add(time.Duration(session.PurchasedMinutes)*time.Minute)) || order.UserID != card.UserID {
				return ErrCardOperation
			}
			if s.Cutoff == nil {
				return errors.New("card cutoff reader is not configured")
			}
			cutoff, err := s.Cutoff.FrozenInTx(tx, order.ID)
			if err != nil {
				return err
			}
			if cutoff {
				return ErrCardOperation
			}
			var pending []CardOperation
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("charge_order_id=? AND status='confirming'", session.ChargeOrderID).Find(&pending).Error; err != nil {
				return err
			}
			if !CanExtend(card.ID, session.CardID, p, session.PurchasedMinutes, session.MaxMinutes, w.BalanceCents-w.FrozenCents, len(pending) > 0) {
				if CanExtend(card.ID, session.CardID, p, session.PurchasedMinutes, session.MaxMinutes, p.PriceCents, len(pending) > 0) {
					return ErrCardBalance
				}
				return ErrCardOperation
			}
			out.Kind = "extend"
			out.Status = "confirmed"
			out.ChargeOrderID = session.ChargeOrderID
			if err := pay.ExtendCardPaymentInTx(tx, uint64(order.PaymentOrderID.Int64), p.PriceCents); err != nil {
				if errors.Is(err, pay.ErrCardFundingConflict) {
					return ErrCardOperation
				}
				return err
			}
			if err := orderstore.SyncOrderPaymentStatus(tx, uint64(order.PaymentOrderID.Int64)); err != nil {
				return err
			}
			if err := tx.Model(&CardCharge{}).Where("charge_order_id=?", session.ChargeOrderID).Updates(map[string]any{"paid_cents": gorm.Expr("paid_cents+?", p.PriceCents), "purchased_minutes": gorm.Expr("purchased_minutes+?", p.Minutes)}).Error; err != nil {
				return err
			}
		} else {
			if err := pay.CheckoutPortAvailable(tx, port.PortID); err != nil {
				return err
			}
			if !port.Available || rule.Spec.Scheme == nil || rule.Spec.Scheme.Card.PackageID == 0 {
				return ErrCardOperation
			}
			var ok bool
			p, ok = rule.Spec.Scheme.Package(rule.Spec.Scheme.Card.PackageID)
			if !ok || !CanExtend(card.ID, card.ID, p, 0, rule.Spec.Scheme.Normalized().Card.MaxMinutes, w.BalanceCents-w.FrozenCents, false) {
				if ok && CanExtend(card.ID, card.ID, p, 0, rule.Spec.Scheme.Normalized().Card.MaxMinutes, p.PriceCents, false) {
					return ErrCardBalance
				}
				return ErrCardOperation
			}
			out.Kind = "start"
			spec := rule.Spec.Scheme.SpecFor(p)
			rule.Spec = spec
			var offer charging.Offer
			for _, o := range spec.Scheme.Offers(rule) {
				if o.PackageID == p.ID {
					offer = o
					offer.ServerDuration = true
				}
			}
			estimate := charging.Estimate{Mode: charging.ModeDeviceDuration, EstimatedKWh: "0.000", EstimatedMinutes: p.Minutes, PrepaidCents: p.PriceCents, TotalCents: p.PriceCents, ChargeMode: 4, ChargeQuantity: spec.Scheme.Normalized().Card.MaxMinutes}
			snapshot, _ := json.Marshal(map[string]any{"rule": rule, "offer": offer, "estimate": estimate})
			intentID := uuid.NewString()
			payNo, err := pay.NewOrderNumber(tx)
			if err != nil {
				return err
			}
			payment := pay.PaymentOrderRecord{OrderNo: payNo, BizType: "charge", UserID: card.UserID, PayMethod: "balance", TotalCents: p.PriceCents, PaidCents: p.PriceCents, Status: "paid", PaidAt: sql.NullTime{Time: time.Now().UTC(), Valid: true}, CreatedMonth: dbutil.MonthStart()}
			if err := pay.CreateCardPaymentInTx(tx, &payment); err != nil {
				return err
			}
			chargeNo, err := orderstore.NewChargeOrderNumber(tx, port.DeviceID, port.PortNo)
			if err != nil {
				return err
			}
			order := orderstore.ChargeOrderRecord{OrderNo: chargeNo, UserID: card.UserID, DeviceID: port.DeviceID, PortNo: port.PortNo, PortCode: sql.NullString{String: port.PortID, Valid: true}, PaymentOrderID: sql.NullInt64{Int64: int64(payment.ID), Valid: true}, Status: "paid", PaymentStatus: "paid", ChargeMode: 4, ChargeQuantity: spec.Scheme.Normalized().Card.MaxMinutes, CreatedMonth: dbutil.MonthStart()}
			if err := orderstore.CreatePaidInTx(tx, &order); err != nil {
				return err
			}
			out.ChargeOrderID = order.ID
			if err := pay.SetCardOrderInTx(tx, payment.ID, order.ID); err != nil {
				return err
			}
			intent := pay.PaymentIntentRecord{IntentID: intentID, ClientRequestID: out.OperationID, MerchantOrderNo: payNo, PaymentOrderID: payment.ID, UserID: card.UserID, DeviceID: port.DeviceID, PortNo: port.PortNo, PortCode: port.PortID, StationID: port.StationID, PricingRuleID: rule.ID, PricingRuleVersion: rule.Version, PricingSnapshot: snapshot, EstimatedKWh: "0.000", EstimatedMinutes: p.Minutes, TotalCents: p.PriceCents, ChargeMode: 4, ChargeQuantity: spec.Scheme.Normalized().Card.MaxMinutes, Status: "paid", ExpiresAt: time.Now().UTC().Add(5 * time.Minute), ChargeOrderID: sql.NullInt64{Int64: int64(order.ID), Valid: true}}
			if err := pay.CreateCardIntentInTx(tx, &intent); err != nil {
				return err
			}
			if err := orderstore.FreezePricingInTx(tx, orderstore.ChargePricingSnapshotRecord{ChargeOrderID: order.ID, PaymentIntentID: intentID, UserID: card.UserID, PortCode: port.PortID, PricingSnapshot: snapshot}); err != nil {
				return err
			}
			praw, _ := json.Marshal(p)
			portCode := port.PortID
			if err := tx.Create(&CardCharge{ChargeOrderID: order.ID, CardID: card.ID, PortCode: portCode, ActivePort: &portCode, PaidCents: p.PriceCents, PurchasedMinutes: p.Minutes, MaxMinutes: spec.Scheme.Normalized().Card.MaxMinutes, PackageJSON: praw, CardNo: card.CardNo, WalletAfterCents: w.BalanceCents - w.FrozenCents - p.PriceCents}).Error; err != nil {
				// 并发刷卡抢同一端口时 active_port 唯一键拒绝后写事务，
				// 按业务冲突处理，设备重试即可。
				if dbutil.IsMySQLDuplicate(err) {
					return ErrCardOperation
				}
				return err
			}
		}
		out.PriceCents, out.Minutes = p.PriceCents, p.Minutes
		if err := mapWalletMoveError(wallet.Move(tx, card.UserID, &w, -p.PriceCents, "CP"+out.OperationID, "在线刷卡"+out.Kind)); err != nil {
			return err
		}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		if out.Kind == "extend" {
			if err := tx.Model(&CardOperation{}).Where("operation_id=?", out.OperationID).Update("confirmed_at", gorm.Expr("UTC_TIMESTAMP(3)")).Error; err != nil {
				return err
			}
		}
		return orderstore.AppendEventInTx(tx, orderstore.ChargeEventLogRecord{ChargeOrderID: out.ChargeOrderID, EventID: out.OperationID, Event: "card_" + out.Kind + "_" + out.Status, Actor: "card", Detail: fmt.Sprintf("card=%d cents=%d minutes=%d", card.ID, p.PriceCents, p.Minutes), OccurredAt: time.Now().UTC()})
	}()
	return out, err
}

// ActiveSessionCount 在 ctx 下统计原 portCode 的活跃卡会话；返回计数或存储错误。
// 此读仅选择当前报价是否需要，事务内会重新锁会话，不作为扣款授权。
func (s Repo) ActiveSessionCount(ctx context.Context, portCode string) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&CardCharge{}).Where("active_port=?", portCode).Count(&count).Error
	return count, err
}
