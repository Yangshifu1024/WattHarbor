package query

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/order"
	"chargepilot/internal/protocol"
)

// liveFixture 同时提供可计数读取和故障注入，仅证明组合规则，不替代真实网关／数据库。
type liveFixture struct {
	evidenceCalls int                                       // 已发起证据批数
	snapshotCalls int                                       // 已发起快照批数
	purchaseCalls int                                       // 已发起卡购买批数
	sizes         []int                                     // 原请求每批项数
	raw           []byte                                    // 原冻结套餐 JSON
	cards         map[uint64]charging.OrderCardPurchase     // 原卡累计购买
	fault         string                                    // 指定存储／证据故障阶段
	cancel        context.CancelFunc                        // 首批返回后取消的受管理测试上下文
	mutate        func([]gatewaycontract.EvidencePageReply) // 指定端口、完整性与缺失响应变异
}

// ChargingEvidence 为 ctx 下 q 生成原身份的60秒心跳，返回页或指定故障，并记录调用次数。
func (f *liveFixture) ChargingEvidence(ctx context.Context, q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
	f.evidenceCalls++
	f.sizes = append(f.sizes, len(q))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if f.fault == "first_batch" && f.evidenceCalls == 1 {
		return nil, errors.New("fixture evidence failure")
	}
	out := make([]gatewaycontract.EvidencePageReply, len(q))
	for i, v := range q {
		out[i] = gatewaycontract.EvidencePageReply{DeviceID: v.DeviceID, PortNo: v.PortNo, Complete: true, Samples: []gatewaycontract.EvidenceSample{{Type: protocol.Heartbeat, DeviceID: v.DeviceID, ReceivedAt: v.StartedAt.Add(time.Minute), ChargingPorts: []protocol.PortTelemetry{{Port: v.PortNo, ChargedMWh: 4000, ChargedSeconds: 60, PowerDeciWatts: 1500}}}}}
	}
	if f.mutate != nil {
		f.mutate(out)
	}
	if f.cancel != nil {
		f.cancel()
	}
	return out, nil
}

// Snapshots 为 ctx 下 ids 返回原冻结 JSON，或指定存储错误；记录批数，无真实写入。
func (f *liveFixture) Snapshots(ctx context.Context, ids []uint64) (map[uint64][]byte, error) {
	f.snapshotCalls++
	if f.fault == "snapshot" {
		return nil, errors.New("fixture snapshot failure")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := map[uint64][]byte{}
	for _, id := range ids {
		out[id] = f.raw
	}
	return out, nil
}

// Purchases 在 ctx 下返回 f 的原累计购买，或指定存储错误；记录批数，无真实扣款。
func (f *liveFixture) Purchases(ctx context.Context, _ []uint64) (map[uint64]charging.OrderCardPurchase, error) {
	f.purchaseCalls++
	if f.fault == "purchase" {
		return nil, errors.New("fixture purchase failure")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return f.cards, nil
}

// liveRows 为 n 构造相同开始时刻、不同原身份的 charging 读模型，无 IO。
func liveRows(n int, start time.Time) []order.OrderView {
	rows := make([]order.OrderView, n)
	for i := range rows {
		rows[i] = order.OrderView{OrderID: uint64(i + 1), DeviceID: "live-proof", PortNo: 1, Status: "charging", StartedAt: &start}
	}
	return rows
}

// durationSnapshot 为 t 生成600分／60分钟原冻结套餐；失败终止测试，无持久化。
func durationSnapshot(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"rule": charging.Rule{Spec: charging.Spec{Mode: charging.ModeDeviceDuration}}, "offer": charging.Offer{ID: 1, StationID: 1, Name: "frozen", Mode: "duration", PriceCents: 600, DurationMinutes: 60}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestLiveBatchKeepsObservedFeesAndBoundsCalls 验证21单仅两批外部调用和各一次所属读取，断线只计已观测60秒。
func TestLiveBatchKeepsObservedFeesAndBoundsCalls(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows := liveRows(21, start)
	f := &liveFixture{raw: durationSnapshot(t), cards: map[uint64]charging.OrderCardPurchase{1: {PaidCents: 1200, PurchasedMinutes: 120, MaxMinutes: 300}}}
	s := LiveService{Gateway: f, Orders: f, Cards: f, Now: func() time.Time { return start.Add(time.Hour) }}
	s.Fill(context.Background(), rows)
	if f.evidenceCalls != 2 || f.sizes[0] != 20 || f.sizes[1] != 1 || f.snapshotCalls != 1 || f.purchaseCalls != 1 {
		t.Fatalf("calls=%+v", f)
	}
	for i, row := range rows {
		if row.Live == nil || !row.Live.Stale || row.Live.Seconds != 60 || row.Live.KWh != 0.004 || row.Live.Fee == nil || row.Live.Fee.TotalCents != 10 || row.LiveUnavailable != "" {
			t.Fatalf("row %d=%+v", i, row.Live)
		}
	}
	rows[0].Status = "completed"
	rows[0].Live = nil
	rows[1].StartedAt = nil
	rows[1].Live = nil
	s.Fill(context.Background(), rows[:2])
	if f.evidenceCalls != 2 || rows[0].Live != nil || rows[1].Live != nil {
		t.Fatal("inactive rows triggered evidence")
	}
}

// TestLiveMissingFactsNeverBecomeConfirmedZero 验证缺口、错身份、无快照、卡零购买及存储故障保留原缺失语义。
func TestLiveMissingFactsNeverBecomeConfirmedZero(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string             // 用例身份
		configure   func(*liveFixture) // 对指定响应事实或故障的修改
		wantView    bool               // 是否应保留已观测计量
		wantFee     string             // 原费用不可估算文案
		unavailable bool               // 是否应标记整体读取失败
	}{
		{"incomplete", func(f *liveFixture) {
			f.mutate = func(p []gatewaycontract.EvidencePageReply) { p[0].Complete = false; p[0].NextAfterID = 9 }
		}, true, "计量证据超过范围，费用待结算", false},
		{"cursor_missing", func(f *liveFixture) { f.mutate = func(p []gatewaycontract.EvidencePageReply) { p[0].Complete = false } }, false, "", true},
		{"wrong_port", func(f *liveFixture) { f.mutate = func(p []gatewaycontract.EvidencePageReply) { p[0].PortNo = 2 } }, false, "", true},
		{"nil_samples", func(f *liveFixture) { f.mutate = func(p []gatewaycontract.EvidencePageReply) { p[0].Samples = nil } }, false, "", true},
		{"missing_snapshot", func(f *liveFixture) { f.raw = nil }, true, "缺少订单计费快照", false},
		{"zero_card_purchase", func(f *liveFixture) { f.cards = map[uint64]charging.OrderCardPurchase{1: {}} }, true, "刷卡购买记录待确认", false},
		{"snapshot_fault", func(f *liveFixture) { f.fault = "snapshot" }, true, "", true},
		{"purchase_fault", func(f *liveFixture) { f.fault = "purchase" }, true, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &liveFixture{raw: durationSnapshot(t)}
			tc.configure(f)
			rows := liveRows(1, start)
			LiveService{Gateway: f, Orders: f, Cards: f, Now: func() time.Time { return start.Add(time.Minute) }}.Fill(context.Background(), rows)
			row := rows[0]
			if (row.Live != nil) != tc.wantView || (row.LiveUnavailable != "") != tc.unavailable {
				t.Fatalf("row=%+v", row)
			}
			if row.Live != nil && (row.Live.Fee != nil || row.Live.FeeUnavailable != tc.wantFee) {
				t.Fatalf("view=%+v", row.Live)
			}
		})
	}
}

// TestLiveBatchFailureAndCancellationPreserveIndependentRecovery 验证一批失败不污染下一批，取消不启动下一批或所属存储读取。
func TestLiveBatchFailureAndCancellationPreserveIndependentRecovery(t *testing.T) {
	start := time.Now().UTC().Add(-time.Minute)
	rows := liveRows(21, start)
	f := &liveFixture{raw: durationSnapshot(t), fault: "first_batch"}
	s := LiveService{Gateway: f, Orders: f, Cards: f}
	s.Fill(context.Background(), rows)
	if f.evidenceCalls != 2 || rows[0].Live != nil || rows[20].Live == nil || rows[20].Live.Fee == nil {
		t.Fatal("independent batch did not recover")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f = &liveFixture{raw: durationSnapshot(t), cancel: cancel}
	s = LiveService{Gateway: f, Orders: f, Cards: f}
	rows = liveRows(21, start)
	s.Fill(ctx, rows)
	if f.evidenceCalls != 1 || f.snapshotCalls != 0 || f.purchaseCalls != 0 || rows[20].Live != nil || rows[0].LiveUnavailable == "" {
		t.Fatal("cancellation started more work or hid failure")
	}
}
