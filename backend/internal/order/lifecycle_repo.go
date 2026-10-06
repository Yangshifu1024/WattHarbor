// 本文件提供充电生命周期阶段所需的订单锁读与状态推进能力。
// 启动、结束回执由 charging 各阶段用例持有事务，在原位置调用这些函数，
// 订单表因此只有一个写入权威；调用方不直接写 charge_order，也不持有订单 ORM 模型。
package order

import (
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrLifecycleConflict 表示目标订单已不处于该阶段要求的前置状态。
// 行数为零说明订单状态已被其他事务推进或订单已删除；调用方据此判定回执冲突，
// 不能当作存储故障重试，也不能改写已进入其他阶段的订单。
var ErrLifecycleConflict = errors.New("order lifecycle state conflicts with the requested transition")

// LifecycleView 是启动与结束阶段核对订单所需的最小只读投影。
// 它只承载这两个阶段的准入、身份比对和资金归属字段，不含计价快照、计量或支付展示字段；
// 支付与结算金额仍由 payment、settlement 各自读取，订单基础包不反向依赖它们。
type LifecycleView struct {
	ID             uint64        `gorm:"column:id"`               // 原订单主键，与月首共同定位分区
	UserID         uint64        `gorm:"column:user_id"`          // 原订单所有人，决定端口占用与退款归属
	DeviceID       string        `gorm:"column:device_id"`        // 原设备全局身份，必须与回执一致
	PortNo         uint8         `gorm:"column:port_no"`          // 原设备端口号，从1开始
	PaymentOrderID sql.NullInt64 `gorm:"column:payment_order_id"` // 原关联支付主键；余额退款按此定位支付，未关联为空
	Status         string        `gorm:"column:status"`           // 原持久化生命周期，阶段准入只认 paid／charging
	StartedAt      sql.NullTime  `gorm:"column:started_at"`       // 原实际启动 UTC 时刻；结束计量不得早于该时刻
}

// LockForLifecycleInTx 在调用方 tx 中按主键与业务单号加锁读取订单，并返回 LifecycleView。
// 谓词同时要求未删除：软删除的订单与单号不符的订单都按缺行返回 gorm.ErrRecordNotFound，
// 由调用方决定映射为回执冲突还是服务故障。SELECT 保持原全列形态，只是不再装载阶段不使用的列。
// 本函数不改状态、不提交事务；调用方须已按既有顺序锁好卡会话或钱包行。
func LockForLifecycleInTx(tx *gorm.DB, id uint64, orderNo string) (LifecycleView, error) {
	var row LifecycleView
	err := tx.Table("charge_order").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND order_no = ? AND deleted_at IS NULL", id, orderNo).Take(&row).Error
	return row, err
}

// ActivateInTx 把 paid 订单推进为 charging，并记录原启动回执时刻。
// 谓词要求仍是 paid：行数为零表示订单已被其他路径推进，按 ErrLifecycleConflict 返回而不是补写。
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

// MarkStartRejectedInTx 把 paid 订单推进为 refunding，并保存原设备拒绝原因。
// 该状态只表示已进入退款流程，不表示款项已退回；余额路径退回后由 SetStatusRefunded 收尾，
// 渠道路径由 worker 依 refund_record 推进。谓词与行数要求同 ActivateInTx。
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

// CompleteInTx 把 charging 订单推进为 completed，并保存原结束计量。
// chargedKWh 是已按原规则格式化的 kWh 十进制字符串，chargedSeconds 为设备累计秒数；
// 两者都取自结束回执的持久证据，不在此重算，也不使用确认费用。谓词要求仍是 charging，
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
