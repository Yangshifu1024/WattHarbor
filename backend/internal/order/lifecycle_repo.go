// 订单状态只由本模块写入，启动和结束用例在各自回执事务中调用这些能力。
package order

import (
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrLifecycleConflict 表示订单已删除或状态已变化，不能再应用当前阶段回执。
// 这属于业务冲突，不能作为数据库故障重试。
var ErrLifecycleConflict = errors.New("order lifecycle state conflicts with the requested transition")

// LifecycleView 保存启动、结束回执所需的标识、状态与付款归属信息；
// 价格、计量与支付金额由各自模块读取。
type LifecycleView struct {
	ID             uint64        `gorm:"column:id"`               // 订单主键，与月首共同定位分区
	UserID         uint64        `gorm:"column:user_id"`          // 订单所有人，决定端口占用与退款归属
	DeviceID       string        `gorm:"column:device_id"`        // 设备全局编号，必须与回执一致
	PortNo         uint8         `gorm:"column:port_no"`          // 设备端口号，从 1 开始
	PaymentOrderID sql.NullInt64 `gorm:"column:payment_order_id"` // 关联支付主键；余额退款按此定位支付，未关联为空
	Status         string        `gorm:"column:status"`           // 持久化生命周期，阶段校验只认 paid、charging
	StartedAt      sql.NullTime  `gorm:"column:started_at"`       // 实际启动 UTC 时刻；结束计量不得早于该时刻
}

// LockForLifecycleInTx 按主键与业务单号锁定未删除订单。
// 订单不存在、已删除或单号不符时返回 gorm.ErrRecordNotFound，由调用方决定错误映射。
// 调用前须按锁序取得卡会话或钱包锁，事务由调用方提交。
func LockForLifecycleInTx(tx *gorm.DB, id uint64, orderNo string) (LifecycleView, error) {
	var row LifecycleView
	err := tx.Table("charge_order").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND order_no = ? AND deleted_at IS NULL", id, orderNo).Take(&row).Error
	return row, err
}

// ActivateInTx 把 paid 订单更新为 charging，并记录启动回执时刻。
// 谓词要求仍是 paid：行数为零表示订单已被其他路径更新，按 ErrLifecycleConflict 返回而不是补写。
// startedAt 为设备回执的 UTC 毫秒时刻，不使用墙钟。函数不提交事务，错误要求调用方回滚回执与事件。
func ActivateInTx(tx *gorm.DB, id uint64, orderNo string, startedAt time.Time) error {
	updated := tx.Model(&ChargeOrderRecord{}).
		Where("id = ? AND order_no = ? AND status = 'paid' AND deleted_at IS NULL", id, orderNo).
		Updates(map[string]any{"status": "charging", "started_at": startedAt.UTC()})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrLifecycleConflict
	}
	return nil
}

// MarkStartRejectedInTx 把 paid 订单更新为 refunding，并保存设备拒绝原因。
// 该状态只表示已进入退款流程，不表示款项已退回；余额路径退回后由 SetStatusRefunded 收尾。
// 渠道路径由 worker 依 refund_record 更新。谓词与行数要求同 ActivateInTx。
func MarkStartRejectedInTx(tx *gorm.DB, id uint64, orderNo, failureReason string) error {
	updated := tx.Model(&ChargeOrderRecord{}).
		Where("id = ? AND order_no = ? AND status = 'paid' AND deleted_at IS NULL", id, orderNo).
		Updates(map[string]any{"failure_reason": failureReason, "status": "refunding"})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrLifecycleConflict
	}
	return nil
}

// CompleteInTx 把 charging 订单更新为 completed，并保存结束计量。
// chargedKWh 是已按既有规则格式化的 kWh 十进制字符串，chargedSeconds 为设备累计秒数。
// 两者都取自结束回执的持久证据，不在此重算，也不使用确认费用。谓词要求仍是 charging。
// 因此迟到的重复结束不能覆盖已完成的订单计量。函数不提交事务。
func CompleteInTx(tx *gorm.DB, id uint64, orderNo string, endedAt time.Time, chargedKWh string, chargedSeconds int64) error {
	updated := tx.Model(&ChargeOrderRecord{}).
		Where("id = ? AND order_no = ? AND status = 'charging' AND deleted_at IS NULL", id, orderNo).
		Updates(map[string]any{"status": "completed", "ended_at": endedAt.UTC(), "charged_kwh": chargedKWh, "charged_seconds": chargedSeconds})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrLifecycleConflict
	}
	return nil
}
