package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// ListStartResults 查询已回执且到期的未上报启动命令，可按 commandID 筛选；最多 50 条。
// 首投按命令更新时间、重试按持久到期时间共同排序，异常首批和持续新投递均不能挤掉另一类记录。
func ListStartResults(ctx context.Context, db *gorm.DB, commandID string) ([]SyncCommandRow, error) {
	query := db.WithContext(ctx).Model(&SyncCommandRow{}).
		Select("charge_command.*").
		Joins("LEFT JOIN sync_delivery_retry x ON x.kind='start' AND x.delivery_key=charge_command.command_id").
		Where("charge_command.status IN ('acked','rejected') AND result_reported = FALSE AND result_code IS NOT NULL AND ack_at IS NOT NULL").
		Where("x.next_attempt_at IS NULL OR x.next_attempt_at<=UTC_TIMESTAMP(3)")
	if commandID != "" {
		query = query.Where("command_id = ?", commandID)
	}
	var rows []SyncCommandRow
	err := query.Order("COALESCE(x.next_attempt_at,charge_command.updated_at),charge_command.command_id").Limit(50).Find(&rows).Error
	return rows, err
}

// MarkStartResultsReported 按输入顺序在同一事务内标记命令已上报。
// 任一失败整批回滚；只有 err=nil 时，返回计数才代表已提交的影响行数。
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

// MarkDeviceEventsProcessed 按输入顺序在同一事务内标记事件已处理，重复请求不重复更新。
// 任一失败整批回滚；只有 err=nil 时，返回计数才代表已提交的影响行数。
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

// ListUnprocessedEndEvents 读取最多 50 条未处理且到期的 ConsumerType 2/3 结束事件。
// 首投接收时间与重试到期时间共同排序；不修改冻结载荷或事件处理状态。
func ListUnprocessedEndEvents(ctx context.Context, db *gorm.DB) ([]SyncEventRow, error) {
	var rows []SyncEventRow
	err := db.WithContext(ctx).
		Select("device_event.id, event_json").
		Joins("LEFT JOIN sync_delivery_retry x ON x.kind='end' AND x.delivery_key=CAST(device_event.id AS CHAR)").
		Where("event_type = 'charge_end' AND processed_at IS NULL AND JSON_EXTRACT(event_json, '$.ConsumerType') IN (2,3)").
		Where("x.next_attempt_at IS NULL OR x.next_attempt_at<=UTC_TIMESTAMP(3)").
		Order("COALESCE(x.next_attempt_at,device_event.received_at),device_event.id").Limit(50).Find(&rows).Error
	return rows, err
}

// FindChargeCommandByOrder 用 Limit(1).Find 查询订单命令，不指定排序；无记录返回 found=false。
func FindChargeCommandByOrder(ctx context.Context, db *gorm.DB, orderID uint64) (SyncCommandRow, bool, error) {
	var row SyncCommandRow
	res := db.WithContext(ctx).Model(&SyncCommandRow{}).Where("charge_order_id = ?", orderID).Limit(1).Find(&row)
	return row, res.RowsAffected != 0, res.Error
}

// ReleaseChargingPort 仅对订单占用且 charging 的端口更新 idle/NULL；
// 返回影响行数及 SQL 错误，不额外读取、添加显式行锁或组合事务，保留 GORM 更新行为。
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

// ReadSyncPort 查询端口状态和当前订单；不加锁，缺失时返回错误。
func ReadSyncPort(ctx context.Context, db *gorm.DB, portID uint64) (SyncPortRow, error) {
	var port SyncPortRow
	err := db.WithContext(ctx).Select("status, current_order_id").Where("id = ?", portID).Take(&port).Error
	return port, err
}

// ListMeterSamples 查询 UTC 时间闭区间内、ID 不超过 endID 的心跳；按 ID 升序最多 10081 条，含一条超限检查记录。
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
