package control

import (
	"context"
	"errors"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/gateway/repo"
)

// ErrUnpaid 订单未满足启动所需的支付或设备额度条件。
var ErrUnpaid = errors.New("charge order is not fully paid or has no device limit")

// ErrNotCharging 订单不属于该用户，或当前不可停止。
var ErrNotCharging = errors.New("charge order is not charging for this user")

// PaidOrderAuthorizer 查询中央服务，确认订单是否允许启动。
type PaidOrderAuthorizer interface {
	// PaidOrder 查询中央启动授权；拒绝启动返回 ErrUnpaid，其他错误原样返回。
	PaidOrder(context.Context, string) (repo.PaidOrder, error)
}

// CentralAuthorizer 将服务契约转换为网关本地模型；HTTP 配置由 cmd 装配。
type CentralAuthorizer struct {
	// Central 由装配方注入的专用中央授权客户端。
	Central interface {
		// StartAuthorization 查询中央服务，确认订单是否允许启动。
		StartAuthorization(context.Context, string) (centralcontract.StartAuthorizationReply, error)
		// StopAuthorization 查询中央服务，确认该用户是否允许停止订单。
		StopAuthorization(context.Context, string, uint64) (centralcontract.StopAuthorizationReply, error)
	}
}

// PaidOrder 查询中央启动授权；拒绝启动返回 ErrUnpaid，其他错误原样返回。
func (a CentralAuthorizer) PaidOrder(ctx context.Context, orderNo string) (repo.PaidOrder, error) {
	if a.Central == nil {
		return repo.PaidOrder{}, errors.New("central authorizer is not configured")
	}
	data, err := a.Central.StartAuthorization(ctx, orderNo)
	var business centralcontract.BusinessError
	if errors.Is(err, centralcontract.ErrAuthorizationDenied) || errors.As(err, &business) {
		return repo.PaidOrder{}, ErrUnpaid
	}
	if err != nil {
		return repo.PaidOrder{}, err
	}
	return repo.PaidOrder(data), nil
}

// ActiveStop 查询中央停机授权；拒绝停机返回 ErrNotCharging，其他错误原样返回。
func (a CentralAuthorizer) ActiveStop(ctx context.Context, orderNo string, userID uint64) (repo.ActiveOrder, error) {
	if a.Central == nil {
		return repo.ActiveOrder{}, errors.New("central authorizer is not configured")
	}
	data, err := a.Central.StopAuthorization(ctx, orderNo, userID)
	var business centralcontract.BusinessError
	if errors.Is(err, centralcontract.ErrAuthorizationDenied) || errors.As(err, &business) {
		return repo.ActiveOrder{}, ErrNotCharging
	}
	if err != nil {
		return repo.ActiveOrder{}, err
	}
	return repo.ActiveOrder(data), nil
}
