package money

import "time"

// RefundDraft 是同库事务内的退款业务输入，不携带数据库模型或渠道完成声明。
type RefundDraft struct {
	RefundNo       string         // 退款单号，重试不得重新生成
	PaymentOrderID uint64         // 支付主键，调用方已持有支付行锁
	UserID         uint64         // 支付所属用户
	BizType        PaymentBizType // charge 或 wallet_recharge
	BizID          uint64         // 业务编号；迟到支付尚未建单时为 0
	AmountCents    int64          // 本笔待退金额，单位分，必须大于 0
	Reason         string         // 持久化退款原因，不含凭据或敏感数据
	CreatedMonth   time.Time      // 首次申请月份的 UTC 月首；重放沿用月份
	NextAttemptAt  time.Time      // 首次可执行的 UTC 时刻；零值使用数据库默认值
}
