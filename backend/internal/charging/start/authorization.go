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
type AuthorizationService struct {
	DB *gorm.DB /* central 授权数据连接，由进程管理 */
}

// StartAuthorization 汇集已付订单归属与可选刷卡首购快照，供网关组装 START；授权不改变订单状态，也不证明设备已经启动。
type StartAuthorization struct {
	PaidStartAuthorization        // 已核对支付、用户及设备归属的订单执行事实
	ConsumerType           uint8  // 设备消费来源，2 扫码、3 在线卡
	CardNumber             uint32 // 冻结卡号的协议整数；扫码启动为 0
	WalletAfterCents       int64  // 刷卡扣款后的余额快照，单位分；扫码不使用
}

// Start 按 orderNo 校验已付清订单并读取首次刷卡执行快照，ctx 取消数据库操作。
// 成功返回设备执行事实；缺单、会话读取失败或无效卡号返回错误，错误时结果不可下发。
// 本方法只授权，不改变订单状态，不把授权等同设备已启动。
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
