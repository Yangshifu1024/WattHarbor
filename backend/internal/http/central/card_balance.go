package central

import (
	"chargepilot/internal/card"
	contract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/httpapi"
	"errors"
	"github.com/gin-gonic/gin"
)

// balance 校验服务令牌，返回卡有效性和可用余额，金额单位为分。
func (a CardAPI) balance(c *gin.Context) {
	if !a.internal(c) {
		return
	}
	out, err := a.Status.Balance(c.Request.Context(), c.Query("card_no"))
	if errors.Is(err, card.ErrCardStatusInput) {
		httpapi.BadRequest(c, "卡号须为非零32位十进制整数")
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "卡状态及余额暂不可读取", nil)
		return
	}
	httpapi.OK(c, contract.CardBalanceReply{Valid: out.Valid, BalanceCents: out.BalanceCents})
}
