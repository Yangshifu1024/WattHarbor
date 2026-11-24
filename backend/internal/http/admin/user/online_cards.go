package user

import (
	"errors"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/card"
	"wattharbor/internal/domain/operations"
	adminhttp "wattharbor/internal/http/admin"
	"wattharbor/internal/platform/httpapi"
)

// registerOnlineCards 注册在线卡查询、绑定和状态修改接口。
func (a API) registerOnlineCards(r *gin.Engine) {
	r.GET("/api/v1/admin/online-cards", a.Auth.Require("user.read"), a.onlineCards)
	r.POST("/api/v1/admin/online-cards/bind", a.Auth.Require("online_card.manage"), a.bindOnlineCard)
	r.POST("/api/v1/admin/online-cards/:id/status", a.Auth.Require("online_card.manage"), a.onlineCardStatus)
}

// onlineCards 返回当前管理员可读取的卡列表和权限。
func (a API) onlineCards(c *gin.Context) {
	actor := c.MustGet("admin_profile").(operations.Profile)
	rows, err := a.Cards.List(c.Request.Context(), actor)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "permissions": actor.Permissions})
}

// bindOnlineCard 校验绑定请求，绑定在线卡并返回卡 ID。
func (a API) bindOnlineCard(c *gin.Context) {
	var in struct {
		CardNo string            `json:"card_no"` // 非零 32 位无符号十进制卡号
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

// onlineCardStatus 修改指定在线卡状态并返回结果。
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
