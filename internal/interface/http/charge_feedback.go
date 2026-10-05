package http

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// submitFeedback 从 c 读取原用户、订单引用和 JSON，调用完整反馈用例，保持原输入与状态文案。
// 业务单号预查仍先于 JSON 绑定；数字主键的归属及状态由同一提交事务核对。
func (a UserAccountAPI) submitFeedback(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	orderID, err := a.Feedback.ResolveReference(c.Request.Context(), userID, c.Param("order_id"))
	var input *order.FeedbackInputError
	if err != nil {
		switch {
		case errors.As(err, &input):
			httpapi.BadRequest(c, input.Message)
		case errors.Is(err, gorm.ErrRecordNotFound):
			httpapi.Write(c, 404, 1004, "订单不存在", nil)
		default:
			resourceWriteFailure(c, err)
		}
		return
	}
	var in order.FeedbackInput
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "反馈请求无效")
		return
	}
	id, err := a.Feedback.Submit(c.Request.Context(), userID, orderID, in)
	switch {
	case errors.As(err, &input):
		httpapi.BadRequest(c, input.Message)
	case errors.Is(err, order.ErrFeedbackOrderNotReady):
		httpapi.Write(c, 404, 1004, "订单不存在或不可评价", nil)
	case errors.Is(err, order.ErrFeedbackExists):
		httpapi.Write(c, 409, 2009, "该订单已评价", nil)
	case err != nil:
		httpapi.Write(c, 503, 5003, "反馈提交失败，请稍后重试", nil)
	default:
		httpapi.OK(c, gin.H{"submitted": true, "feedback_id": id})
	}
}
