package start

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	"gorm.io/gorm"
)

var (
	ErrCardSessionUnavailable = errors.New("card execution snapshot unavailable")
	ErrInvalidCardNumber      = errors.New("invalid frozen card number")
)

// AuthorizationService 校验启动充电操作；协议单位由 HTTP 适配器转换。
type AuthorizationService struct{ DB *gorm.DB }

type StartAuthorization struct {
	PaidStartAuthorization
	ConsumerType     uint8
	CardNumber       uint32
	WalletAfterCents int64
}

func (s AuthorizationService) Start(ctx context.Context, orderNo string) (StartAuthorization, error) {
	order, err := FindPaidStartAuthorization(ctx, s.DB, orderNo)
	if err != nil {
		return StartAuthorization{}, err
	}
	out := StartAuthorization{PaidStartAuthorization: order, ConsumerType: 2}
	session, err := card.FindCardChargeSession(ctx, s.DB, order.ChargeOrderID)
	if err != nil {
		return out, fmt.Errorf("%w: %w", ErrCardSessionUnavailable, err)
	}
	if session.ChargeOrderID != 0 {
		number, err := strconv.ParseUint(session.CardNo, 10, 32)
		if err != nil || number == 0 {
			return out, ErrInvalidCardNumber
		}
		out.ConsumerType, out.CardNumber, out.WalletAfterCents = 3, uint32(number), session.WalletAfterCents
	}
	return out, nil
}
