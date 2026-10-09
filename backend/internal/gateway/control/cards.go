package control

import (
	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/gateway/session"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
	"encoding/hex"
	"github.com/gin-gonic/gin"
	"net/http"
)

// CardReplyAPI 向设备发送已保存的刷卡拒绝或余额回复。
type CardReplyAPI struct {
	Devices      *session.Registry // 进程拥有的在线连接，发送响应受请求取消管理
	ServiceToken string            // 内部调用身份凭据
}

// Register 注册内部卡回复路由。
func (a CardReplyAPI) Register(r *gin.Engine) { r.POST(contract.CardReplyPath, a.reply) }

// reply 校验服务令牌、卡号和会话后发送设备回复。
// accepted 只表示连接写入完成，还须等待设备处理。
func (a CardReplyAPI) reply(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var in contract.CardReplyRequest
	if c.ShouldBindJSON(&in) != nil || in.DeviceID == "" || (in.CardNumber == 0 && !(in.Kind == protocol.CommandCardBalance && in.Invalid)) || (in.Kind != protocol.CommandCardDenied && in.Kind != protocol.CommandCardBalance) {
		httpapi.BadRequest(c, "invalid card reply")
		return
	}
	raw, err := hex.DecodeString(in.Session)
	if err != nil || len(raw) != 6 {
		httpapi.BadRequest(c, "invalid reply session")
		return
	}
	var session [6]byte
	copy(session[:], raw)
	if err := a.Devices.Send(c.Request.Context(), in.DeviceID, protocol.Command{Kind: in.Kind, SessionID: session, CardNumber: in.CardNumber, CardBalanceUnits: in.BalanceUnits, CardInvalid: in.Invalid}); err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "card reply not delivered", nil)
		return
	}
	httpapi.OK(c, contract.CardDeliveryReply{Accepted: true})
}
