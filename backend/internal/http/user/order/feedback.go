package order

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	userhttp "wattharbor/internal/http/user"
	"wattharbor/internal/order"
	"wattharbor/internal/platform/httpapi"
)

// submitFeedback 提交当前用户的订单反馈。
// 业务订单号先查询再绑定 JSON；数字 ID 的归属和状态在提交事务中核对。
func (a FeedbackAPI) submitFeedback(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
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
			userhttp.WriteResourceFailure(c, err)
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
