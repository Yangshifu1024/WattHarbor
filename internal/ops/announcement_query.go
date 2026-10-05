package ops

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// PublicAnnouncement 是公开公告投影，保留原数字身份、UTC时刻及可空截止。
type PublicAnnouncement struct {
	ID      uint64     `json:"id"`       // 公告原主键
	Title   string     `json:"title"`    // 公开标题
	Content string     `json:"content"`  // 公开正文
	StartAt time.Time  `json:"start_at"` // 原生效时刻
	EndAt   *time.Time `json:"end_at"`   // 原截止；null为未设置
}

// AnnouncementQueryService 拥有公开有效期和范围过滤，不接受用户位置的推断。
type AnnouncementQueryService struct {
	DB *gorm.DB // 所属公告连接或调用方事务
}

// List 在ctx下以page、size读取原候选窗口并返回全局公告或存储错误，无写入。
// 保留原offset=(page-1)*size、候选size*3后过滤的契约，不把无位置用户当作站点／城市受众。
func (s AnnouncementQueryService) List(ctx context.Context, page, size int) ([]PublicAnnouncement, error) {
	rows, err := (AnnouncementQueryRepo{DB: s.DB}).Active(ctx, time.Now().UTC(), (page-1)*size, size*3, false)
	if err != nil {
		return nil, err
	}
	return publicAnnouncements(rows, 0), nil
}

// StationAnnouncements 在ctx下返回stationID可见的原前50条全局／站点候选或存储错误。
// 无城市上下文不返回城市公告；坏目标JSON按原规则不匹配，不写公告或站点。
func (s AnnouncementQueryService) StationAnnouncements(ctx context.Context, stationID uint64) ([]PublicAnnouncement, error) {
	rows, err := (AnnouncementQueryRepo{DB: s.DB}).Active(ctx, time.Now().UTC(), 0, 50, true)
	if err != nil {
		return nil, err
	}
	return publicAnnouncements(rows, stationID), nil
}

// publicAnnouncements 从rows生成stationID的公开字段，0只保留全局；返回非nil数组，无IO。
func publicAnnouncements(rows []announcementQueryRow, stationID uint64) []PublicAnnouncement {
	items := []PublicAnnouncement{}
	for _, row := range rows {
		visible := row.Scope == "global"
		if !visible && row.Scope == "station" && stationID != 0 && row.RawIDs != nil {
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
