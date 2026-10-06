package order

import (
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/gin-gonic/gin"
)

// FeedbackAPI 将本业务族的用户请求交给所属用例，字段由 cmd 装配。
type FeedbackAPI struct {
	Feedback order.FeedbackService         // 解析所属订单并执行评价提交事务。
	Auth     authhttp.SessionAuthenticator // 验证当前会话与用户状态；失败已写响应，处理器立即返回。
}

// Register 向 r 注册本业务族的原用户路由；只装配处理器，不执行查询或写入。
func (a FeedbackAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/user/charge/:order_id/feedback", a.submitFeedback)
}
