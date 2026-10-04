package stop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

var (
	ErrPortIdentity             = errors.New("start receipt has no port identity")
	ErrBillingCutoffUnavailable = errors.New("billing cutoff persistence unavailable")
)

// AuthorizationService 校验用户归属和端口身份，并冻结计费截止点。
type AuthorizationService struct{ DB *gorm.DB }

// Stop 在授权成功时首写冻结计费截止点，重试不得延长截止时间。
func (s AuthorizationService) Stop(ctx context.Context, orderNo string, userID uint64) (ActiveStopAuthorization, error) {
	order, err := FindActiveStopAuthorization(ctx, s.DB, orderNo, userID)
	if err != nil {
		return order, err
	}
	if !order.PortID.Valid || order.PortID.Int64 <= 0 {
		return order, ErrPortIdentity
	}
	if err := RecordBillingCutoff(ctx, s.DB, order.ChargeOrderID, "stop_request", time.Now().UTC()); err != nil {
		return order, fmt.Errorf("%w: %w", ErrBillingCutoffUnavailable, err)
	}
	return order, nil
}
