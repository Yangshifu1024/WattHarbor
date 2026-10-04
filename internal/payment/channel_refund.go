package payment

import (
	"context"
	"errors"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
)

// ErrRefundNotFound 表示渠道按原退款号确认无记录，执行器此时才允许创建同号退款。
var ErrRefundNotFound = errors.New("refund does not exist at provider")

// RefundRequest 来自已领取的原退款凭据；查询与创建始终使用相同交易、单号和分值。
type RefundRequest struct {
	RefundNo        string // 原退款单号，渠道幂等身份
	MerchantOrderNo string // 原商户支付号
	TransactionID   string // 原成功支付的渠道交易号
	TotalCents      int64  // 原支付总额，CNY 分
	RefundCents     int64  // 本次退款额，分，正数且不超过原总额
}

// RefundResult 是渠道已验证响应，资金用例仍须逐项与原请求比对后才能应用。
type RefundResult struct {
	RefundNo        string    // 渠道原退款号
	MerchantOrderNo string    // 渠道关联的原商户支付号
	TransactionID   string    // 渠道原支付交易号
	RefundID        string    // 渠道退款唯一身份，执行器要求非空且最多 64 字节
	Status          string    // SUCCESS、PROCESSING 或确认终态 CLOSED／ABNORMAL
	TotalCents      int64     // 渠道原支付总额，CNY 分
	RefundCents     int64     // 渠道本次退款额，分
	SuccessAt       time.Time // SUCCESS 必须有确认时刻，其他状态允许零值
}

// RefundProvider 只操作渠道原退款，不决定钱包、支付和业务订单状态。
type RefundProvider interface {
	// QueryRefund 在 context 下查询原 request；仅 ErrRefundNotFound 表示可创建，其他错误保留未知结果等待重试。
	QueryRefund(context.Context, RefundRequest) (RefundResult, error)
	// CreateRefund 在 context 下用 request 原退款号创建；返回回执或受理未知错误，禁止换号补发。
	CreateRefund(context.Context, RefundRequest) (RefundResult, error)
}

// QueryRefund 对 r 始终返回 ErrRefundNotFound，模拟实现不持有历史渠道记录；context 无外部 IO。
func (Simulator) QueryRefund(_ context.Context, r RefundRequest) (RefundResult, error) {
	return RefundResult{}, ErrRefundNotFound
}

// CreateRefund 校验 r 原退款身份与分值，返回同号的模拟 SUCCESS 回执或 ErrInvalidPayment。
// context 无外部 IO；模拟回执不能用于真实渠道支付，隔离由执行器核对。
func (Simulator) CreateRefund(_ context.Context, r RefundRequest) (RefundResult, error) {
	if r.RefundNo == "" || r.TransactionID == "" || r.TotalCents <= 0 || r.RefundCents <= 0 || r.RefundCents > r.TotalCents {
		return RefundResult{}, ErrInvalidPayment
	}
	return RefundResult{RefundNo: r.RefundNo, MerchantOrderNo: r.MerchantOrderNo, TransactionID: r.TransactionID, RefundID: "SIMRF" + r.RefundNo, Status: "SUCCESS", TotalCents: r.TotalCents, RefundCents: r.RefundCents, SuccessAt: time.Now().UTC()}, nil
}

// QueryRefund 在 ctx 下按 r.RefundNo 查询官方渠道，返回经字段校验的回执或 SDK 错误。
// RESOURCE_NOT_EXISTS 映射为 ErrRefundNotFound；其他错误不能推断渠道未受理，无业务状态写入。
func (w *WechatDirect) QueryRefund(ctx context.Context, r RefundRequest) (RefundResult, error) {
	result, _, err := w.refunds.QueryByOutRefundNo(ctx, refunddomestic.QueryByOutRefundNoRequest{OutRefundNo: new(r.RefundNo)})
	var apiErr *core.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "RESOURCE_NOT_EXISTS" {
		return RefundResult{}, ErrRefundNotFound
	}
	if err != nil {
		return RefundResult{}, err
	}
	return refundResult(result)
}

// CreateRefund 在 ctx 下用 r 原交易、退款号及 CNY 分值申请退款，返回完整回执或输入／SDK 错误。
// 调用前必须提交持久 claim，调用失败可能已受理，不在此改写资金或换退款号。
func (w *WechatDirect) CreateRefund(ctx context.Context, r RefundRequest) (RefundResult, error) {
	if r.RefundNo == "" || r.TransactionID == "" || r.TotalCents <= 0 || r.RefundCents <= 0 || r.RefundCents > r.TotalCents {
		return RefundResult{}, ErrInvalidPayment
	}
	result, _, err := w.refunds.Create(ctx, refunddomestic.CreateRequest{TransactionId: new(r.TransactionID), OutRefundNo: new(r.RefundNo), Amount: &refunddomestic.AmountReq{Total: new(r.TotalCents), Refund: new(r.RefundCents), Currency: new("CNY")}})
	if err != nil {
		return RefundResult{}, err
	}
	return refundResult(result)
}

// refundResult 将 r 官方响应转换为原业务回执；缺少身份／CNY 分值或成功时刻返回 ErrInvalidPayment。
// 不推导缺失字段，不执行 IO，成功结果仍需由执行器核对金额与原交易身份。
func refundResult(r *refunddomestic.Refund) (RefundResult, error) {
	if r == nil || r.OutRefundNo == nil || r.OutTradeNo == nil || r.TransactionId == nil || r.RefundId == nil || r.Status == nil || r.Amount == nil || r.Amount.Total == nil || r.Amount.Refund == nil || r.Amount.Currency == nil || *r.Amount.Currency != "CNY" {
		return RefundResult{}, ErrInvalidPayment
	}
	out := RefundResult{RefundNo: *r.OutRefundNo, MerchantOrderNo: *r.OutTradeNo, TransactionID: *r.TransactionId, RefundID: *r.RefundId, Status: string(*r.Status), TotalCents: *r.Amount.Total, RefundCents: *r.Amount.Refund}
	if r.SuccessTime != nil {
		out.SuccessAt = r.SuccessTime.UTC()
	}
	if out.Status == "SUCCESS" && out.SuccessAt.IsZero() {
		return RefundResult{}, ErrInvalidPayment
	}
	return out, nil
}
