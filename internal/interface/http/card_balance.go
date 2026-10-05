package http

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// balance 从 c 校验内部令牌和卡号，调用共享钱包读取用例，写原有效性／金额或错误响应。
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
	httpapi.OK(c, contract.CardBalance{Valid: out.Valid, BalanceCents: out.BalanceCents})
}
