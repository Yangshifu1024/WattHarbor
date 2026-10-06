package ops

import (
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/gin-gonic/gin"
)

// API 将本业务族的用户请求交给所属用例，字段由 cmd 装配。
type API struct {
	Announcements ops.AnnouncementQueryService // 读取公开公告及分页，不要求用户身份。
}

// Register 向 r 注册本业务族的原用户路由；只装配处理器，不执行查询或写入。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/user/announcement/list", a.announcements)
}
