package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"math"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrStartResultConflict = errors.New("charge start result conflicts with order state")

type StartResult struct {
	CommandID     string    `json:"command_id" binding:"required,uuid"`
	ChargeOrderID uint64    `json:"charge_order_id" binding:"required"`
	OrderNo       string    `json:"order_no" binding:"required,max=64"`
	DeviceID      string    `json:"device_id" binding:"required,max=64"`
	PortNo        uint8     `json:"port_no" binding:"required"`
	PortID        uint64    `json:"port_id" binding:"required"`
	Success       bool      `json:"success"`
	ResultCode    uint8     `json:"result_code"`
	OccurredAt    time.Time `json:"occurred_at" binding:"required"`
}

type StartResultStore struct{ DB *gorm.DB }

func (s StartResultStore) Apply(ctx context.Context, result StartResult) (bool, error) {
	if s.DB == nil || result.CommandID == "" || result.ChargeOrderID == 0 || result.OrderNo == "" || result.DeviceID == "" || result.PortNo == 0 || result.PortID == 0 || result.PortID > math.MaxInt64 || result.OccurredAt.IsZero() || result.Success != (result.ResultCode == 0) || result.ResultCode > 3 {
		return false, ErrStartResultConflict
	}
	result.OccurredAt = result.OccurredAt.UTC().Truncate(time.Millisecond)
	replayed := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var walletRow orderstore.Row
		payMethod, paymentUserID, err := orderstore.FindOrderPaymentForCharge(tx, result.ChargeOrderID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 订单已回滚或压根不存在。回执落不下去是冲突而不是服务故障：
			// 报 500 会让网关无限重试一笔永远不会成立的回执。
			return ErrStartResultConflict
		}
		if err != nil {
			return err
		}
		if payMethod == "balance" {
			walletRow, err = orderstore.Lock(tx, paymentUserID)
			if err != nil {
				return err
			}
		}
		// Match the card transaction lock order: session before order.
		// 跨家族锁：card_charge 归属 card 家族，仅取行锁参与加锁顺序，不读字段。
		if err := orderstore.LockCardChargeForOrder(tx, result.ChargeOrderID); err != nil {
			return err
		}
		var order orderstore.ChargeOrderRecord
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND order_no = ? AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).Take(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrStartResultConflict
		}
		if err != nil {
			return err
		}
		if order.DeviceID != result.DeviceID || order.PortNo != result.PortNo {
			return ErrStartResultConflict
		}
		var existing orderstore.StartReceiptRecord
		err = tx.Where("command_id = ?", result.CommandID).Take(&existing).Error
		if err == nil {
			if existing.ChargeOrderID != result.ChargeOrderID || existing.OrderNo.String != result.OrderNo || existing.DeviceID.String != result.DeviceID || existing.PortNo.Int16 != int16(result.PortNo) || existing.PortID.Int64 != int64(result.PortID) || existing.Success != result.Success || existing.ResultCode.Int16 != int16(result.ResultCode) || !existing.OccurredAt.Time.Equal(result.OccurredAt.UTC()) {
				return ErrStartResultConflict
			}
			replayed = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if order.Status != "paid" {
			return ErrStartResultConflict
		}
		receipt := orderstore.StartReceiptRecord{CommandID: result.CommandID, ChargeOrderID: result.ChargeOrderID,
			OrderNo: sql.NullString{String: result.OrderNo, Valid: true}, DeviceID: sql.NullString{String: result.DeviceID, Valid: true},
			PortNo: sql.NullInt16{Int16: int16(result.PortNo), Valid: true}, PortID: sql.NullInt64{Int64: int64(result.PortID), Valid: true},
			Success: result.Success, ResultCode: sql.NullInt16{Int16: int16(result.ResultCode), Valid: true}, OccurredAt: sql.NullTime{Time: result.OccurredAt.UTC(), Valid: true}}
		if err := tx.Create(&receipt).Error; err != nil {
			if dbutil.IsMySQLDuplicate(err) {
				return ErrStartResultConflict
			}
			return err
		}
		stream, eventName := "charge_started_stream", "start_acked"
		if result.Success {
			confirmed, err := orderstore.ConfirmCardStartOperation(tx, result.ChargeOrderID, result.OccurredAt)
			if err != nil {
				return err
			}
			if confirmed != 1 {
				return ErrStartResultConflict
			}
			if err := tx.Create(&orderstore.ActivePortChargeRecord{PortID: result.PortID, DeviceID: result.DeviceID, PortNo: result.PortNo,
				ChargeOrderID: result.ChargeOrderID, UserID: order.UserID, StartedAt: result.OccurredAt.UTC()}).Error; err != nil {
				if dbutil.IsMySQLDuplicate(err) {
					return ErrStartResultConflict
				}
				return err
			}
			updated := tx.Model(&orderstore.ChargeOrderRecord{}).Where("id = ? AND order_no = ? AND status = 'paid' AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).
				Updates(map[string]any{"status": "charging", "started_at": result.OccurredAt.UTC()})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrStartResultConflict
			}
		} else {
			stream, eventName = "charge_start_rejected_stream", "start_rejected"
			failureReason := fmt.Sprintf("device START rejected: %d", result.ResultCode)
			updated := tx.Model(&orderstore.ChargeOrderRecord{}).Where("id = ? AND order_no = ? AND status = 'paid' AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).
				Updates(map[string]any{"failure_reason": failureReason, "status": "refunding"})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrStartResultConflict
			}
			if payMethod == "balance" {
				paid, refunded, err := orderstore.ReadBalancePaymentAmounts(tx, uint64(order.PaymentOrderID.Int64))
				if err != nil {
					return err
				}
				if err := refundBalanceStartFailure(tx, order, &walletRow, paid-refunded, "CSF"+result.CommandID, failureReason); err != nil {
					return err
				}
				if err := orderstore.SetOrderStatusRefunded(tx, order.ID); err != nil {
					return err
				}
				if err := orderstore.FailCardStartOperation(tx, order.ID, failureReason, result.OccurredAt); err != nil {
					return err
				}
				if err := orderstore.ClearCardActivePort(tx, order.ID); err != nil {
					return err
				}
			} else {
				payment, lookup := orderstore.LockWechatPaymentForRefund(tx, result.ChargeOrderID)
				if errors.Is(lookup, gorm.ErrRecordNotFound) {
					return ErrStartResultConflict
				}
				if lookup != nil {
					return lookup
				}
				if payment.UserID != order.UserID || !payment.WechatTransactionID.Valid || payment.WechatTransactionID.String == "" ||
					payment.TotalCents <= 0 || payment.PaidCents <= 0 || payment.RefundedCents < 0 || payment.PaidCents > payment.TotalCents-payment.RefundedCents {
					return ErrStartResultConflict
				}
				digest := sha256.Sum256([]byte("start-failure\x00" + result.CommandID))
				refundNo := "RF" + hex.EncodeToString(digest[:16])
				refundEventID := "R" + hex.EncodeToString(digest[:16])
				refundCents := payment.PaidCents - payment.RefundedCents
				if refundCents <= 0 {
					return ErrStartResultConflict
				}
				month := dbutil.MonthStart()
				// 跨家族写：refund_record 归属 refund 家族，经裸表名插入，避免 order 反向依赖 refund。
				if err := orderstore.InsertRefundRecord(tx, orderstore.RefundRecordInput{
					ExecutionPolicy: "automatic", RefundNo: refundNo, PaymentOrderID: payment.ID,
					UserID: payment.UserID, BizType: "charge", BizID: result.ChargeOrderID,
					RefundCents: refundCents, Reason: failureReason, Status: "pending",
				}, month); err != nil {
					return err
				}
				refundEnvelope, err := json.Marshal(map[string]any{
					"event_id": refundEventID, "refund_no": refundNo, "payment_order_id": payment.ID,
					"charge_order_id": result.ChargeOrderID, "order_no": result.OrderNo, "amount_cents": refundCents,
				})
				if err != nil {
					return err
				}
				if err := tx.Create(&orderstore.EventOutboxRecord{EventID: refundEventID, Stream: "refund_required_stream", EnvelopeJSON: refundEnvelope}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Create(&orderstore.ChargeEventLogRecord{ChargeOrderID: result.ChargeOrderID, EventID: result.CommandID,
			Event: eventName, Actor: "gateway", Detail: fmt.Sprintf("device=%s port=%d result=%d", result.DeviceID, result.PortNo, result.ResultCode),
			OccurredAt: result.OccurredAt.UTC()}).Error; err != nil {
			return err
		}
		envelope, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := tx.Create(&orderstore.EventOutboxRecord{EventID: result.CommandID, Stream: stream, EnvelopeJSON: envelope}).Error; err != nil {
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return replayed, err
}

type startRefundPaymentRow struct {
	ID                  uint64
	UserID              uint64
	TotalCents          int64
	PaidCents           int64
	RefundedCents       int64
	WechatTransactionID sql.NullString
	Status              string
}

// refundBalanceStartFailure 复现卡族 RefundInTx 的余额退款事务体（钱包退回 +
// payment_order 改写 + 支付状态同步 + refund_record 记录）。card→order 与 refund→order
// 依赖已单向占用，order 不得反向 import 三者，跨家族行一律经裸表名访问；
// 支付行校验冲突返回 ErrStartResultConflict（与原 card.ErrCardOperation 同为服务端错误）。
func refundBalanceStartFailure(tx *gorm.DB, order orderstore.ChargeOrderRecord, w *orderstore.Row, cents int64, refundNo, reason string) error {
	if cents <= 0 {
		return nil
	}
	if err := orderstore.Move(tx, order.UserID, w, cents, refundNo, reason); err != nil {
		return err
	}
	paid, refunded, err := orderstore.ReadBalancePaymentAmounts(tx, uint64(order.PaymentOrderID.Int64))
	if err != nil {
		return err
	}
	if paid-refunded < cents {
		return ErrStartResultConflict
	}
	status := "partial_refunded"
	if refunded+cents == paid {
		status = "refunded"
	}
	paymentID := uint64(order.PaymentOrderID.Int64)
	if err := orderstore.AddPaymentRefunded(tx, paymentID, cents, status); err != nil {
		return err
	}
	if err := orderstore.SyncOrderPaymentStatus(tx, paymentID); err != nil {
		return err
	}
	return orderstore.InsertRefundRecord(tx, orderstore.RefundRecordInput{
		RefundNo: refundNo, PaymentOrderID: paymentID, UserID: order.UserID,
		BizType: "charge", BizID: order.ID, RefundCents: cents, Status: "success",
		ExecutionPolicy: "automatic", Reason: reason,
	}, dbutil.MonthStart())
}

type StartResultAPI struct {
	Store        StartResultStore
	ServiceToken string
}

func (a StartResultAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/charge-orders/:order_no/start-result", a.handle)
}

func (a StartResultAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request StartResult
	if err := c.ShouldBindJSON(&request); err != nil || c.Param("order_no") != request.OrderNo {
		httpapi.BadRequest(c, "invalid start result")
		return
	}
	replayed, err := a.Store.Apply(c.Request.Context(), request)
	if errors.Is(err, ErrStartResultConflict) {
		httpapi.Write(c, http.StatusConflict, 2000, "start result conflicts with order", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "start result persistence failed", nil)
		return
	}
	httpapi.OK(c, gin.H{"accepted": true, "replayed": replayed})
}
