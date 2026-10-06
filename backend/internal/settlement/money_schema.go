package settlement

import (
	"database/sql"
	"encoding/json"
	"time"
)

// ChargeFeeRecord 保存订单结算回执，费用明细在计价引擎输出的 result_json 中。
type ChargeFeeRecord struct {
	ChargeOrderID  uint64 `gorm:"column:charge_order_id;primaryKey"` // 原充电订单 ID，一单一份费用回执
	CalculationNo  string `gorm:"column:calculation_no"`             // 原计费编号，重投递不重新生成
	ResultJSON     []byte `gorm:"column:result_json"`                // 完整已计算费用与原计量价格证据 JSON
	ShortfallCents int64  `gorm:"column:shortfall_cents"`            // 结算资金缺口，分，非负
}

// TableName 返回 ChargeFeeRecord 所属表名，不执行查询或修改。
func (ChargeFeeRecord) TableName() string { return "charge_fee_receipt" }

// Fees 将接收者原 ResultJSON 解码为 electric 电费、service 服务费及 total 总费，单位均为分。
// ok=false 表示 JSON 无法解码，此时金额不可使用；不重新计算或写入，无 IO。
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

// RefundRecord 保存原路退款身份、分值、执行策略及恢复状态，不以审批代替渠道完成。
type RefundRecord struct {
	ExecutionPolicy string         `gorm:"column:execution_policy;default:manual_review"`       // automatic 可执行；manual_review 须人工双签
	NextAttemptAt   time.Time      `gorm:"column:next_attempt_at;default:CURRENT_TIMESTAMP(3)"` // 原退款下次可执行 UTC 时刻，未知结果保持待重试
	RetryCount      uint32         `gorm:"column:retry_count"`                                  // 执行尝试次数，沿用原记录累加
	ID              uint64         `gorm:"column:id;primaryKey"`                                // 原退款主键，与 UTC 月分区共同标识记录
	RefundNo        string         `gorm:"column:refund_no"`                                    // 原退款单号，渠道重试和幂等沿用
	PaymentOrderID  uint64         `gorm:"column:payment_order_id"`                             // 原支付主键，创建或执行时锁定额度
	UserID          uint64         `gorm:"column:user_id"`                                      // 原支付所属用户
	BizType         string         `gorm:"column:biz_type"`                                     // charge 或 wallet_recharge，决定资金及订单联动
	BizID           uint64         `gorm:"column:biz_id"`                                       // 原业务 ID，迟到支付尚未建单时为 0
	RefundCents     int64          `gorm:"column:refund_cents"`                                 // 本笔原路退款金额，分，须大于 0
	Reason          sql.NullString `gorm:"column:reason"`                                       // 退款业务依据，可空
	Status          string         `gorm:"column:status"`                                       // pending、processing、success、failed 或 needs_review 等执行状态
	CreatedMonth    time.Time      `gorm:"column:created_month;primaryKey"`                     // 首次申请的 UTC 月首，重试沿用原分区
}

// TableName 返回 RefundRecord 所属表名，不执行查询或修改。
func (RefundRecord) TableName() string { return "refund_record" }
