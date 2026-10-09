package start

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"chargepilot/internal/card"
	"gorm.io/gorm"
)

var (
	// ErrCardSessionUnavailable 表示无法读取刷卡执行快照。
	ErrCardSessionUnavailable = errors.New("card execution snapshot unavailable")
	// ErrInvalidCardNumber 表示快照中的卡号不是非零 uint32 整数。
	ErrInvalidCardNumber = errors.New("invalid frozen card number")
)

// AuthorizationService 校验启动充电操作；协议单位由 HTTP 适配器转换。
type AuthorizationService struct {
	DB *gorm.DB /* central 授权数据连接，由进程管理 */
}

// StartAuthorization 汇集已付订单归属与可选刷卡首购快照，供网关组装 START；授权不改变订单状态，也不证明设备已经启动。
type StartAuthorization struct {
	PaidStartAuthorization        // 已核对支付、用户、设备和执行额度的订单信息
	ConsumerType           uint8  // 设备启动来源，2 扫码、3 在线卡
	CardNumber             uint32 // 冻结卡号的协议整数；扫码启动为 0
	WalletAfterCents       int64  // 刷卡扣款后的余额快照，单位分；扫码不使用
}

// Start 校验订单已付清，读取可选刷卡快照供设备启动。
// 缺单、无效卡号或读取失败时不能下发；授权成功不表示设备已启动。
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
