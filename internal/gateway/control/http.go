package control

import (
	"context"
	"net/http"
	"strings"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// StartAPI 适配内部启动请求，输出原持久受理事实及设备确认标志，不把 HTTP 接受视为设备执行成功。
type StartAPI struct {
	// Service 所属命令用例能力，保留原返回事实及错误。
	Service interface {
		// Start 接收请求 context 与原订单号，返回持久启动受理事实或原授权、端口及存储错误；设备确认仍由原回执推进。
		Start(context.Context, string) (repo.StartReservation, error)
	}
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 接收非 nil router，注册本类型原路由与处理器，无数据库或设备 IO，不启动 HTTP 监听。
func (a StartAPI) Register(router *gin.Engine) {
	router.POST(gatewaycontract.StartPath, a.start)
}

// start 接收 c，校验服务令牌与原订单号并请求启动；写原 202 受理事实或 400、401、409、503，不把 TCP 写入当作设备确认。
func (a StartAPI) start(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request gatewaycontract.StartRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.OrderNo) != request.OrderNo {
		httpapi.BadRequest(c, "invalid order number")
		return
	}
	command, err := a.Service.Start(c.Request.Context(), request.OrderNo)
	if err != nil {
		httpapi.Write(c, startStatusCode(err), 2000, "charge start unavailable", nil)
		return
	}
	httpapi.Write(c, http.StatusAccepted, 0, "accepted", gatewaycontract.StartReply{
		OrderNo: command.OrderNo, CommandID: command.CommandID, Status: command.Status,
		DeviceAcknowledged: command.Status == "acked",
	})
}
