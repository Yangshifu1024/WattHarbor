package http

import (
	"errors"
	cardstore "github.com/ChargePilot2026/charge-pilot/internal/card"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The board sees spendable shared-wallet balance; no balance is stored on-card.
func (a CardAPI) balance(c *gin.Context) {
	if !a.internal(c) {
		return
	}
	card := c.Query("card_no")
	if !money.CanonicalCardNumber(card) {
		httpapi.BadRequest(c, "卡号须为非零32位十进制整数")
		return
	}
	balance, err := cardstore.CardAvailableCents(c.Request.Context(), a.Store.DB, card)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.OK(c, gin.H{"valid": false, "balance_cents": 0})
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "卡状态及余额暂不可读取", nil)
		return
	}
	if balance < 0 {
		balance = 0
	}
	httpapi.OK(c, gin.H{"valid": true, "balance_cents": balance})
}
