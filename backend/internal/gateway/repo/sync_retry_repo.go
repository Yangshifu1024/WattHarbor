package repo

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeferSyncDelivery 保存 kind(start/end)、key(命令 UUID/事件十进制 ID) 的投递退避。
// category 为 invalid/unknown/dependency/retry；前两类进入 review 并每小时重查，后两类指数退避至30分钟。
// ctx 取消或数据库失败返回错误；只修改投递元数据，不改变设备、命令或 processed 状态及冻结请求。
func DeferSyncDelivery(ctx context.Context, db *gorm.DB, kind, key, category string) error {
	if (kind != "start" && kind != "end") || key == "" || len(key) > 64 || (category != "invalid" && category != "unknown" && category != "dependency" && category != "retry") {
		return errors.New("invalid sync retry")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 源记录先于退避行加锁，与成功标记保持同一锁序；迟到失败不重建已完成的退避。
		pending, err := syncDeliveryPending(tx, kind, key)
		if err != nil {
			return err
		}
		if !pending {
			return tx.Table("sync_delivery_retry").Where("kind=? AND delivery_key=?", kind, key).Delete(map[string]any{}).Error
		}
		if err := tx.Table("sync_delivery_retry").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"kind": gorm.Expr("kind")})}).Create(map[string]any{"kind": kind, "delivery_key": key}).Error; err != nil {
			return err
		}
		var row struct {
			Attempts uint32 // 已失败次数；同一持久投递跨进程累计
		}
		if err := tx.Table("sync_delivery_retry").Clauses(clause.Locking{Strength: "UPDATE"}).Where("kind=? AND delivery_key=?", kind, key).Take(&row).Error; err != nil {
			return err
		}
		state, delay := "retry", time.Minute
		for i := uint32(0); i < row.Attempts && delay < 30*time.Minute; i++ {
			delay *= 2
		}
		if delay > 30*time.Minute {
			delay = 30 * time.Minute
		}
		if category == "invalid" || category == "unknown" {
			state, delay = "review", time.Hour
		}
		attempts := row.Attempts
		if attempts < ^uint32(0) {
			attempts++
		}
		return tx.Table("sync_delivery_retry").Where("kind=? AND delivery_key=?", kind, key).Updates(map[string]any{"state": state, "attempts": attempts, "last_reason": category, "next_attempt_at": time.Now().UTC().Add(delay)}).Error
	})
}

// syncDeliveryPending 在调用方事务内锁读源记录；未完成返回true，已完成或缺源返回false。
// kind/key须为已校验身份；end使用规范十进制uint64，避免隐式转换误认源记录；SQL错误原样返回。
func syncDeliveryPending(tx *gorm.DB, kind, key string) (bool, error) {
	if kind == "start" {
		var row struct {
			ResultReported bool // 已确认启动回执的上报标记
		}
		err := tx.Table("charge_command").Select("result_reported").Clauses(clause.Locking{Strength: "UPDATE"}).Where("command_id=?", key).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return !row.ResultReported, err
	}
	id, err := strconv.ParseUint(key, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != key {
		return false, errors.New("invalid end delivery key")
	}
	var row struct {
		ProcessedAt sql.NullTime // 成功同步时刻；NULL表示未处理
	}
	err = tx.Table("device_event").Select("processed_at").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return !row.ProcessedAt.Valid, err
}

// deleteSyncRetry 在标记事务内删除已完成或缺源的元数据；仍未完成的源记录保留。
// 源行先于退避行加锁；删除失败由调用方回滚整批标记，本函数不提交事务。
func deleteSyncRetry(tx *gorm.DB, kind, key string) error {
	pending, err := syncDeliveryPending(tx, kind, key)
	if err != nil || pending {
		return err
	}
	return tx.Table("sync_delivery_retry").Where("kind=? AND delivery_key=?", kind, key).Delete(map[string]any{}).Error
}

// PurgeSyncDeliveryRetries 清理cutoff前最多limit条已完成或缺源退避，不按年龄删除待处理review/retry。
// 先筛除未完成项再限量，避免长期核实项占满清理窗口；每项短事务按源行→退避行锁序重查。
// limit须为1至500；返回已提交删除数，取消或SQL错误返回此前计数及错误，不修改源记录或冻结载荷。
func PurgeSyncDeliveryRetries(ctx context.Context, db *gorm.DB, cutoff time.Time, limit int) (int, error) {
	if cutoff.IsZero() || limit < 1 || limit > 500 {
		return 0, errors.New("invalid sync retry cleanup window")
	}
	var rows []struct {
		Kind        string // 投递类型
		DeliveryKey string // 完整投递主键
	}
	eligible := `(kind='start' AND NOT EXISTS (SELECT 1 FROM charge_command c WHERE c.command_id=CONVERT(sync_delivery_retry.delivery_key USING utf8mb4) COLLATE utf8mb4_bin AND c.result_reported=FALSE)) OR (kind='end' AND NOT EXISTS (SELECT 1 FROM device_event e WHERE e.id=CAST(sync_delivery_retry.delivery_key AS UNSIGNED) AND e.processed_at IS NULL))`
	if err := db.WithContext(ctx).Table("sync_delivery_retry").Select("kind,delivery_key").Where("updated_at<?", cutoff.UTC()).Where(eligible).Order("updated_at,kind,delivery_key").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	deleted := 0
	for _, row := range rows {
		var affected int64
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			pending, err := syncDeliveryPending(tx, row.Kind, row.DeliveryKey)
			if err != nil || pending {
				return err
			}
			result := tx.Table("sync_delivery_retry").Where("kind=? AND delivery_key=? AND updated_at<?", row.Kind, row.DeliveryKey, cutoff.UTC()).Delete(map[string]any{})
			affected = result.RowsAffected
			return result.Error
		})
		if err != nil {
			return deleted, err
		}
		deleted += int(affected)
	}
	return deleted, nil
}

// CleanupSyncRetries 清理30天前已完成或缺源历史，每轮最多500条；失败交运行时记录，下轮继续。
// DB与任务ctx由网关管理；仍待投递的核实记录保留，不决定业务结果。
func (s MySQLSink) CleanupSyncRetries(ctx context.Context) error {
	_, err := PurgeSyncDeliveryRetries(ctx, s.DB, time.Now().UTC().Add(-30*24*time.Hour), 500)
	return err
}
