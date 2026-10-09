package payment

import "errors"

// ErrPaymentIntentConflict 表示支付意图与另一请求或活跃端口占用冲突。
// 申请内容不一致、已失效或端口已占用时均返回此错误。
var ErrPaymentIntentConflict = errors.New("payment intent conflicts with another request or active port")
