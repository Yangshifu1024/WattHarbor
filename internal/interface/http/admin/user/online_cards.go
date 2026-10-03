package user

import (
	"fmt"
	cardstore "github.com/ChargePilot2026/charge-pilot/internal/card"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strings"
)

func (a API) registerOnlineCards(r *gin.Engine) {
	r.GET("/api/v1/admin/online-cards", a.Auth.Require("user.read"), a.onlineCards)
	r.POST("/api/v1/admin/online-cards/bind", a.Auth.Require("online_card.manage"), a.bindOnlineCard)
	r.POST("/api/v1/admin/online-cards/:id/status", a.Auth.Require("online_card.manage"), a.onlineCardStatus)
}
func (a API) onlineCards(c *gin.Context) {
	rows := []cardstore.OnlineCard{}
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Order("id DESC").Limit(500).Find(&rows).Error; err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(operations.Profile).Permissions})
}
func (a API) bindOnlineCard(c *gin.Context) {
	var in struct {
		CardNo string            `json:"card_no"`
		UserID httpapi.DecimalID `json:"user_id"`
		Reason string            `json:"reason"`
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	userID := uint64(in.UserID)
	in.CardNo = strings.TrimSpace(in.CardNo)
	if !money.CanonicalCardNumber(in.CardNo) || userID == 0 || !operations.ValidText(strings.TrimSpace(in.Reason), 500) {
		httpapi.BadRequest(c, "请填写32位十进制卡号、用户及核验绑定依据")
		return
	}
	var id uint64
	store := cardstore.CardAdminStore{DB: a.Store.UserDB}
	actor := c.MustGet("admin_profile").(operations.Profile).ID
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := store.FindActiveUser(tx, userID); err != nil {
			return err
		}
		card, err := store.LockCardByNo(tx, in.CardNo)
		if err != nil {
			return err
		}
		if card.ID == 0 {
			// 卡号不存在则新建。并发绑定同一张卡时两个请求被行锁串行化，
			// 后到的那个会取到已建的卡，不会重复插入。
			if id, err = store.CreateOnlineCard(tx, in.CardNo, userID); err != nil {
				return err
			}
		} else {
			id = card.ID
			if card.Status != "unbound" {
				if card.UserID == userID && card.Status == "active" {
					return nil
				}
				return fmt.Errorf("%w: %s", admin.ErrConflict, cardstore.ErrCardAlreadyBound)
			}
			if err := store.BindOnlineCard(tx, id, userID); err != nil {
				return err
			}
		}
		return store.AppendOnlineCardAudit(tx, cardstore.OnlineCardAuditInput{CardID: id, ActorID: actor, Action: "bind", Detail: in.Reason})
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
func (a API) onlineCardStatus(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !chargehttp.AdminOneOf(in.Status, "active lost disabled unbound") || in.Status == "" || !operations.ValidText(in.Reason, 500) {
		httpapi.BadRequest(c, "卡状态与依据无效")
		return
	}
	store := cardstore.CardAdminStore{DB: a.Store.UserDB}
	actor := c.MustGet("admin_profile").(operations.Profile).ID
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		card, err := store.LockCardByID(tx, id)
		if err != nil {
			return err
		}
		if card.Status == "unbound" && in.Status != "unbound" {
			return admin.ErrConflict
		}
		if err := store.SetOnlineCardStatus(tx, card, in.Status); err != nil {
			return err
		}
		return store.AppendOnlineCardAudit(tx, cardstore.OnlineCardAuditInput{CardID: id, ActorID: actor, Action: in.Status, Detail: in.Reason})
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
