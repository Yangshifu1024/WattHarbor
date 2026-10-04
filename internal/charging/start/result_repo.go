package start

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	pays "github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	walletpkg "github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"math"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrStartResultConflict = errors.New("charge start result conflicts with order state")

type StartResult struct {
	CommandID     string    `json:"command_id"`      // 原启动命令幂等身份
	ChargeOrderID uint64    `json:"charge_order_id"` // 原已付订单主键
	OrderNo       string    `json:"order_no"`        // 与主键共同核对的业务单号
	DeviceID      string    `json:"device_id"`       // 实际响应的设备身份
	PortNo        uint8     `json:"port_no"`         // 设备端口序号，从 1 开始
	PortID        uint64    `json:"port_id"`         // gateway 持久端口身份，非零且不得超过 int64
	Success       bool      `json:"success"`         // 必须与 ResultCode==0 一致
	ResultCode    uint8     `json:"result_code"`     // 原设备结果码，0 成功、1–3 拒绝
	OccurredAt    time.Time `json:"occurred_at"`     // 回执发生时刻，入库规范为 UTC 毫秒
}

type StartResultService struct {
	DB *gorm.DB /* central 回执事务连接，生命周期由进程管理 */
}

// Apply 在 ctx 管理的 READ COMMITTED 事务中应用 result 的启动回执。
// 返回 bool 为同内容重放标志，首次成功为 false；冲突或存储错误时不应确认投递。
// 余额路径先锁钱包，再锁卡会话和订单；回执、状态、退款、日志及 Outbox 原子提交。
// 拒绝回执仅登记渠道退款或完成余额退款，不能把待渠道退款当作到账。
func (s StartResultService) Apply(ctx context.Context, result StartResult) (bool, error) {
	if s.DB == nil || result.CommandID == "" || result.ChargeOrderID == 0 || result.OrderNo == "" || result.DeviceID == "" || result.PortNo == 0 || result.PortID == 0 || result.PortID > math.MaxInt64 || result.OccurredAt.IsZero() || result.Success != (result.ResultCode == 0) || result.ResultCode > 3 {
		return false, ErrStartResultConflict
	}
	result.OccurredAt = result.OccurredAt.UTC().Truncate(time.Millisecond)
	replayed := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var walletRow walletpkg.Row
		payMethod, paymentUserID, err := pays.FindOrderPaymentForCharge(tx, result.ChargeOrderID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 订单已回滚或压根不存在。回执落不下去是冲突而不是服务故障：
			// 报 500 会让网关无限重试一笔永远不会成立的回执。
			return ErrStartResultConflict
		}
		if err != nil {
			return err
		}
		if payMethod == "balance" {
			walletRow, err = walletpkg.Lock(tx, paymentUserID)
			if err != nil {
				return err
			}
		}
		// 与刷卡事务保持同一加锁顺序：先会话，再订单。
		if err := card.LockChargeForOrder(tx, result.ChargeOrderID); err != nil {
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
			// 刷卡起充才有 card_operation 行；扫码/余额起充没有，影响 0 行是常态而非冲突。
			// 这里只把数据库错误上抛——把 RowsAffected 当冲突判据会让所有非刷卡订单
			// 的启动回执被拒，worker 随即下发补偿停止，把已经在充电的枪拔掉。
			if _, err := card.ConfirmStartOperation(tx, result.ChargeOrderID, result.OccurredAt); err != nil {
				return err
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
				paid, refunded, err := pays.ReadBalancePaymentAmounts(tx, uint64(order.PaymentOrderID.Int64))
				if err != nil {
					return err
				}
				if err := settlement.RefundBalanceInTx(tx, order, &walletRow, paid-refunded, "CSF"+result.CommandID, failureReason); err != nil {
					if errors.Is(err, settlement.ErrBalanceRefundConflict) {
						return ErrStartResultConflict
					}
					return err
				}
				if err := orderstore.SetStatusRefunded(tx, order.ID); err != nil {
					return err
				}
				if err := card.FailStartOperation(tx, order.ID, failureReason, result.OccurredAt); err != nil {
					return err
				}
				if err := card.ClearActivePort(tx, order.ID); err != nil {
					return err
				}
			} else {
				payment, lookup := pays.LockWechatPaymentForRefund(tx, result.ChargeOrderID)
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
				// 退款记录与启动失败回执、Outbox 在同一事务内提交。
				if err := settlement.InsertRefundRecord(tx, settlement.RefundRecordInput{
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
				if err := tx.Create(&ops.EventOutboxRecord{EventID: refundEventID, Stream: "refund_required_stream", EnvelopeJSON: refundEnvelope}).Error; err != nil {
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
		if err := tx.Create(&ops.EventOutboxRecord{EventID: result.CommandID, Stream: stream, EnvelopeJSON: envelope}).Error; err != nil {
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return replayed, err
}
