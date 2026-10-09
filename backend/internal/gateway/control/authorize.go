package control

import (
	"context"
	"errors"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/gateway/repo"
)

var ErrUnpaid = errors.New("charge order is not fully paid or has no device limit")
var ErrNotCharging = errors.New("charge order is not charging for this user")

// PaidOrderAuthorizer 启动用例消费的中央已支付订单授权能力，仅返回原授权事实及错误，不在网关写中央订单或资金。
type PaidOrderAuthorizer interface {
	// PaidOrder 接收 ctx 与 orderNo，调用中央启动授权并转换原本地模型；业务拒绝映射 ErrUnpaid，其余错误保留，不写网关或中央数据库。
	PaidOrder(context.Context, string) (repo.PaidOrder, error)
}

// CentralAuthorizer 将服务契约转换为网关本地模型；HTTP 配置由 cmd 装配。
type CentralAuthorizer struct {
	// Central 由装配方注入的具名中央授权客户端。
	Central interface {
		// StartAuthorization 接收请求 context 与原订单号，返回中央启动授权事实或错误；由具名客户端执行原服务调用，不创建新身份。
		StartAuthorization(context.Context, string) (centralcontract.StartAuthorizationReply, error)
		// StopAuthorization 接收请求 context、原订单号与用户身份，返回中央停机授权事实或错误；不在网关写中央业务状态。
		StopAuthorization(context.Context, string, uint64) (centralcontract.StopAuthorizationReply, error)
	}
}

// PaidOrder 接收 ctx 与 orderNo，调用中央启动授权并转换原本地模型；业务拒绝映射 ErrUnpaid，其余错误保留，不写网关或中央数据库。
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

// ActiveStop 接收 ctx、orderNo 与 userID，获取中央原停机授权事实；业务拒绝映射 ErrNotCharging，其余错误保留，不写数据库。
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
