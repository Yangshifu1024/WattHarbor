// Package control 提供网关内部设备控制、事件同步和读数查询接口。
package control

import (
	"context"
	"net/http"
	"strings"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/gateway/repo"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// StartAPI 适配内部启动请求，输出已保存的受理结果及设备确认标志，不把 HTTP 接受视为设备执行成功。
type StartAPI struct {
	// Service 处理命令，返回处理结果或错误。
	Service interface {
		// Start 接收请求 context 与订单号，返回已保存的启动结果或授权、端口及存储错误；设备确认仍由回执更新。
		Start(context.Context, string) (repo.StartReservation, error)
	}
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 在 router 注册路由；调用方须提供非 nil router。
func (a StartAPI) Register(router *gin.Engine) {
	router.POST(gatewaycontract.StartPath, a.start)
}

// start 校验服务令牌与订单号并请求启动；写 202 受理结果或 400、401、409、503，不把 TCP 写入当作设备确认。
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
