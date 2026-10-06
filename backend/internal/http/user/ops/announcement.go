package ops

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// announcements 从c解析原分页并调用ops公开公告能力，返回原筛选窗口或存储错误。
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
