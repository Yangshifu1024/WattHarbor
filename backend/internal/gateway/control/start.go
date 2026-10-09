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

// StartService 编排中央订单授权、网关启动受理与设备下发，发送不确定时持久化补偿恢复入口。
type StartService struct {
	// Orders 中央订单授权接口，不在网关写中央订单或资金。
	Orders PaidOrderAuthorizer
	// Repo MySQLSink 持久能力，保留命令标识与事务规则。
	Repo repo.MySQLSink
	// Devices 设备连接注册表，查询在线状态并发送受管理协议命令。
	Devices *session.Registry
}

// Start 先中央授权并已保存的受理，再为在线设备标记和下发 START。
// 返回受理结果或授权、端口、协议及 SQL 错误；TCP 失败持久 stopping 并尝试 STOP，确认由匹配会话的设备回执更新。
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
