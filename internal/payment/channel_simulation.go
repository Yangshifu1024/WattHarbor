package payment

import (
	"context"
	"regexp"
)

// Simulator 仅在 PAYMENT_MODE=simulation 时使用，返回模拟支付参数。
type Simulator struct{}

// simulationOrderNumber 同时接受当前 P 雪花编号与历史 PAY 编号，拒绝任意客户端生成的其他支付身份。
var simulationOrderNumber = regexp.MustCompile(`^(P[1-9][0-9]{4,18}|PAY[A-Za-z0-9]+)$`)

// Prepay 为 request 的原单返回稳定模拟参数；微信身份、分值或单号无效时返回 ErrInvalidPayment。
// context 不参与 IO，因为模拟实现无外部调用或持久状态；模拟参数不代表到账。
func (Simulator) Prepay(_ context.Context, request PrepayRequest) (PrepayParams, error) {
	if request.OpenID == "" || request.AmountCents <= 0 || !simulationOrderNumber.MatchString(request.MerchantOrderNo) {
		return PrepayParams{}, ErrInvalidPayment
	}
	return PrepayParams{Provider: "simulation", PrepayID: "SIM" + request.MerchantOrderNo}, nil
}
