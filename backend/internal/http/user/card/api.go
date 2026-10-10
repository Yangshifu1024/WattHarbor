// Package card 提供当前用户的卡列表、挂失和解绑接口。
package card

import (
	"errors"

	"chargepilot/internal/card"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 提供当前用户的卡列表、挂失和解绑接口。
type API struct {
	Status card.CardStatusService        // 当前用户卡的列表、挂失和解绑用例。
	Auth   authhttp.SessionAuthenticator // 请求编号及唯一认证错误响应。
}

// Register 注册用户卡列表和状态变更路由。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/user/cards", a.cards)
	r.POST("/api/v1/user/cards/:card_no/status", a.cardStatus)
}

// cards 返回当前用户持有的卡。
func (a API) cards(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	rows, err := a.Status.List(c.Request.Context(), user)
	if err != nil {
		httpapi.Write(c, 503, 5001, "卡信息暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// cardStatus 提交当前用户的卡挂失或解绑请求。
func (a API) cardStatus(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		// Status 卡状态变更输入，card.CardStatus 的 C 端可写子集仅含挂失和解绑。
		Status card.CardStatus
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "仅可挂失或解绑自己的卡")
		return
	}
	err := a.Status.Change(c.Request.Context(), user, c.Param("card_no"), in.Status)
	if errors.Is(err, card.ErrCardStatusInput) {
		httpapi.BadRequest(c, "仅可挂失或解绑自己的卡")
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "卡不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "卡状态暂不可保存", nil)
		return
	}
	httpapi.OK(c, gin.H{"status": string(in.Status)})
}
