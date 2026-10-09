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

// CardReplyAPI 处理已冻结的设备回复，不执行资金决策。
type CardReplyAPI struct {
	Devices      *session.Registry // 进程拥有的在线连接，发送响应受请求取消管理
	ServiceToken string            // 内部调用身份凭据
}

// Register 在 r 注册共享内部回复端点；不发送设备命令或打开资源。
func (a CardReplyAPI) Register(r *gin.Engine) { r.POST(contract.CardReplyPath, a.reply) }

// reply 从请求读取设备、会话和已保存的决定，鉴权后发送；失败返回对应 HTTP 错误。
// accepted 只说明连接发送完成，不能据此确认设备处理或扣款结果。
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
