package database

import (
	"context"
	"encoding/json"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"gorm.io/gorm"
)

// 对账记录的落库形态与写入。逐笔比对规则在 domain/money.DiffReconcile，
// 这里只负责取内部台账、比对着色后的结果并落库。

type ReconcileRow struct {
	ID            uint64 `gorm:"column:id" json:"id"`                         // 对账记录主键 ID
	ReconcileType string `gorm:"column:reconcile_type" json:"reconcile_type"` // 对账类型:wechat_refund 微信退款 / wechat_pay 微信支付 / split 分账 / withdraw 提现
	ReconcileDate string `gorm:"column:reconcile_date" json:"reconcile_date"` // 对账日期,格式 YYYY-MM-DD;同一天同类型只保留一条,重跑覆盖
	InternalCount int    `gorm:"column:internal_count" json:"internal_count"` // 内部台账笔数
	ExternalCount int    `gorm:"column:wechat_count" json:"wechat_count"`     // 渠道上报笔数(库字段 wechat_count)
	DiffCount     int    `gorm:"column:diff_count" json:"diff_count"`         // 对不上的笔数
	InternalCents int64  `gorm:"column:internal_cents" json:"internal_cents"` // 内部台账金额合计,单位分
	ExternalCents int64  `gorm:"column:wechat_cents" json:"wechat_cents"`     // 渠道上报金额合计,单位分(库字段 wechat_cents)
	DiffCents     int64  `gorm:"column:diff_cents" json:"diff_cents"`         // 差额合计,单位分,等于渠道减内部
	Resolved      bool   `gorm:"column:resolved" json:"resolved"`             // 运营是否已标记为处理完毕
	CreatedAt     string `gorm:"column:created_at" json:"created_at"`         // 记录写入时间,UTC
	// Diffs 在查询后由 JSON 文本解码，避免直接将 JSON 列扫描到 any 字段。
	Diffs []ReconcileDiff `json:"diffs" gorm:"-"` // 逐条差异明细,查询后从 diffs_json 解析回填
}

// ReconcileDiff 是一条对不上的流水：同一个参照号在内部台账和渠道侧的金额以及差异原因。
type ReconcileDiff struct {
	Ref           string `json:"ref"`            // 对账参照号(内部单号或渠道流水号),按它逐笔比对
	InternalCents int64  `json:"internal_cents"` // 内部台账金额,单位分
	ChannelCents  int64  `json:"channel_cents"`  // 渠道上报金额,单位分
	Reason        string `json:"reason"`         // 差异原因:金额不一致 / 内部无对应记录
}

// ToReconcileDiffs 把领域层的差异明细转成落库与返回共用的形态。
//
// 领域类型刻意不带 JSON 标签：序列化形状由持久化层决定。两边字段名不同
// （领域是 ChannelCents，库侧是 channel_cents）时，json 解码不会报错但会
// 静默丢值——带下划线的键匹配不上，全额变成 0。所以落库前必须显式转换。
func ToReconcileDiffs(diffs []money.ReconcileDiff) []ReconcileDiff {
	out := make([]ReconcileDiff, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, ReconcileDiff{Ref: d.Ref, InternalCents: d.InternalCents, ChannelCents: d.ChannelCents, Reason: d.Reason})
	}
	return out
}

// SaveReconcileResult 按 (对账类型, 对账日期) 唯一键写入对账结果，同日重跑覆盖原结果并
// 把 resolved 清回 0——重跑意味着原结论作废，是否已处理需要运营重新判断。
// 差异明细以 JSON 文本落库，返回时再解回结构，避免 JSON 列被扫描成 any。
func SaveReconcileResult(ctx context.Context, db *gorm.DB, kind, date string, result money.ReconcileResult) error {
	diffs, err := json.Marshal(ToReconcileDiffs(result.Diffs))
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Exec(
		`INSERT INTO finance_reconcile_log(reconcile_type,reconcile_date,internal_count,wechat_count,diff_count,internal_cents,wechat_cents,diff_cents,diffs_json,resolved)
		 VALUES(?,?,?,?,?,?,?,?,?,0)
		 ON DUPLICATE KEY UPDATE internal_count=VALUES(internal_count),wechat_count=VALUES(wechat_count),diff_count=VALUES(diff_count),
		   internal_cents=VALUES(internal_cents),wechat_cents=VALUES(wechat_cents),diff_cents=VALUES(diff_cents),diffs_json=VALUES(diffs_json),resolved=0`,
		kind, date, result.InternalCount, result.ExternalCount, result.DiffCount,
		result.InternalCents, result.ExternalCents, result.DiffCents, string(diffs)).Error
}
