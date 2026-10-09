package finish

import (
	"chargepilot/internal/card"
	"chargepilot/internal/ops"
	orderstore "chargepilot/internal/order"
	"chargepilot/internal/settlement"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	rules "chargepilot/internal/domain/charging"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var ErrEndResultConflict = errors.New("charge end result conflicts with order state")

// EndResult 携带订单、启动及停止命令编号和结束计量，供中央用例核对结束回执；它本身不证明回执已落库或计费已完成。
type EndResult struct {
	OrderNo        string         `json:"order_no"`         // 业务订单号
	ChargeOrderID  uint64         `json:"charge_order_id"`  // 订单主键，必须仍在充电
	StartCommandID string         `json:"start_command_id"` // 成功启动凭据的命令编号
	StopCommandID  string         `json:"stop_command_id"`  // 本次结束回执命令编号，重放不可换号
	DeviceID       string         `json:"device_id"`        // 设备编号
	PortNo         uint8          `json:"port_no"`          // 设备端口序号，从 1 开始
	PortID         uint64         `json:"port_id"`          // 占用端口的 gateway 主键
	Meter          rules.EndMeter `json:"meter"`            // 结束计量证据；时长秒、电量 Wh、结束时刻 UTC
}

// EndResultService 在中央事务中登记结束凭据、更新订单并排入计费与 Outbox；网关占用释放和后续计费由各自流程完成。
type EndResultService struct {
	DB *gorm.DB /* central 结束回执事务连接，生命周期由进程管理 */
}

// Apply 在 ctx 管理的 READ COMMITTED 事务中应用 result 的结束回执。
// 返回 bool 表示同内容重放；冲突、计量不符或数据库错误时整体回滚，不确认网关投递。
// 按卡会话→订单加锁；释放端口必须匹配订单，迟到回执不能释放新的占用。
// 结束凭据、订单计量、占用释放、计费任务及 Outbox 同事务，计费留待后续执行。
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
		var view orderstore.LifecycleView
		view, err := orderstore.LockForLifecycleInTx(tx, result.ChargeOrderID, result.OrderNo)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEndResultConflict
		}
		if err != nil {
			return err
		}
		if view.DeviceID != result.DeviceID || view.PortNo != result.PortNo {
			return ErrEndResultConflict
		}
		var existing endReceiptRecord
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
		if view.Status != "charging" || !view.StartedAt.Valid || result.Meter.EndedAt.Before(view.StartedAt.Time) {
			return ErrEndResultConflict
		}
		var start startReceiptView
		if err := tx.Where("charge_order_id = ?", result.ChargeOrderID).Take(&start).Error; err != nil || !start.Success || start.CommandID != result.StartCommandID || !start.PortID.Valid || start.PortID.Int64 != int64(result.PortID) {
			return ErrEndResultConflict
		}
		if err := tx.Create(&endReceiptRecord{ChargeOrderID: result.ChargeOrderID, StopCommandID: result.StopCommandID, MeterJSON: meterJSON}).Error; err != nil {
			return err
		}
		// 经 card 用例释放该订单的卡端口占用。
		if err := card.ClearActivePort(tx, result.ChargeOrderID); err != nil {
			return err
		}
		kwh := decimal.NewFromInt(int64(result.Meter.ChargedWh)).Shift(-3).StringFixed(4)
		// 计量时长在领域内是 uint32 秒，写入订单计量列前按无损方式提升为 int64。
		if err := orderstore.CompleteInTx(tx, result.ChargeOrderID, result.OrderNo, result.Meter.EndedAt, kwh, int64(result.Meter.ChargedSeconds)); err != nil {
			if errors.Is(err, orderstore.ErrLifecycleConflict) {
				return ErrEndResultConflict
			}
			return err
		}
		port := tx.Where("port_id = ? AND device_id = ? AND port_no = ? AND charge_order_id = ?", result.PortID, result.DeviceID, result.PortNo, result.ChargeOrderID).Delete(&activePortChargeRecord{})
		if port.Error != nil {
			return port.Error
		}
		if port.RowsAffected != 1 {
			return ErrEndResultConflict
		}
		if err := orderstore.AppendEventInTx(tx, orderstore.ChargeEventLogRecord{ChargeOrderID: result.ChargeOrderID, EventID: result.StopCommandID,
			Event: "charge_ended", Actor: "gateway", Detail: "device settlement frame accepted", OccurredAt: result.Meter.EndedAt}); err != nil {
			return err
		}
		envelope, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := settlement.EnqueueChargeBilling(tx, result.ChargeOrderID); err != nil {
			return err
		}
		return ops.AppendEventInTx(tx, ops.OutboxEvent{EventID: result.StopCommandID, Stream: "charge_ended_stream", EnvelopeJSON: envelope})
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return replayed, err
}
