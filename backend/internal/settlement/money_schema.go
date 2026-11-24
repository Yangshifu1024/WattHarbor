package settlement

import (
	"database/sql"
	"encoding/json"
	"time"
	"wattharbor/internal/domain/money"
)

// ChargeFeeRecord 保存订单结算回执，费用明细在计价引擎输出的 result_json 中。
type ChargeFeeRecord struct {
	ChargeOrderID  uint64 `gorm:"column:charge_order_id;primaryKey"` // 充电订单 ID，一单一份费用回执
	CalculationNo  string `gorm:"column:calculation_no"`             // 首次计费编号，重投递不重新生成
	ResultJSON     []byte `gorm:"column:result_json"`                // 完整计算结果 JSON，包含费用、价格和计量
	ShortfallCents int64  `gorm:"column:shortfall_cents"`            // 结算资金缺口，分，非负
}

// TableName 返回 ChargeFeeRecord 所属表名，不执行查询或修改。
func (ChargeFeeRecord) TableName() string { return "charge_fee_receipt" }

// Fees 解码回执中的电费、服务费和总额，单位均为分；ok=false 时金额不可使用。
func (r ChargeFeeRecord) Fees() (electric, service, total int64, ok bool) {
	// 回执存的是完整的计费 Result，
	// 其中费用字段就在顶层，与 source 块并列。
	var flat struct {
		ElectricCents int64 `json:"electric_cents"` // 回执确定电费，分
		ServiceCents  int64 `json:"service_cents"`  // 回执确定服务费，分
		TotalCents    int64 `json:"total_cents"`    // 回执确定总费，分
	}
	if err := json.Unmarshal(r.ResultJSON, &flat); err != nil {
		return 0, 0, 0, false
	}
	return flat.ElectricCents, flat.ServiceCents, flat.TotalCents, true
}

// RefundRecord 保存支付原路退款的单号、金额（分）、执行方式和状态，审核通过不代表渠道完成。
type RefundRecord struct {
	ExecutionPolicy money.ExecutionPolicy `gorm:"column:execution_policy;default:manual_review"`       // automatic 可执行；manual_review 须人工双签。tag 内的 default 字面量与 money.ExecutionManualReview 同值
	NextAttemptAt   time.Time             `gorm:"column:next_attempt_at;default:CURRENT_TIMESTAMP(3)"` // 退款下次可执行 UTC 时刻，未知结果保持待重试
	RetryCount      uint32                `gorm:"column:retry_count"`                                  // 执行尝试次数，沿用首次记录累加
	ID              uint64                `gorm:"column:id;primaryKey"`                                // 退款主键，与 UTC 月分区共同标识记录
	RefundNo        string                `gorm:"column:refund_no"`                                    // 退款单号，渠道重试和幂等沿用
	PaymentOrderID  uint64                `gorm:"column:payment_order_id"`                             // 支付主键，创建或执行时锁定额度
	UserID          uint64                `gorm:"column:user_id"`                                      // 支付所属用户
	BizType         money.PaymentBizType  `gorm:"column:biz_type"`                                     // charge 或 wallet_recharge，决定资金及订单联动
	BizID           uint64                `gorm:"column:biz_id"`                                       // 业务 ID，迟到支付尚未建单时为 0
	RefundCents     int64                 `gorm:"column:refund_cents"`                                 // 本笔原路退款金额，分，须大于 0
	Reason          sql.NullString        `gorm:"column:reason"`                                       // 退款业务依据，可空
	Status          money.RefundStatus    `gorm:"column:status"`                                       // pending 待执行 / processing 渠道处理中 / success 已退 / failed 可重试 / rejected 复核否决
	CreatedMonth    time.Time             `gorm:"column:created_month;primaryKey"`                     // 首次申请的 UTC 月首，重试沿用首次分区
}

// TableName 返回 RefundRecord 所属表名，不执行查询或修改。
func (RefundRecord) TableName() string { return "refund_record" }
