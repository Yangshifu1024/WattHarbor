package repo

import (
	"context"
	"errors"
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
