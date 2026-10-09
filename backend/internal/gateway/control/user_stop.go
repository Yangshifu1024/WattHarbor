package control

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"net/http"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/gateway/repo"
	"chargepilot/internal/gateway/session"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// StopAuthorizer 用户停机用例需要的中央订单归属及充电状态授权能力，返回授权结果或错误。
type StopAuthorizer interface {
	// ActiveStop 查询中央停机授权；拒绝停机返回 ErrNotCharging，其他错误原样返回。
	ActiveStop(context.Context, string, uint64) (repo.ActiveOrder, error)
}

// UserStopService 核对用户停机授权、保存命令并下发，重试使用同一 STOP 会话。
type UserStopService struct {
	// Orders 核对中央订单的用户归属和停机条件。
	Orders StopAuthorizer
	// Repo 保存用户停机命令和端口关联。
	Repo repo.MySQLSink
	// Devices 设备连接注册表，查询在线状态并发送用户 STOP。
	Devices *session.Registry
}

// Stop 复用已保存的停机命令，或经中央授权创建命令；在线时尝试发送 STOP。
// 发送失败保留命令供后续恢复，不把发送完成当成设备断电。
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

// RetryPending 向在线设备重发到期 STOP，保留原会话编号；单项失败后继续其余命令。
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

// UserStopAPI 适配内部用户停机请求，负责鉴权、输入和 HTTP 映射，返回受理结果而非提前确认停止。
type UserStopAPI struct {
	// Service 处理命令，返回处理结果或错误。
	Service UserStopService
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 在 router 注册路由；调用方须提供非 nil router。
func (a UserStopAPI) Register(router *gin.Engine) {
	router.POST(gatewaycontract.StopPath, a.handle)
}

// handle 鉴权校验停机请求并调用 Service；成功写 202 受理（Stopped=false），冲突写 409，其余写 400、401 或 503。
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
