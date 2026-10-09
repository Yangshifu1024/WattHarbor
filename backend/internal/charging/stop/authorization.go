package stop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

var (
	// ErrPortIdentity 表示启动回执没有有效的网关端口 ID。
	ErrPortIdentity = errors.New("start receipt has no port identity")
	// ErrBillingCutoffUnavailable 表示计费截止时刻保存失败。
	ErrBillingCutoffUnavailable = errors.New("billing cutoff persistence unavailable")
)

// AuthorizationService 校验用户归属和端口编号，并冻结计费截止点。
type AuthorizationService struct {
	DB *gorm.DB /* central 授权及截止点连接，由进程管理 */
}

// Stop 校验订单归属与端口后冻结计费截止点，重试不能延长截止时间。
// 失败时不得下发指令；成功只表示已冻结，不表示网关受理或设备停止。
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
