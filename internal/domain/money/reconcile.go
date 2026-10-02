package money

// 对账差异比对。给定内部台账与渠道上报两份"参照号 → 金额（分）"，
// 逐笔比较并汇总差异。纯计算，不涉及存储，可独立测试。

// ReconcileDiff 是一条对不上的流水：同一个参照号在内部台账和渠道侧的金额以及差异原因。
type ReconcileDiff struct {
	Ref           string // 对账参照号（内部单号或渠道流水号），按它逐笔比对
	InternalCents int64  // 内部台账金额，单位分
	ChannelCents  int64  // 渠道上报金额，单位分
	Reason        string // 差异原因
}

// 差异原因的两类取值。差异原样交给运营判断，系统不做自动抹平。
const (
	ReasonAmountMismatch = "金额不一致"   // 两侧都有该参照号，金额不等
	ReasonMissingLocal   = "内部无对应记录" // 渠道有、内部台账查不到
)

// ReconcileResult 是一次对账的比对结果。
type ReconcileResult struct {
	Diffs     []ReconcileDiff
	DiffCount int
	// DiffCents 是差额合计，等于渠道减内部。
	DiffCents     int64
	InternalCount int
	ExternalCount int
	InternalCents int64
	ExternalCents int64
}

// DiffReconcile 逐笔比对内部台账与渠道上报，返回笔数、金额与差异明细。
//
// 两侧金额都按参照号累加过：同一参照号出现多次视为一笔合计。
// 内部有而渠道没有的参照号记为金额不一致（渠道侧为 0）；
// 渠道有而内部没有的记为内部无对应记录。比对只陈述事实，不推断原因、不自动补平。
func DiffReconcile(internal, channel map[string]int64) ReconcileResult {
	out := ReconcileResult{Diffs: []ReconcileDiff{}}
	seen := map[string]bool{}
	for ref, cents := range internal {
		seen[ref] = true
		if channel[ref] != cents {
			out.Diffs = append(out.Diffs, ReconcileDiff{Ref: ref, InternalCents: cents, ChannelCents: channel[ref], Reason: ReasonAmountMismatch})
			out.DiffCents += channel[ref] - cents
			out.DiffCount++
		}
	}
	for ref, cents := range channel {
		if !seen[ref] {
			out.Diffs = append(out.Diffs, ReconcileDiff{Ref: ref, InternalCents: 0, ChannelCents: cents, Reason: ReasonMissingLocal})
			out.DiffCents += cents
			out.DiffCount++
		}
	}
	out.InternalCount, out.ExternalCount = len(internal), len(channel)
	for _, cents := range internal {
		out.InternalCents += cents
	}
	for _, cents := range channel {
		out.ExternalCents += cents
	}
	return out
}

// IndexAmounts 把按行取出的"参照号 → 金额"汇总成 map，跳过空参照号，重复参照号累加。
// 各台账来源共用这一份口径，避免"同号多笔"在不同来源下处理不一致。
func IndexAmounts[T any](rows []T, pick func(int) (string, int64)) map[string]int64 {
	out := map[string]int64{}
	for i := range rows {
		ref, cents := pick(i)
		if ref != "" {
			out[ref] += cents
		}
	}
	return out
}
