package money

import "testing"

func TestDiffReconcileReportsBothDirections(t *testing.T) {
	internal := map[string]int64{"A": 1000, "B": 2000, "C": 3000}
	channel := map[string]int64{"A": 1000, "B": 2500, "D": 700}
	got := DiffReconcile(internal, channel)

	if got.InternalCount != 3 || got.ExternalCount != 3 {
		t.Fatalf("笔数 %d/%d", got.InternalCount, got.ExternalCount)
	}
	if got.InternalCents != 6000 || got.ExternalCents != 4200 {
		t.Fatalf("金额 %d/%d", got.InternalCents, got.ExternalCents)
	}
	byRef := map[string]ReconcileDiff{}
	for _, d := range got.Diffs {
		byRef[d.Ref] = d
	}
	// 金额不一致：渠道比内部多 500，差额应为 +500
	if d := byRef["B"]; d.Reason != ReasonAmountMismatch || d.InternalCents != 2000 || d.ChannelCents != 2500 {
		t.Fatalf("B: %+v", d)
	}
	// 渠道有、内部没有：全额计入差额
	if d := byRef["D"]; d.Reason != ReasonMissingLocal || d.InternalCents != 0 || d.ChannelCents != 700 {
		t.Fatalf("D: %+v", d)
	}
	// 内部有、渠道没有：渠道侧记 0，差额为负
	if d := byRef["C"]; d.Reason != ReasonAmountMismatch || d.ChannelCents != 0 {
		t.Fatalf("C: %+v", d)
	}
	if got.DiffCount != 3 {
		t.Fatalf("差异笔数 %d", got.DiffCount)
	}
	// +500（B） -3000（C） +700（D） = -1800
	if got.DiffCents != -1800 {
		t.Fatalf("差额 %d", got.DiffCents)
	}
}

func TestDiffReconcileExactMatchHasNoDiffs(t *testing.T) {
	ledger := map[string]int64{"A": 1000, "B": 2000}
	got := DiffReconcile(ledger, map[string]int64{"A": 1000, "B": 2000})
	if got.DiffCount != 0 || got.DiffCents != 0 || len(got.Diffs) != 0 {
		t.Fatalf("完全一致却报出差异：%+v", got)
	}
	if got.InternalCents != got.ExternalCents {
		t.Fatalf("合计不等 %d vs %d", got.InternalCents, got.ExternalCents)
	}
}

func TestDiffReconcileEmptySidesAreNotFalsePositives(t *testing.T) {
	// 渠道无对应记录时，内部有记录才算差异；内部为空时不应凭 0 金额报差异。
	if got := DiffReconcile(nil, map[string]int64{}); got.DiffCount != 0 {
		t.Fatalf("两侧皆空却报差异：%+v", got)
	}
	// 渠道侧金额为 0 且内部无此单：属于"内部无对应记录"，但金额差为 0
	got := DiffReconcile(map[string]int64{}, map[string]int64{"Z": 0})
	if len(got.Diffs) != 1 || got.Diffs[0].Reason != ReasonMissingLocal {
		t.Fatalf("零金额单未被识别为缺失：%+v", got)
	}
}

func TestIndexAmountsAccumulatesAndSkipsEmptyRefs(t *testing.T) {
	rows := []struct {
		ref   string
		cents int64
	}{{"A", 100}, {"A", 250}, {"", 999}, {"B", 50}}
	got := IndexAmounts(rows, func(i int) (string, int64) { return rows[i].ref, rows[i].cents })
	if len(got) != 2 {
		t.Fatalf("参照号数 %d: %v", len(got), got)
	}
	if got["A"] != 350 || got["B"] != 50 {
		t.Fatalf("累加结果 %v", got)
	}
	if _, present := got[""]; present {
		t.Fatal("空参照号不应进入结果")
	}
}
