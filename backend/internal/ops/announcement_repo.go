package ops

import (
	"chargepilot/internal/persist"
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"time"
)

// AnnouncementRow 是后台公告记录，JSON 目标保留 null、空数组和字符串值。
type AnnouncementRow struct {
	ID        uint64          `json:"id"`                                  // 公告主键
	Title     string          `json:"title"`                               // 标题
	Content   string          `json:"content"`                             // 正文
	Scope     string          `json:"scope"`                               // 发布范围
	TargetIDs json.RawMessage `json:"target_ids" gorm:"column:target_ids"` // JSON 目标，可为 null
	Status    string          `json:"status"`                              // 发布状态
	StartAt   time.Time       `json:"start_at"`                            // 生效时刻
	EndAt     *time.Time      `json:"end_at"`                              // 可空失效时刻
}

// AnnouncementRepo 只读写所属公告表，不接收 HTTP 表名或动态列。
type AnnouncementRepo struct {
	DB *gorm.DB /* 公告连接，由 cmd 管理 */
}

// List 倒序读取未删除公告，包含有效期外记录，返回非 nil 数组。
func (s AnnouncementRepo) List(ctx context.Context) ([]AnnouncementRow, error) {
	rows := []AnnouncementRow{}
	err := s.DB.WithContext(ctx).Table("announcement").Select("id,title,content,scope,target_ids,status,start_at,end_at").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error
	return rows, err
}

// Insert 在调用方事务中保存 published 公告、目标和创建人；nil 目标编码为 JSON null。
// 调用方完成权限和内容校验，并同时保存审计。
func (s AnnouncementRepo) Insert(tx *gorm.DB, in AnnouncementInput, actorID uint64) (uint64, error) {
	targets, err := json.Marshal(in.TargetIDs)
	if err != nil {
		return 0, err
	}
	if err := tx.Table("announcement").Create(map[string]any{"title": in.Title, "content": in.Content, "scope": in.Scope, "target_ids": string(targets), "start_at": in.StartAt, "end_at": in.EndAt, "status": "published", "created_by": actorID}).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}
