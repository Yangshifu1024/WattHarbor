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
type AuthorizationService struct {
	DB *gorm.DB /* central 授权及截止点连接，由进程管理 */
}

// Stop 在授权成功时首写冻结计费截止点，重试不得延长截止时间。
// ctx 取消 IO；orderNo/userID 为原订单和用户身份。返回授权事实或缺单、端口身份、
// 持久化错误；错误时结果不可下发。成功仅表示截止已冻结，不表示网关受理或设备结束。
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
