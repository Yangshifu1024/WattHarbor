package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ErrSimulationLookup 表示原支付／意图读取失败，保留缺行原因供适配。
var ErrSimulationLookup = errors.New("simulation payment lookup failed")

// ErrNotSimulationCheckout 表示原预支付参数不是模拟渠道，不允许本地确认。
var ErrNotSimulationCheckout = errors.New("payment is not a simulated checkout")

// SimulationService 仅由 simulation 模式进程装配，使用正式回调事务确认本地支付。
type SimulationService struct {
	DB       *gorm.DB        // payment 原支付及预支付参数连接，由进程管理
	Callback CallbackService // 金额、交易身份、到账及迟到退款的权威入口
}

// ConfirmUser 在 ctx 下仅确认 userID 所属 no 原支付，并核对保存的渠道为 simulation。
// 返回正常回调结果；缺单、非模拟参数和回调冲突独立返回，不接受用户提交的金额。
// 稳定交易身份与原创建时刻使同一模拟确认使用相同摘要，不重复到账。
func (s SimulationService) ConfirmUser(ctx context.Context, userID uint64, no string) (PaymentCallbackResult, error) {
	order, err := FindDevelopmentPayment(ctx, s.DB, no, userID)
	if err != nil {
		return PaymentCallbackResult{}, fmt.Errorf("%w: %w", ErrSimulationLookup, err)
	}
	var prepay ChargePrepayRecord
	var params struct {
		Provider string `json:"provider"` /* 首次保存的渠道标签 */
	}
	if err := s.DB.WithContext(ctx).Where("payment_order_id=?", order.ID).Take(&prepay).Error; err != nil || json.Unmarshal(prepay.ParamsJSON, &params) != nil || params.Provider != "simulation" {
		return PaymentCallbackResult{}, ErrNotSimulationCheckout
	}
	return s.Callback.Apply(ctx, VerifiedPayment{Provider: "simulation", MerchantID: "local-simulation", AppID: s.Callback.ExpectedAppID, MerchantOrderNo: order.OrderNo, TransactionID: "SIMH5" + order.OrderNo, OpenID: order.OpenID, PaidCents: order.TotalCents, PaidAt: order.CreatedAt})
}

// ConfirmIntent 在 ctx 下为内部已授权 no 支付意图构造稳定模拟交易身份。
// 返回正式回调结果或缺意图／存储／金额冲突，不读取 HTTP 或服务令牌，不推进设备状态。
func (s SimulationService) ConfirmIntent(ctx context.Context, no string) (PaymentCallbackResult, error) {
	var intent PaymentIntentRecord
	if err := s.DB.WithContext(ctx).Where("merchant_order_no=?", no).Take(&intent).Error; err != nil {
		return PaymentCallbackResult{}, fmt.Errorf("%w: %w", ErrSimulationLookup, err)
	}
	return s.Callback.Apply(ctx, VerifiedPayment{Provider: "simulation", MerchantID: "local-simulation", AppID: s.Callback.ExpectedAppID, MerchantOrderNo: intent.MerchantOrderNo, TransactionID: "SIMTX" + intent.IntentID, OpenID: intent.OpenID, PaidCents: intent.TotalCents, PaidAt: time.Now().UTC()})
}
