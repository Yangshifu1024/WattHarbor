package http

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"time"
)

// 公告列表。
func (a UserAccountAPI) announcements(c *gin.Context) {
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()
	// 返回有效期内的全局公告，以及当前用户可达站点范围内的公告。
	base := a.AdminDB.WithContext(ctx).Table("announcement").
		Where("deleted_at IS NULL AND status = 'published' AND start_at <= ? AND (end_at IS NULL OR end_at >= ?)", now, now)
	var rows []struct {
		ID      uint64     `gorm:"column:id"`
		Title   string     `gorm:"column:title"`
		Content string     `gorm:"column:content"`
		Scope   string     `gorm:"column:scope"`
		RawIDs  *string    `gorm:"column:target_ids"`
		StartAt time.Time  `gorm:"column:start_at"`
		EndAt   *time.Time `gorm:"column:end_at"`
	}
	if err := base.Order("start_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize * 3).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "公告暂时无法读取", nil)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		if row.Scope == "global" {
			items = append(items, gin.H{"id": row.ID, "title": row.Title, "content": row.Content, "start_at": row.StartAt, "end_at": row.EndAt})
			continue
		}
		// 站点或城市维度的公告仍需做一次归属判断；
		// 只有全局公告在没有位置信息时才无歧义。
		continue
	}
	httpapi.OK(c, gin.H{"items": items, "page": page, "page_size": pageSize})
}

// ---- 站点 ----
