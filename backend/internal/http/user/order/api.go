// Package order 提供当前用户的订单查询、充电曲线和反馈接口。
package order

import (
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/order"
	"github.com/gin-gonic/gin"
)

// FeedbackAPI 将本业务族的用户请求交给用例，字段由 cmd 装配。
type FeedbackAPI struct {
	Feedback order.FeedbackService         // 解析订单并执行评价提交事务。
	Auth     authhttp.SessionAuthenticator // 验证当前会话与用户状态；失败已写响应，处理器立即返回。
}

// Register 向 r 注册本业务族的用户路由；只装配处理器，不执行查询或写入。
func (a FeedbackAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/user/charge/:order_id/feedback", a.submitFeedback)
}
