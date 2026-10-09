package ops

import (
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// announcements 分页查询有效期内的公开公告。
func (a API) announcements(c *gin.Context) {
	page, size, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	items, err := a.Announcements.List(c.Request.Context(), page, size)
	if err != nil {
		httpapi.Write(c, 503, 5003, "公告暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": items, "page": page, "page_size": size})
}
