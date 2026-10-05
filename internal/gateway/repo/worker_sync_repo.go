package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// ListStartResults 接收 ctx、db 与可选 commandID，按原条件读取已回执未上报启动命令，updated_at 升序最多五十条；返回原列投影及 SQL 错误，无写入或新事务。
func ListStartResults(ctx context.Context, db *gorm.DB, commandID string) ([]SyncCommandRow, error) {
	query := db.WithContext(ctx).Model(&SyncCommandRow{}).
		Where("status IN ('acked','rejected') AND result_reported = FALSE AND result_code IS NOT NULL AND ack_at IS NOT NULL")
	if commandID != "" {
		query = query.Where("command_id = ?", commandID)
	}
	var rows []SyncCommandRow
	err := query.Order("updated_at").Limit(50).Find(&rows).Error
	return rows, err
}

// MarkStartResultsReported 接收 ctx、db 与原 commandIDs，在同一事务按原输入顺序逐条条件更新 result_reported；返回累计影响行数及错误，计数仅在 err=nil 时代表提交结果，任一失败回滚，不更换命令身份。
func MarkStartResultsReported(ctx context.Context, db *gorm.DB, commandIDs []string) (int, error) {
	marked := 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, id := range commandIDs {
			res := tx.Model(&SyncCommandRow{}).
				Where("command_id = ? AND status IN ('acked','rejected') AND result_reported = FALSE", id).
				Update("result_reported", true)
			if res.Error != nil {
				return res.Error
			}
			marked += int(res.RowsAffected)
		}
		return nil
	})
	return marked, err
}

// MarkDeviceEventsProcessed 接收 ctx、db 与原 ids，在同一事务按原输入顺序标记仍未处理事件；返回累计影响行数及错误，计数仅在 err=nil 时代表提交结果，失败回滚，重放不重复推进。
func MarkDeviceEventsProcessed(ctx context.Context, db *gorm.DB, ids []uint64) (int, error) {
	marked := 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, id := range ids {
			affected, err := MarkSyncEventProcessed(tx, id)
			if err != nil {
				return err
			}
			marked += int(affected)
		}
		return nil
	})
	return marked, err
}

// ListUnprocessedEndEvents 接收 ctx 与 db，读取原未处理 ConsumerType 2/3 结束事件，按 id 升序最多五十条；返回原 id/载荷投影及 SQL 错误，无标记写入。
func ListUnprocessedEndEvents(ctx context.Context, db *gorm.DB) ([]SyncEventRow, error) {
	var rows []SyncEventRow
	err := db.WithContext(ctx).
		Select("id, event_json").
		Where("event_type = 'charge_end' AND processed_at IS NULL AND JSON_EXTRACT(event_json, '$.ConsumerType') IN (2,3)").
		Order("id").Limit(50).Find(&rows).Error
	return rows, err
}

// FindChargeCommandByOrder 接收 ctx、db 与 orderID，保留原无排序 Limit(1).Find 读取；返回原命令、RowsAffected 非零 found 与 SQL 错误，无记录不是错误，无写入。
func FindChargeCommandByOrder(ctx context.Context, db *gorm.DB, orderID uint64) (SyncCommandRow, bool, error) {
	var row SyncCommandRow
	res := db.WithContext(ctx).Model(&SyncCommandRow{}).Where("charge_order_id = ?", orderID).Limit(1).Find(&row)
	return row, res.RowsAffected != 0, res.Error
}

// ReleaseChargingPort 接收 ctx、db、portID 与 orderNo，仅对原订单占用且 charging 的端口更新 idle/NULL；
// 返回影响行数及 SQL 错误，不额外读取、添加显式行锁或组合事务，保留 GORM 原更新行为。
func ReleaseChargingPort(
	ctx context.Context,
	db *gorm.DB,
	portID uint64,
	orderNo string,
) (int64, error) {
	res := db.WithContext(ctx).Model(&SyncPortRow{}).
		Where("id = ? AND current_order_id = ? AND status = 'charging'", portID, orderNo).
		Updates(map[string]any{"status": "idle", "current_order_id": nil})
	return res.RowsAffected, res.Error
}

// ReadSyncPort 接收 ctx、db 与 portID，以原 status/current_order_id 列及 Take 读取端口状态；返回原投影或缺失、SQL 错误，不加锁、写入或新建事务。
func ReadSyncPort(ctx context.Context, db *gorm.DB, portID uint64) (SyncPortRow, error) {
	var port SyncPortRow
	err := db.WithContext(ctx).Select("status, current_order_id").Where("id = ?", portID).Take(&port).Error
	return port, err
}

// ListMeterSamples 接收 ctx、db、device、ackAt、endAt 与 endID，读取原闭区间 UTC 心跳且 id 不超过 endID；按 id 升序最多一万零八十一条（含原哨兵预算），返回 id/载荷及 SQL 错误，无写入。
func ListMeterSamples(
	ctx context.Context,
	db *gorm.DB,
	device string,
	ackAt, endAt time.Time,
	endID uint64,
) ([]SyncEventRow, error) {
	var rows []SyncEventRow
	err := db.WithContext(ctx).Select("id, event_json").
		Where("device_id=? AND event_type='heartbeat' AND id<=? AND received_at>=? AND received_at<=?", device, endID, ackAt.UTC(), endAt.UTC()).
		Order("id").Limit(10081).Find(&rows).Error
	return rows, err
}
