package control

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"net/http"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/session"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// StopAuthorizer 用户停机用例消费的中央订单归属及充电状态授权能力，返回原授权事实或错误。
type StopAuthorizer interface {
	// ActiveStop 接收 ctx、orderNo 与 userID，获取中央原停机授权事实；业务拒绝映射 ErrNotCharging，其余错误保留，不写数据库。
	ActiveStop(context.Context, string, uint64) (repo.ActiveOrder, error)
}

// UserStopService 编排用户停机授权、原 STOP 持久受理与同身份下发恢复，设备断电以原回执为准。
type UserStopService struct {
	// Orders 中央订单授权窄能力，不在网关写中央订单或资金。
	Orders StopAuthorizer
	// Repo 原 MySQLSink 持久能力，保留命令身份与事务规则。
	Repo repo.MySQLSink
	// Devices 原设备连接注册表，查询在线状态并发送原用户 STOP。
	Devices *session.Registry
}

// Stop 接收 ctx、orderNo 与 userID，复用原停机或经中央授权持久受理，在线时标记发送并尝试原 STOP；返回受理事实或授权、存储错误，保留发送错误的原恢复语义。
func (s UserStopService) Stop(ctx context.Context, orderNo string, userID uint64) (repo.StopReservation, error) {
	reservation, err := s.Repo.ExistingUserStop(ctx, orderNo, userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return repo.StopReservation{}, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		active, err := s.Orders.ActiveStop(ctx, orderNo, userID)
		if err != nil {
			return repo.StopReservation{}, err
		}
		reservation, err = s.Repo.ReserveUserStop(ctx, active)
		if err != nil {
			return repo.StopReservation{}, err
		}
	}
	if reservation.Status == "pending" && s.Devices.Connected(reservation.DeviceID) {
		if err := s.Repo.MarkUserStopSent(ctx, reservation.CommandID); err != nil {
			return repo.StopReservation{}, err
		}
		reservation.Status = "sent"
		// 写入失败或部分写入时保留停止命令，重试使用同一 STOP 会话标识。
		// HTTP 接受请求不表示设备已经断电。
		_ = s.Devices.Send(ctx, reservation.DeviceID, reservation.Wire)
	}
	return reservation, nil
}

// RetryPending 接收 ctx，读取到期原停机并为在线设备依次标记和发送同一 STOP；返回首次错误，继续其余独立任务，不创建新会话身份。
func (s UserStopService) RetryPending(ctx context.Context) error {
	jobs, err := s.Repo.PendingUserStops(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, job := range jobs {
		if !s.Devices.Connected(job.DeviceID) {
			continue
		}
		if err := s.Repo.MarkUserStopSent(ctx, job.CommandID); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := s.Devices.Send(ctx, job.DeviceID, job.Wire); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// UserStopAPI 适配内部用户停机请求，负责鉴权、输入和 HTTP 映射，返回受理事实而非提前确认停止。
type UserStopAPI struct {
	// Service 所属命令用例能力，保留原返回事实及错误。
	Service UserStopService
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 接收非 nil router，注册本类型原路由与处理器，无数据库或设备 IO，不启动 HTTP 监听。
func (a UserStopAPI) Register(router *gin.Engine) {
	router.POST(gatewaycontract.StopPath, a.handle)
}

// handle 接收 c，鉴权校验原停机请求并调用 Service；成功写原 202 受理（Stopped=false），冲突写 409，其余写原 400、401 或 503。
func (a UserStopAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request gatewaycontract.StopRequest
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "invalid stop request")
		return
	}
	stop, err := a.Service.Stop(c.Request.Context(), request.OrderNo, request.UserID)
	if errors.Is(err, ErrNotCharging) || errors.Is(err, repo.ErrOrderConflict) || errors.Is(err, repo.ErrPortUnavailable) {
		httpapi.Write(c, http.StatusConflict, 2000, "charge order is not stoppable", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "stop command unavailable", nil)
		return
	}
	httpapi.Write(c, http.StatusAccepted, 0, "accepted", gatewaycontract.StopReply{Accepted: true, Stopped: false, CommandID: stop.CommandID, Status: stop.Status})
}
