package device

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

// stationSummaryFunc 只在给定ctx和ids下提供摘要fixture，不模拟数据库。
type stationSummaryFunc func(context.Context, []string) ([]contract.DeviceSummary, error)

// DeviceSummaries 把ctx、ids交给f，返回其原数组／错误，不开后台任务。
func (f stationSummaryFunc) DeviceSummaries(ctx context.Context, ids []string) ([]contract.DeviceSummary, error) {
	return f(ctx, ids)
}

// TestStationRuntimeBatchesAndPreservesUnknown 验证201台设备三批、失败批隔离及心跳null／零差异。
func TestStationRuntimeBatchesAndPreservesUnknown(t *testing.T) {
	rows := make([]stationDeviceRow, 201)
	for i := range rows {
		rows[i] = stationDeviceRow{fmt.Sprintf("station-%03d", i), "active"}
	}
	call := 0
	recent, old := time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour)
	s := UserStationService{Summaries: stationSummaryFunc(func(ctx context.Context, ids []string) ([]contract.DeviceSummary, error) {
		call++
		if len(ids) > 100 {
			t.Fatalf("unbounded batch %d", len(ids))
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing managed deadline")
		}
		if call == 2 {
			return nil, errors.New("one batch unavailable")
		}
		out := []contract.DeviceSummary{}
		for _, id := range ids {
			out = append(out, contract.DeviceSummary{DeviceID: id, LastHeartbeatAt: &recent})
		}
		if call == 1 {
			out[0].LastHeartbeatAt = nil
			out[1].LastHeartbeatAt = &old
		}
		return out, nil
	})}
	out, err := s.runtime(context.Background(), rows)
	if err != nil || call != 3 || len(out) != 201 {
		t.Fatalf("result=%d calls=%d err=%v", len(out), call, err)
	}
	if !out[0].RuntimeAvailable || out[0].Online || out[0].LastHeartbeatAt != nil {
		t.Fatalf("known no heartbeat %+v", out[0])
	}
	if !out[1].RuntimeAvailable || out[1].Online {
		t.Fatalf("known offline %+v", out[1])
	}
	if !out[2].Online || out[100].RuntimeAvailable || out[100].LastHeartbeatAt != nil || !out[200].Online {
		t.Fatal("batch failure leaked or unaffected batch lost")
	}
}

// TestStationRuntimeRejectsWrongIdentityAndStopsOnCancel 验证重复／额外身份不伪造已知、取消不继续取证。
func TestStationRuntimeRejectsWrongIdentityAndStopsOnCancel(t *testing.T) {
	for _, extras := range [][]contract.DeviceSummary{{{DeviceID: "outside"}}, {{DeviceID: "one"}, {DeviceID: "one"}}} {
		s := UserStationService{Summaries: stationSummaryFunc(func(context.Context, []string) ([]contract.DeviceSummary, error) { return extras, nil })}
		out, err := s.runtime(context.Background(), []stationDeviceRow{{"one", "active"}})
		if err != nil || out[0].RuntimeAvailable {
			t.Fatalf("bad identity %+v %v", out, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	s := UserStationService{Summaries: stationSummaryFunc(func(context.Context, []string) ([]contract.DeviceSummary, error) {
		calls++
		cancel()
		return nil, ctx.Err()
	})}
	rows := make([]stationDeviceRow, 101)
	_, err := s.runtime(ctx, rows)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancel calls=%d err=%v", calls, err)
	}
}

// TestStationAdmissionBeforeStorage 验证坏输入在nil数据库之前被拒绝，不产生SQL。
func TestStationAdmissionBeforeStorage(t *testing.T) {
	for _, q := range []StationQuery{{Page: 0, PageSize: 20, RadiusKM: 50}, {Page: 1, PageSize: 101, RadiusKM: 50}, {Page: 1, PageSize: 20, RadiusKM: math.NaN()}, {Page: 1, PageSize: 20, RadiusKM: 51}, {Page: 1, PageSize: 20, RadiusKM: 50, Located: true, Latitude: 91}} {
		if _, err := (UserStationService{}).Nearby(context.Background(), q); err == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	if _, err := (UserStationService{}).Detail(context.Background(), 0); err == nil {
		t.Fatal("zero station accepted")
	}
}
