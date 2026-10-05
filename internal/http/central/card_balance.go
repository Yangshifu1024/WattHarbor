package central

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// balance 校验c内部令牌后按原card_no读取所属卡状态及钱包余额，返回原valid／balance_cents。
// 无效卡号400、读取失败503，金额单位分；不变更卡状态、余额或发起渠道调用。
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
