package finish

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"math"
	"time"

	rules "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrEndResultConflict = errors.New("charge end result conflicts with order state")

type EndResult struct {
	OrderNo        string         `json:"order_no"`
	ChargeOrderID  uint64         `json:"charge_order_id"`
	StartCommandID string         `json:"start_command_id"`
	StopCommandID  string         `json:"stop_command_id"`
	DeviceID       string         `json:"device_id"`
	PortNo         uint8          `json:"port_no"`
	PortID         uint64         `json:"port_id"`
	Meter          rules.EndMeter `json:"meter"`
}

type EndResultService struct{ DB *gorm.DB }

func (s EndResultService) Apply(ctx context.Context, result EndResult) (bool, error) {
	if s.DB == nil || result.OrderNo == "" || result.ChargeOrderID == 0 || result.StartCommandID == "" || result.StopCommandID == "" || result.DeviceID == "" || result.PortNo == 0 || result.PortID == 0 || result.PortID > math.MaxInt64 || result.Meter.ChargedSeconds > math.MaxInt32 || result.Meter.EndedAt.IsZero() {
		return false, ErrEndResultConflict
	}
	result.Meter.EndedAt = result.Meter.EndedAt.UTC().Truncate(time.Millisecond)
	meterJSON, err := json.Marshal(result.Meter)
	if err != nil {
		return false, err
	}
	replayed := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 与刷卡事务保持同一加锁顺序：先会话，再订单。
		if err := card.LockChargeForOrder(tx, result.ChargeOrderID); err != nil {
			return err
		}
		var order orderstore.ChargeOrderRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND order_no = ? AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).Take(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEndResultConflict
		}
		if err != nil {
			return err
		}
		if order.DeviceID != result.DeviceID || order.PortNo != result.PortNo {
			return ErrEndResultConflict
		}
		var existing orderstore.EndReceiptRecord
		err = tx.Where("charge_order_id = ?", result.ChargeOrderID).Take(&existing).Error
		if err == nil {
			var previous rules.EndMeter
			if json.Unmarshal(existing.MeterJSON, &previous) != nil || existing.StopCommandID != result.StopCommandID || previous.ChargedWh != result.Meter.ChargedWh || previous.ChargedSeconds != result.Meter.ChargedSeconds || previous.StopReason != result.Meter.StopReason || !rules.SameSegments(previous.Segments, result.Meter.Segments) || !previous.EndedAt.Equal(result.Meter.EndedAt) {
				return ErrEndResultConflict
			}
			replayed = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if order.Status != "charging" || !order.StartedAt.Valid || result.Meter.EndedAt.Before(order.StartedAt.Time) {
			return ErrEndResultConflict
		}
		var start orderstore.StartReceiptRecord
		if err := tx.Where("charge_order_id = ?", result.ChargeOrderID).Take(&start).Error; err != nil || !start.Success || start.CommandID != result.StartCommandID || !start.PortID.Valid || start.PortID.Int64 != int64(result.PortID) {
			return ErrEndResultConflict
		}
		if err := tx.Create(&orderstore.EndReceiptRecord{ChargeOrderID: result.ChargeOrderID, StopCommandID: result.StopCommandID, MeterJSON: meterJSON}).Error; err != nil {
			return err
		}
		// 经 card 用例释放该订单的卡端口占用。
		if err := card.ClearActivePort(tx, result.ChargeOrderID); err != nil {
			return err
		}
		kwh := decimal.NewFromInt(int64(result.Meter.ChargedWh)).Shift(-3).StringFixed(4)
		updated := tx.Model(&orderstore.ChargeOrderRecord{}).Where("id = ? AND order_no = ? AND status = 'charging' AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).
			Updates(map[string]any{"status": "completed", "ended_at": result.Meter.EndedAt, "charged_kwh": kwh, "charged_seconds": result.Meter.ChargedSeconds})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrEndResultConflict
		}
		port := tx.Where("port_id = ? AND device_id = ? AND port_no = ? AND charge_order_id = ?", result.PortID, result.DeviceID, result.PortNo, result.ChargeOrderID).Delete(&orderstore.ActivePortChargeRecord{})
		if port.Error != nil {
			return port.Error
		}
		if port.RowsAffected != 1 {
			return ErrEndResultConflict
		}
		if err := tx.Create(&orderstore.ChargeEventLogRecord{ChargeOrderID: result.ChargeOrderID, EventID: result.StopCommandID,
			Event: "charge_ended", Actor: "gateway", Detail: "device settlement frame accepted", OccurredAt: result.Meter.EndedAt}).Error; err != nil {
			return err
		}
		envelope, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := settlement.EnqueueChargeBilling(tx, result.ChargeOrderID); err != nil {
			return err
		}
		return tx.Create(&ops.EventOutboxRecord{EventID: result.StopCommandID, Stream: "charge_ended_stream", EnvelopeJSON: envelope}).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return replayed, err
}
