package control

import (
	"context"
	"errors"
	"testing"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
)

// authorizationClientStub 返回固定授权资料与可配置错误，用于网关错误映射检查，不连接中央服务。
type authorizationClientStub struct {
	// err 当前授权夹具配置的返回错误；nil 为固定成功事实，不查询中央服务。
	err error
}

// StartAuthorization 忽略传入的 context 与订单号，返回固定启动授权回复及 s.err；只供错误映射和字段透传检查，不证明中央业务或资金准入。
func (s authorizationClientStub) StartAuthorization(context.Context, string) (centralcontract.StartAuthorizationReply, error) {
	return centralcontract.StartAuthorizationReply{ChargeOrderID: 9, PaymentOrderID: 7, OrderNo: "C-9", UserID: 42, CardBalanceUnits: 45, ChargeQuantity: 1200}, s.err
}

// StopAuthorization 忽略传入的 context、订单号与用户身份，返回固定停止授权回复及 s.err；不查询真实订单或设备。
func (s authorizationClientStub) StopAuthorization(context.Context, string, uint64) (centralcontract.StopAuthorizationReply, error) {
	return centralcontract.StopAuthorizationReply{ChargeOrderID: 9, OrderNo: "C-9", UserID: 42, PortID: 3, StartCommandID: "start-1"}, s.err
}

// TestCentralAuthorizerMapsOnlyAuthorizationRejection 使用 t 核对中央拒绝映射为网关业务错误、成功字段透传及非法响应错误保留；无返回值，夹具不发 HTTP 请求，不证明资金或鉴权真实准入。
func TestCentralAuthorizerMapsOnlyAuthorizationRejection(t *testing.T) {
	for _, err := range []error{centralcontract.ErrAuthorizationDenied, centralcontract.BusinessError{Code: 2000}} {
		a := CentralAuthorizer{Central: authorizationClientStub{err: err}}
		if _, got := a.PaidOrder(context.Background(), "C-9"); !errors.Is(got, ErrUnpaid) {
			t.Fatalf("start=%v", got)
		}
		if _, got := a.ActiveStop(context.Background(), "C-9", 42); !errors.Is(got, ErrNotCharging) {
			t.Fatalf("stop=%v", got)
		}
	}
	a := CentralAuthorizer{Central: authorizationClientStub{}}
	paid, err := a.PaidOrder(context.Background(), "C-9")
	if err != nil || paid.ChargeOrderID != 9 || paid.CardBalanceUnits != 45 || paid.ChargeQuantity != 1200 {
		t.Fatalf("paid=%+v err=%v", paid, err)
	}
	stop, err := a.ActiveStop(context.Background(), "C-9", 42)
	if err != nil || stop.PortID != 3 || stop.StartCommandID != "start-1" {
		t.Fatalf("stop=%+v err=%v", stop, err)
	}
	a.Central = authorizationClientStub{err: centralcontract.ErrInvalidResponse}
	if _, err := a.PaidOrder(context.Background(), "C-9"); !errors.Is(err, centralcontract.ErrInvalidResponse) {
		t.Fatalf("invalid response hidden: %v", err)
	}
}
