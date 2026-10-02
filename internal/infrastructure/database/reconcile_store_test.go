package database

import (
	"encoding/json"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
)

// 落库的差异明细必须能被读回原值。领域类型不带 JSON 标签，若直接序列化
// money.ReconcileDiff，存的是 ChannelCents，而回读按 channel_cents 匹配，
// 解码不报错但金额全变 0——运营看到的每条差异金额都是零。
func TestReconcileDiffsSurviveStoreRoundTrip(t *testing.T) {
	source := money.ReconcileResult{
		Diffs: []money.ReconcileDiff{
			{Ref: "CHAN-0001", InternalCents: 0, ChannelCents: 1700, Reason: money.ReasonMissingLocal},
			{Ref: "ORD-0002", InternalCents: 900, ChannelCents: 900, Reason: "金额不一致"},
		},
	}
	encoded, err := json.Marshal(ToReconcileDiffs(source.Diffs))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []ReconcileDiff
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Fatalf("want 2 diffs, got %d", len(decoded))
	}
	for i, want := range source.Diffs {
		got := decoded[i]
		if got.Ref != want.Ref || got.InternalCents != want.InternalCents || got.ChannelCents != want.ChannelCents || got.Reason != want.Reason {
			t.Fatalf("diff %d round trip mismatch: got %+v, want %+v", i, got, want)
		}
	}
}

// 直接序列化领域类型会写出 Go 字段名，与读回用的键名不一致。
// 这条用例把该前提固定下来：一旦有人给领域类型加了标签或改用别处编码，
// 这里的假设就该跟着更新，而不是等到对账页金额全零才发现。
func TestReconcileDiffWireKeysUseDatabaseShape(t *testing.T) {
	encoded, err := json.Marshal(ToReconcileDiffs([]money.ReconcileDiff{{Ref: "R1", ChannelCents: 7}}))
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("want 1 diff, got %d", len(raw))
	}
	for _, key := range []string{"ref", "internal_cents", "channel_cents", "reason"} {
		if _, ok := raw[0][key]; !ok {
			t.Fatalf("wire key %q missing in %s", key, encoded)
		}
	}
	if _, ok := raw[0]["ChannelCents"]; ok {
		t.Fatalf("go field name leaked into stored json: %s", encoded)
	}
}

func TestToReconcileDiffsKeepsEmptyAsEmpty(t *testing.T) {
	if got := ToReconcileDiffs(nil); got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", got)
	}
}
