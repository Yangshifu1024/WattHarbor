package control

import (
	"context"
	"errors"
	"net/http"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/gateway/repo"
	"chargepilot/internal/gateway/session"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// Compensation 编排原启动补偿的持久状态与设备 STOP 下发，复用原命令及会话，不重新生成 START。
type Compensation struct {
	// Repo 原 MySQLSink 持久能力，保留命令身份与事务规则。
	Repo repo.MySQLSink
	// Devices 原设备连接注册表，查询在线状态并发送原补偿 STOP。
	Devices *session.Registry
}

// Request 接收 ctx、orderNo 与 commandID，先持久化原启动补偿事实，连接在线时标记发送并下发原 STOP；返回持久化或下发错误，不创建第二条 START。
func (c Compensation) Request(ctx context.Context, orderNo, commandID string) error {
	job, needsStop, err := c.Repo.CompensateStart(ctx, orderNo, commandID)
	if err != nil || !needsStop {
		return err
	}
	if !c.Devices.Connected(job.DeviceID) {
		return nil
	}
	if err := c.Repo.MarkStopSent(ctx, job.CommandID); err != nil {
		return err
	}
	return c.Devices.Send(ctx, job.DeviceID, job.Wire)
}

// RetryStopping 接收 ctx，读取原到期补偿任务，为在线设备依次标记发送并下发同一 STOP。
// 返回首次持久化或发送错误，继续其它任务，不创建第二条 START 或新会话。
func (c Compensation) RetryStopping(ctx context.Context) error {
	jobs, err := c.Repo.StoppingJobs(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, job := range jobs {
		if !c.Devices.Connected(job.DeviceID) {
			continue
		}
		if err := c.Repo.MarkStopSent(ctx, job.CommandID); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := c.Devices.Send(ctx, job.DeviceID, job.Wire); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CompensationAPI 适配内部启动补偿请求，负责服务鉴权、输入和错误映射，业务操作交由 Request 能力执行。
type CompensationAPI struct {
	// Service 所属命令用例能力，保留原返回事实及错误。
	Service interface {
		// Request 接收 ctx、orderNo 与 commandID，先持久化原启动补偿事实，连接在线时标记发送并下发原 STOP；返回持久化或下发错误，不创建第二条 START。
		Request(context.Context, string, string) error
	}
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 接收非 nil router，注册本类型原路由与处理器，无数据库或设备 IO，不启动 HTTP 监听。
func (a CompensationAPI) Register(router *gin.Engine) {
	router.POST(gatewaycontract.CompensateStartRoute, a.handle)
}

// handle 接收 c，鉴权并校验原补偿请求后调用 Service；成功返回 accepted，失配写 409，输入、鉴权或执行失败写原 400、401、503。
func (a CompensationAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var body gatewaycontract.CompensateStartRequest
	if c.Param("order_no") == "" || len(c.Param("order_no")) > 64 || c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid compensation request")
		return
	}
	if err := a.Service.Request(c.Request.Context(), c.Param("order_no"), body.CommandID); err != nil {
		if errors.Is(err, repo.ErrOrderConflict) {
			httpapi.Write(c, http.StatusConflict, 2000, "command does not match order", nil)
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "compensation send failed", nil)
		return
	}
	httpapi.OK(c, gatewaycontract.CompensateStartReply{Accepted: true})
}
