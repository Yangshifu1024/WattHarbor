package card

import (
	"errors"

	"chargepilot/internal/card"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 适配用户卡列表和状态端点，查询及变更由服务拥有。
type API struct {
	Status card.CardStatusService        // 当前用户卡的列表、挂失和解绑用例。
	Auth   authhttp.SessionAuthenticator // 请求编号及唯一认证错误响应。
}

// Register 在 r 注册用户列表与状态端点；无业务 IO 或返回值。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/user/cards", a.cards)
	r.POST("/api/v1/user/cards/:card_no/status", a.cardStatus)
}

// cards 从 c 验证用户身份，调用卡列表用例并写 items 或 503 响应。
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

// cardStatus 从请求读取用户、卡号及状态，调用原子状态用例并映射输入／缺卡／存储错误。
func (a API) cardStatus(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		// Status 卡状态变更输入，由用例限制为挂失或解绑。
		Status string
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
	httpapi.OK(c, gin.H{"status": in.Status})
}
