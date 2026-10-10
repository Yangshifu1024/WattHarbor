package ops

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// PublicAnnouncement 是公开公告资料，保留数字 ID、UTC 时间和可空截止时间。
type PublicAnnouncement struct {
	ID      uint64     `json:"id"`       // 公告主键
	Title   string     `json:"title"`    // 公开标题
	Content string     `json:"content"`  // 公开正文
	StartAt time.Time  `json:"start_at"` // 生效时刻
	EndAt   *time.Time `json:"end_at"`   // 截止；null 为未设置
}

// AnnouncementQueryService 筛选有效公开公告和目标范围，不推断未提供的用户位置。
type AnnouncementQueryService struct {
	DB *gorm.DB // 所属公告连接或调用方事务
}

// List 按请求分页位置读取 size*3 个候选，再筛选全局公告；未提供位置不能匹配站点或城市受众。
func (s AnnouncementQueryService) List(ctx context.Context, page, size int) ([]PublicAnnouncement, error) {
	rows, err := (AnnouncementQueryRepo{DB: s.DB}).Active(ctx, time.Now().UTC(), (page-1)*size, size*3, false)
	if err != nil {
		return nil, err
	}
	return publicAnnouncements(rows, 0), nil
}

// StationAnnouncements 从最新 50 条全局或站点公告中筛选本站可见项，不返回城市公告，非法目标 JSON 不匹配。
func (s AnnouncementQueryService) StationAnnouncements(ctx context.Context, stationID uint64) ([]PublicAnnouncement, error) {
	rows, err := (AnnouncementQueryRepo{DB: s.DB}).Active(ctx, time.Now().UTC(), 0, 50, true)
	if err != nil {
		return nil, err
	}
	return publicAnnouncements(rows, stationID), nil
}

// publicAnnouncements 转换公开公告字段；站点 ID 为 0 时只保留全局公告，结果始终为非 nil 数组。
func publicAnnouncements(rows []announcementQueryRow, stationID uint64) []PublicAnnouncement {
	items := []PublicAnnouncement{}
	for _, row := range rows {
		visible := row.Scope == AnnouncementScopeGlobal
		if !visible && row.Scope == AnnouncementScopeStation && stationID != 0 && row.RawIDs != nil {
			var ids []string
			if json.Unmarshal([]byte(*row.RawIDs), &ids) == nil {
				for _, id := range ids {
					if id == strconv.FormatUint(stationID, 10) {
						visible = true
						break
					}
				}
			}
		}
		if visible {
			items = append(items, PublicAnnouncement{row.ID, row.Title, row.Content, row.StartAt, row.EndAt})
		}
	}
	return items
}
