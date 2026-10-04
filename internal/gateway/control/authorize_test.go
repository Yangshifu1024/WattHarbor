package control

import (
	"context"
	"errors"
	"testing"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
)

type authorizationClientStub struct{ err error }

func (s authorizationClientStub) StartAuthorization(context.Context, string) (centralcontract.StartAuthorization, error) {
	return centralcontract.StartAuthorization{ChargeOrderID: 9, PaymentOrderID: 7, OrderNo: "C-9", UserID: 42, CardBalanceUnits: 45, ChargeQuantity: 1200}, s.err
}
func (s authorizationClientStub) StopAuthorization(context.Context, string, uint64) (centralcontract.StopAuthorization, error) {
	return centralcontract.StopAuthorization{ChargeOrderID: 9, OrderNo: "C-9", UserID: 42, PortID: 3, StartCommandID: "start-1"}, s.err
}
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
