package control

import (
	"context"
	"errors"
	"net/http"
	"time"

	"chargepilot/internal/gateway/repo"
	"chargepilot/internal/gateway/session"
	"chargepilot/internal/protocol/dc589"
)

// StartService 读取订单授权、保存启动命令并下发；发送结果不确定时安排补偿停机。
type StartService struct {
	// Orders 读取中央服务中的订单启动授权。
	Orders PaidOrderAuthorizer
	// Repo 保存启动命令、会话和端口占用。
	Repo repo.MySQLSink
	// Devices 查找当前设备连接并发送命令。
	Devices *session.Registry
}

// Start 先核对中央订单授权并保存命令，再向在线设备发送 START。
// 已有非 pending 命令直接返回；TCP 发送失败先保存 stopping，再尝试补偿 STOP。
func (s StartService) Start(ctx context.Context, orderNo string) (repo.StartReservation, error) {
	paid, err := s.Orders.PaidOrder(ctx, orderNo)
	if err != nil {
		return repo.StartReservation{}, err
	}
	reservation, err := s.Repo.ReserveStart(ctx, paid)
	if err != nil {
		return repo.StartReservation{}, err
	}
	if reservation.Status != "pending" {
		return reservation, nil
	}
	if _, err := dc589.BuildStart(dc589.StartCommand{Session: reservation.Wire.SessionID, Port: reservation.Wire.Port, OrderBCD: reservation.Wire.OrderBCD, Mode: dc589.ChargeMode(reservation.Wire.Mode), Quantity: reservation.Wire.Quantity, ConsumerType: reservation.Wire.ConsumerType, CardNumber: reservation.Wire.CardNumber, CardBalanceUnits: reservation.Wire.CardBalanceUnits}); err != nil {
		return repo.StartReservation{}, err
	}
	if !s.Devices.Connected(paid.DeviceID) {
		return reservation, nil
	}
	sent, err := s.Repo.MarkStartSent(ctx, reservation.CommandID)
	if err != nil {
		return repo.StartReservation{}, err
	}
	if !sent {
		status, err := s.Repo.StartStatus(ctx, orderNo)
		reservation.Status = status
		return reservation, err
	}
	reservation.Status = "sent"
	if err := s.Devices.Send(ctx, paid.DeviceID, reservation.Wire); err != nil {
		// 部分 TCP 写入可能已触发设备执行；先持久化 stopping 再尝试补偿 STOP，禁止重发 START。
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if persistErr := s.Repo.MarkStartStopping(cleanupCtx, reservation.CommandID, err); persistErr != nil {
			return repo.StartReservation{}, persistErr
		}
		_ = s.Devices.Send(cleanupCtx, paid.DeviceID, reservation.StopWire)
		reservation.Status = "stopping"
	}
	return reservation, nil
}

// startStatusCode 未支付、端口不可用或订单冲突映射 409，其余映射 503；返回 HTTP 状态。
func startStatusCode(err error) int {
	if errors.Is(err, ErrUnpaid) || errors.Is(err, repo.ErrPortUnavailable) || errors.Is(err, repo.ErrOrderConflict) {
		return http.StatusConflict
	}
	return http.StatusServiceUnavailable
}
