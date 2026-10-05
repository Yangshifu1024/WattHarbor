package user

import (
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	adminhttp "github.com/ChargePilot2026/charge-pilot/internal/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// registerOnlineCards 在 r 注册卡列表及原管理路由；权限中间件每次重读当前权限，无业务 IO。
func (a API) registerOnlineCards(r *gin.Engine) {
	r.GET("/api/v1/admin/online-cards", a.Auth.Require("user.read"), a.onlineCards)
	r.POST("/api/v1/admin/online-cards/bind", a.Auth.Require("online_card.manage"), a.bindOnlineCard)
	r.POST("/api/v1/admin/online-cards/:id/status", a.Auth.Require("online_card.manage"), a.onlineCardStatus)
}

// onlineCards 从 c 取实时身份调用卡列表用例，写原列表和本地权限或公开错误。
func (a API) onlineCards(c *gin.Context) {
	actor := c.MustGet("admin_profile").(operations.Profile)
	rows, err := a.Cards.List(c.Request.Context(), actor)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "permissions": actor.Permissions})
}

// bindOnlineCard 从 c 严格解析绑定依据和完整主键，调用所属用例，写原卡 ID 或错误。
func (a API) bindOnlineCard(c *gin.Context) {
	var in struct {
		CardNo string            `json:"card_no"` // 原非零32位无符号十进制卡号
		UserID httpapi.DecimalID `json:"user_id"` // 完整用户主键，大整数由十进制字符串保留精度
		Reason string            `json:"reason"`  // 人工核验依据
	}
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	id, err := a.Cards.Bind(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), card.CardBindInput{CardNo: in.CardNo, UserID: uint64(in.UserID), Reason: in.Reason})
	if errors.Is(err, card.ErrCardAdminInput) {
		httpapi.BadRequest(c, "请填写32位十进制卡号、用户及核验绑定依据")
		return
	}
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// onlineCardStatus 从 c 解析主键及状态依据，调用原子状态用例并保留原响应。
func (a API) onlineCardStatus(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var in card.CardChangeInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	err := a.Cards.ChangeStatus(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), id, in)
	if errors.Is(err, card.ErrCardAdminInput) {
		httpapi.BadRequest(c, "卡状态与依据无效")
		return
	}
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
