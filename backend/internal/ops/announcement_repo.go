package ops

import (
	"context"
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
	"time"
)

// AnnouncementRow 是原管理公告清单；JSON目标保留null／空数组及字符串原值，不转数值身份。
type AnnouncementRow struct {
	ID        uint64          `json:"id"`                                  // 原公告主键
	Title     string          `json:"title"`                               // 原标题
	Content   string          `json:"content"`                             // 原正文
	Scope     string          `json:"scope"`                               // 原发布范围
	TargetIDs json.RawMessage `json:"target_ids" gorm:"column:target_ids"` // 原JSON目标，可为null
	Status    string          `json:"status"`                              // 原发布状态
	StartAt   time.Time       `json:"start_at"`                            // 生效时刻
	EndAt     *time.Time      `json:"end_at"`                              // 可空失效时刻
}

// AnnouncementRepo 只读写所属公告表，不接收HTTP表名或动态列。
type AnnouncementRepo struct {
	DB *gorm.DB /* 公告连接，由cmd管理 */
}

// List 在ctx下按原ID倒序读取未删除公告，返回非nil清单或取消／存储错误，不限制原有效期。
func (s AnnouncementRepo) List(ctx context.Context) ([]AnnouncementRow, error) {
	rows := []AnnouncementRow{}
	err := s.DB.WithContext(ctx).Table("announcement").Select("id,title,content,scope,target_ids,status,start_at,end_at").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error
	return rows, err
}

// Insert 在tx下按in及actorID插入已发布公告，返回原自增ID或序列化／存储错误，不自行提交。
// 调用方先核对输入和当前操作者，公告与审计同事务；nil目标编码为JSON null，沿用原存储语义。
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
