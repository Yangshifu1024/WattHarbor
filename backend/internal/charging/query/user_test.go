package query

import (
	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/order"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// userQueryFixture 提供归属与取消闸门，不冒充真实网关或存储验收。
type userQueryFixture struct {
	row            order.ChargeOrderRecord         // 本用户信息
	ownerError     error                           // 所属读取故障
	frozen         FrozenState                     // 冻结查询结果
	fees           map[uint64]order.FeeFacts       // 确认费用
	funds          DetailFunds                     // 资金查询结果
	stateError     error                           // 冻结故障
	feeError       error                           // 回执故障
	fundsError     error                           // 支付、退款故障
	gatewayError   error                           // 网络故障
	telemetry      contract.TelemetryReply         // 观测
	history        contract.HistoricalCurveReply   // 聚合
	reads          int                             // 授权后组合调用计数
	telemetryCalls int                             // 遥测调用计数
	historyCalls   int                             // 历史调用计数
	lastTelemetry  contract.TelemetryRequest       // 实际规范遥测窗口
	lastHistory    contract.HistoricalCurveRequest // 实际规范历史窗口
}

// Resolve 检查 uid/no 与 f 用户及单号，返回信息或取消、测试替身校验错误。
func (f *userQueryFixture) Resolve(ctx context.Context, uid uint64, no string) (order.ChargeOrderRecord, error) {
	if err := ctx.Err(); err != nil {
		return f.row, err
	}
	if uid != f.row.UserID || no != f.row.OrderNo {
		return order.ChargeOrderRecord{}, errors.New("owner denied")
	}
	return f.row, f.ownerError
}

// Frozen 记录一次已授权 row 的读取，返回 f 冻结模型或注入错误。
func (f *userQueryFixture) Frozen(ctx context.Context, row order.ChargeOrderRecord) (FrozenState, error) {
	f.reads++
	return f.frozen, f.stateError
}

// Funds 记录一次已授权 row 的读取，返回 f 资金模型或注入错误。
func (f *userQueryFixture) Funds(ctx context.Context, row order.ChargeOrderRecord) (DetailFunds, error) {
	f.reads++
	return f.funds, f.fundsError
}

// Fees 记录 ids 首次回执的读取，返回 f 确认值或注入错误。
func (f *userQueryFixture) Fees(ctx context.Context, ids []uint64) (map[uint64]order.FeeFacts, error) {
	f.reads++
	return f.fees, f.feeError
}

// Telemetry 记录 q 窗口，返回 f 观测或取消、注入错误，不启动后台任务。
func (f *userQueryFixture) Telemetry(ctx context.Context, q contract.TelemetryRequest) (contract.TelemetryReply, error) {
	f.telemetryCalls++
	f.lastTelemetry = q
	if err := ctx.Err(); err != nil {
		return f.telemetry, err
	}
	return f.telemetry, f.gatewayError
}

// HistoricalCurve 记录 q 窗口，返回 f 聚合或取消、注入错误，不启动后台任务。
func (f *userQueryFixture) HistoricalCurve(ctx context.Context, q contract.HistoricalCurveRequest) (contract.HistoricalCurveReply, error) {
	f.historyCalls++
	f.lastHistory = q
	if err := ctx.Err(); err != nil {
		return f.history, err
	}
	return f.history, f.gatewayError
}

// userQueryService 创建固定时刻与标识 测试替身，返回可注入故障源和业务服务，不执行外部 I/O。
func userQueryService() (*userQueryFixture, UserService, time.Time) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	f := &userQueryFixture{row: order.ChargeOrderRecord{ID: 19, UserID: 7, OrderNo: "original", DeviceID: "board", PortNo: 2, Status: "charging", StartedAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}}, fees: map[uint64]order.FeeFacts{19: {OrderID: 19}}, telemetry: contract.TelemetryReply{Series: []contract.CurvePoint{}}, history: contract.HistoricalCurveReply{Series: []contract.CurvePoint{}, Summary: map[string]json.RawMessage{}}}
	return f, UserService{Orders: f, Fees: f, State: f, Gateway: f, Now: func() time.Time { return now }}, now
}

// TestUserQueryOwnershipPrecedesAllSupplementalReads 验证所有组合入口先归属、取消不启动补充 I/O。
func TestUserQueryOwnershipPrecedesAllSupplementalReads(t *testing.T) {
	f, s, _ := userQueryService()
	live := &liveFixture{raw: durationSnapshot(t)}
	s.Live = LiveService{Gateway: live, Orders: live, Cards: live}
	for _, call := range []func(context.Context, uint64) error{
		func(ctx context.Context, uid uint64) error { _, err := s.Detail(ctx, uid, "original"); return err },
		func(ctx context.Context, uid uint64) error { _, err := s.Snapshot(ctx, uid, "original"); return err },
		func(ctx context.Context, uid uint64) error {
			_, err := s.Curve(ctx, uid, "original", "last_24h")
			return err
		},
		func(ctx context.Context, uid uint64) error {
			_, err := s.HistoricalCurve(ctx, uid, "original", "15min")
			return err
		},
	} {
		if err := call(context.Background(), 8); err == nil {
			t.Fatal("other user reached query")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := call(ctx, 7); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
	}
	if f.reads != 0 || f.telemetryCalls != 0 || f.historyCalls != 0 || live.evidenceCalls != 0 {
		t.Fatal("denied/cancelled request started supplemental reads")
	}
}

// TestUserSnapshotSharesAdminEstimateWithoutConfirmingFees 验证同一观测及冻结套餐得到后台相同预估，且不产生确认费用。
func TestUserSnapshotSharesAdminEstimateWithoutConfirmingFees(t *testing.T) {
	f, s, now := userQueryService()
	f.fees = map[uint64]order.FeeFacts{}
	f.row.StartedAt.Time = now.Add(-time.Minute)
	live := &liveFixture{raw: durationSnapshot(t)}
	service := LiveService{Gateway: live, Orders: live, Cards: live, Now: s.Now}
	s.Live = service
	out, err := s.Snapshot(t.Context(), 7, "original")
	if err != nil || out.Live == nil || out.Live.Fee == nil || out.Live.Fee.TotalCents != 10 || out.TotalCents != nil || out.ElectricCents != nil || out.ServiceCents != nil {
		t.Fatalf("snapshot=%+v err=%v", out, err)
	}
	admin := []order.OrderView{{OrderID: f.row.ID, DeviceID: f.row.DeviceID, PortNo: f.row.PortNo, Status: f.row.Status, StartedAt: out.StartedAt}}
	service.Fill(t.Context(), admin)
	if admin[0].Live == nil || admin[0].Live.Fee == nil || *admin[0].Live.Fee != *out.Live.Fee || out.Live.Stale {
		t.Fatal("user estimate differs from admin or incorrectly stale")
	}
	f.fees[f.row.ID] = order.FeeFacts{OrderID: f.row.ID}
	out, err = s.Snapshot(t.Context(), 7, "original")
	if err != nil || out.TotalCents == nil || *out.TotalCents != 0 || out.Live.Fee.TotalCents != 10 {
		t.Fatal("estimate overwrote confirmed zero")
	}
	for _, status := range []string{"paid", "completed"} {
		f.row.Status = status
		before := live.evidenceCalls
		out, err = s.Snapshot(t.Context(), 7, "original")
		if err != nil || out.Live != nil || live.evidenceCalls != before {
			t.Fatal("noncharging snapshot estimated fees")
		}
	}
}

// TestUserSnapshotEstimateFailurePreservesTelemetry 验证估价取证失败仍展示遥测、不完整证据不输出费用，陈旧量明确标记。
func TestUserSnapshotEstimateFailurePreservesTelemetry(t *testing.T) {
	for _, fault := range []string{"first_batch", "snapshot", "incomplete", "stale"} {
		t.Run(fault, func(t *testing.T) {
			f, s, now := userQueryService()
			f.fees = map[uint64]order.FeeFacts{}
			power := float64(6000)
			f.telemetry.Series = []contract.CurvePoint{{TS: now.Format(time.RFC3339), PowerW: &power}}
			live := &liveFixture{raw: durationSnapshot(t), fault: fault}
			if fault == "incomplete" {
				live.mutate = func(p []contract.EvidencePageReply) { p[0].Complete = false; p[0].NextAfterID = 1 }
			}
			s.Live = LiveService{Gateway: live, Orders: live, Cards: live, Now: s.Now}
			out, err := s.Snapshot(t.Context(), 7, "original")
			if err != nil || out.TotalCents != nil || out.CurrentPowerW == nil || *out.CurrentPowerW != power {
				t.Fatalf("estimate failure hid telemetry: %+v %v", out, err)
			}
			if fault == "stale" {
				if out.Live == nil || !out.Live.Stale || out.Live.Fee == nil {
					t.Fatal("old observed estimate lost stale flag")
				}
			} else if out.LiveUnavailable == "" && (out.Live == nil || out.Live.Fee != nil || out.Live.FeeUnavailable == "") {
				t.Fatal("estimate failure produced a readable fee")
			}
		})
	}
}

// TestUserSnapshotEstimateCancellationStopsTelemetry 验证估价阶段请求取消不继续遥测读取。
func TestUserSnapshotEstimateCancellationStopsTelemetry(t *testing.T) {
	f, s, _ := userQueryService()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	live := &liveFixture{raw: durationSnapshot(t), cancel: cancel}
	s.Live = LiveService{Gateway: live, Orders: live, Cards: live}
	if _, err := s.Snapshot(ctx, 7, "original"); !errors.Is(err, context.Canceled) || f.telemetryCalls != 0 {
		t.Fatalf("cancelled estimate continued reads: %v", err)
	}
}

// TestUserSnapshotKeepsConfirmedZeroAndOptionalObservations 验证确认零、未上报空值、有限窗口与网关降级。
func TestUserSnapshotKeepsConfirmedZeroAndOptionalObservations(t *testing.T) {
	f, s, now := userQueryService()
	zero := float64(0)
	meter := .25
	f.telemetry.Series = []contract.CurvePoint{{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), PowerW: &meter}, {TS: now.Add(-time.Minute).Format(time.RFC3339), PowerW: &zero, MeterKWh: &meter}, {TS: now.Format(time.RFC3339), VoltageV: &zero}, {TS: now.Add(time.Minute).Format(time.RFC3339), PowerW: &meter}}
	out, err := s.Snapshot(context.Background(), 7, "original")
	if err != nil {
		t.Fatal(err)
	}
	if out.TotalCents == nil || *out.TotalCents != 0 || out.ChargedKWh != meter || out.CurrentPowerW == nil || *out.CurrentPowerW != 0 || out.TemperatureC != nil || out.TelemetryAvailable == nil || !*out.TelemetryAvailable || out.ElapsedSeconds == nil || *out.ElapsedSeconds != 3600 || out.TelemetryAt == nil || !out.TelemetryAt.Equal(now) {
		t.Fatalf("snapshot=%+v", out)
	}
	if !f.lastTelemetry.From.Equal(now.Add(-2*time.Minute)) || !f.lastTelemetry.To.Equal(now) || f.lastTelemetry.DeviceID != "board" || f.lastTelemetry.PortNo != 2 || f.telemetryCalls != 1 {
		t.Fatal("canonical snapshot window changed")
	}
	f.gatewayError = errors.New("offline")
	out, err = s.Snapshot(context.Background(), 7, "original")
	if err != nil || out.TelemetryAvailable == nil || *out.TelemetryAvailable || out.CurrentPowerW != nil || out.TotalCents == nil || *out.TotalCents != 0 {
		t.Fatal("optional gateway failure hid facts or forged zero observation")
	}
	f.row.Status = "completed"
	f.row.ChargedKWh = sql.NullString{String: "0.250000", Valid: true}
	before := f.telemetryCalls
	out, err = s.Snapshot(context.Background(), 7, "original")
	if err != nil || out.ChargedKWh != "0.250000" || out.PollContinue == nil || *out.PollContinue || f.telemetryCalls != before {
		t.Fatal("completed snapshot queried gateway or changed confirmed decimal")
	}
}

// TestUserDetailNullShapeAndStorageFailures 验证省略、null、空数组的区别，存储失败不伪装为缺失。
func TestUserDetailNullShapeAndStorageFailures(t *testing.T) {
	f, s, _ := userQueryService()
	f.fees = map[uint64]order.FeeFacts{}
	f.frozen.Snapshot = []byte(`{"rule":{"spec":{"display":{"show_tariff":true,"show_rule":true},"scheme":{"display":{"show_power":true},"remark":""}}},"offer":null}`)
	out, err := s.Detail(context.Background(), 7, "original")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if _, ok := data["total_cents"]; ok {
		t.Fatal("missing fee became zero")
	}
	if string(data["offer"]) != "null" || string(data["tariff"]) != "null" || string(data["tariff_lines"]) != "[]" || string(data["rule_description"]) != `""` || string(data["card_operations"]) != "[]" || string(data["refunds"]) != "[]" {
		t.Fatalf("null shape=%s", raw)
	}
	if _, ok := data["payment"]; ok {
		t.Fatal("missing payment emitted")
	}
	for _, stage := range []string{"fee", "state", "payment", "refund"} {
		f.feeError = nil
		f.stateError = nil
		f.fundsError = nil
		failure := errors.New("storage failed")
		switch stage {
		case "fee":
			f.feeError = failure
		case "state":
			f.stateError = failure
		default:
			f.fundsError = &UserReadError{Stage: stage, Cause: failure}
		}
		_, err := s.Detail(context.Background(), 7, "original")
		var read *UserReadError
		if !errors.As(err, &read) || read.Stage != stage || !errors.Is(err, failure) {
			t.Fatalf("%s hidden as missing: %v", stage, err)
		}
	}
}

// TestUserCurvesBoundWindowsAndKeepOriginalSummary 验证配置允许值、三年、2000桶边界及确认DECIMAL优先。
func TestUserCurvesBoundWindowsAndKeepOriginalSummary(t *testing.T) {
	f, s, now := userQueryService()
	s.AllowedWindows = []string{"last_2h"}
	for _, window := range []string{"last_24h", "unlimited"} {
		_, err := s.Curve(context.Background(), 7, "original", window)
		var input *UserInputError
		if !errors.As(err, &input) || input.Kind != "input" {
			t.Fatal("disabled/invalid window accepted")
		}
	}
	if f.telemetryCalls != 0 {
		t.Fatal("invalid window reached gateway")
	}
	out, err := s.Curve(context.Background(), 7, "original", "")
	if err != nil || out.Window != "last_2h" || out.Count != 0 || out.Series == nil || !out.From.Equal(now.Add(-2*time.Hour)) || !out.To.Equal(now) {
		t.Fatalf("bounded curve=%+v err=%v", out, err)
	}
	f.row.EndedAt = sql.NullTime{Time: now, Valid: true}
	f.row.ChargedKWh = sql.NullString{String: "0.250000", Valid: true}
	f.history.Summary = map[string]json.RawMessage{"total_kwh": json.RawMessage(`0.9`), "original_id": json.RawMessage(`18446744073709551614`)}
	history, err := s.HistoricalCurve(context.Background(), 7, "original", "15min")
	if err != nil || string(history.Summary["total_kwh"]) != `"0.250000"` || string(history.Summary["original_id"]) != "18446744073709551614" || f.lastHistory.OrderNo != "original" || f.lastHistory.DeviceID != "board" || f.lastHistory.PortNo != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	if string(f.history.Summary["total_kwh"]) != "0.9" {
		t.Fatal("reader summary mutated")
	}
	f.row.StartedAt.Time = now.Add(-1999 * 15 * time.Minute)
	if _, err := s.HistoricalCurve(context.Background(), 7, "original", "15min"); err != nil {
		t.Fatal("2000 original buckets rejected", err)
	}
	f.row.StartedAt.Time = now.Add(-2000 * 15 * time.Minute)
	before := f.historyCalls
	_, err = s.HistoricalCurve(context.Background(), 7, "original", "15min")
	var input *UserInputError
	if !errors.As(err, &input) || input.Kind != "input" || f.historyCalls != before {
		t.Fatal("2001 buckets reached gateway")
	}
	if _, err := s.HistoricalCurve(context.Background(), 7, "original", "hourly"); err != nil {
		t.Fatal("coarse original window rejected", err)
	}
	f.row.StartedAt.Time = now.AddDate(-3, 0, -1)
	before = f.historyCalls
	_, err = s.HistoricalCurve(context.Background(), 7, "original", "hourly")
	if !errors.As(err, &input) || input.Kind != "expired" || f.historyCalls != before {
		t.Fatal("expired window reached gateway")
	}
	f.row.StartedAt.Valid = false
	_, err = s.HistoricalCurve(context.Background(), 7, "original", "15min")
	if !errors.As(err, &input) || input.Kind != "conflict" {
		t.Fatal("unstarted order did not conflict")
	}
	f.gatewayError = errors.New("offline")
	_, err = s.Curve(context.Background(), 7, "original", "last_2h")
	var read *UserReadError
	if !errors.As(err, &read) || read.Stage != "curve" {
		t.Fatal("gateway failure became empty curve")
	}
}
