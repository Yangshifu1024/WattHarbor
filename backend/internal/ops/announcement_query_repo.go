package ops

import (
	"context"
	"gorm.io/gorm"
	"time"
)

// AnnouncementQueryRepo 只读所属公告表，范围解释由用例完成。
type AnnouncementQueryRepo struct {
	DB *gorm.DB // 所属连接或调用方事务
}

// announcementQueryRow 保存公告字段和目标 JSON 字符串。
type announcementQueryRow struct {
	ID      uint64            `gorm:"column:id"`         // 主键
	Title   string            `gorm:"column:title"`      // 标题
	Content string            `gorm:"column:content"`    // 正文
	Scope   AnnouncementScope `gorm:"column:scope"`      // 发布范围
	RawIDs  *string           `gorm:"column:target_ids"` // 目标字符串数组；坏值不匹配
	StartAt time.Time         `gorm:"column:start_at"`   // 生效时刻
	EndAt   *time.Time        `gorm:"column:end_at"`     // 可空截止
}

// Active 按当前时间和分页查询有效 published 公告，可限制为全局和站点公告。
// 截止时间等于当前时间仍有效，返回非 nil 数组。
// status 与 scope 的字面量对应 announcement.status 与 announcement.scope 两个列词表，
// 查询语句不因类型化拆分。
func (r AnnouncementQueryRepo) Active(ctx context.Context, now time.Time, offset, limit int, stationOnly bool) ([]announcementQueryRow, error) {
	rows := []announcementQueryRow{}
	q := r.DB.WithContext(ctx).Table("announcement").Select("id,title,content,scope,target_ids,start_at,end_at").
		Where("deleted_at IS NULL AND status=? AND start_at<=? AND (end_at IS NULL OR end_at>=?)", AnnouncementPublished, now, now)
	if stationOnly {
		q = q.Where("scope IN ('" + AnnouncementScopeGlobal + "','" + AnnouncementScopeStation + "')")
	}
	err := q.Order("start_at DESC,id DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, err
}
