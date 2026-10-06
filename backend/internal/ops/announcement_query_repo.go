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

// announcementQueryRow 保留公告原列和目标字符串JSON，不映射设备或城市事实。
type announcementQueryRow struct {
	ID      uint64     `gorm:"column:id"`         // 原主键
	Title   string     `gorm:"column:title"`      // 标题
	Content string     `gorm:"column:content"`    // 正文
	Scope   string     `gorm:"column:scope"`      // 原发布范围
	RawIDs  *string    `gorm:"column:target_ids"` // 目标字符串数组；坏值不匹配
	StartAt time.Time  `gorm:"column:start_at"`   // 生效时刻
	EndAt   *time.Time `gorm:"column:end_at"`     // 可空截止
}

// Active 在ctx下按now、offset、limit读取未删除已发布的有效公告，stationOnly限制全局／站点候选。
// 返回有序非nil数组或原存储错误，只读且不补造截止；截止等于now仍有效。
func (r AnnouncementQueryRepo) Active(ctx context.Context, now time.Time, offset, limit int, stationOnly bool) ([]announcementQueryRow, error) {
	rows := []announcementQueryRow{}
	q := r.DB.WithContext(ctx).Table("announcement").Select("id,title,content,scope,target_ids,start_at,end_at").Where("deleted_at IS NULL AND status='published' AND start_at<=? AND (end_at IS NULL OR end_at>=?)", now, now)
	if stationOnly {
		q = q.Where("scope IN ('global','station')")
	}
	err := q.Order("start_at DESC,id DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, err
}
