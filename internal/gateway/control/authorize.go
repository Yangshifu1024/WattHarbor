package control

import (
	"context"
	"errors"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
)

var ErrUnpaid = errors.New("charge order is not fully paid or has no device limit")
var ErrNotCharging = errors.New("charge order is not charging for this user")

type PaidOrderAuthorizer interface {
	PaidOrder(context.Context, string) (store.PaidOrder, error)
}

// CentralAuthorizer 将服务契约转换为网关本地模型；HTTP 配置由 cmd 装配。
type CentralAuthorizer struct {
	Central interface {
		StartAuthorization(context.Context, string) (centralcontract.StartAuthorization, error)
		StopAuthorization(context.Context, string, uint64) (centralcontract.StopAuthorization, error)
	}
}

func (a CentralAuthorizer) PaidOrder(ctx context.Context, orderNo string) (store.PaidOrder, error) {
	if a.Central == nil {
		return store.PaidOrder{}, errors.New("central authorizer is not configured")
	}
	data, err := a.Central.StartAuthorization(ctx, orderNo)
	var business centralcontract.BusinessError
	if errors.Is(err, centralcontract.ErrAuthorizationDenied) || errors.As(err, &business) {
		return store.PaidOrder{}, ErrUnpaid
	}
	if err != nil {
		return store.PaidOrder{}, err
	}
	return store.PaidOrder(data), nil
}

func (a CentralAuthorizer) ActiveStop(ctx context.Context, orderNo string, userID uint64) (store.ActiveOrder, error) {
	if a.Central == nil {
		return store.ActiveOrder{}, errors.New("central authorizer is not configured")
	}
	data, err := a.Central.StopAuthorization(ctx, orderNo, userID)
	var business centralcontract.BusinessError
	if errors.Is(err, centralcontract.ErrAuthorizationDenied) || errors.As(err, &business) {
		return store.ActiveOrder{}, ErrNotCharging
	}
	if err != nil {
		return store.ActiveOrder{}, err
	}
	return store.ActiveOrder(data), nil
}
