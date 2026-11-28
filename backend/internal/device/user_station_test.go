package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	contract "wattharbor/internal/contracts/gateway"
)

// stationSummaryFunc 提供摘要查询测试替身，不模拟数据库。
type stationSummaryFunc func(context.Context, []string) ([]contract.DeviceSummary, error)

// DeviceSummaries 调用测试函数获取摘要，不启动后台任务。
func (f stationSummaryFunc) DeviceSummaries(ctx context.Context, ids []string) ([]contract.DeviceSummary, error) {
	return f(ctx, ids)
}

// TestStationRuntimeBatchesAndPreservesUnknown 验证201台设备三批、失败批隔离及心跳 null／零差异。
func TestStationRuntimeBatchesAndPreservesUnknown(t *testing.T) {
	rows := make([]stationDeviceRow, 201)
	for i := range rows {
		rows[i] = stationDeviceRow{DeviceID: fmt.Sprintf("station-%03d", i), Status: "enabled"}
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

// TestStationRuntimeRejectsWrongIdentityAndStopsOnCancel 验证重复或额外设备不被合并，取消后不继续查询。
func TestStationRuntimeRejectsWrongIdentityAndStopsOnCancel(t *testing.T) {
	for _, extras := range [][]contract.DeviceSummary{{{DeviceID: "outside"}}, {{DeviceID: "one"}, {DeviceID: "one"}}} {
		s := UserStationService{Summaries: stationSummaryFunc(func(context.Context, []string) ([]contract.DeviceSummary, error) { return extras, nil })}
		out, err := s.runtime(context.Background(), []stationDeviceRow{{DeviceID: "one", Status: "enabled"}})
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

// TestStationAdmissionBeforeStorage 验证非法输入在数据库访问前被拒绝。
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

// TestStationPortCountContract 核对旧网关缺少可用数、停用设备、缺失摘要和已知空站的公开 JSON。
func TestStationPortCountContract(t *testing.T) {
	rows := []stationDeviceRow{{DeviceID: "旧网关", Status: "enabled"}, {DeviceID: "停用", Status: "disabled"}, {DeviceID: "未同步", Status: "enabled"}}
	service := UserStationService{Summaries: stationSummaryFunc(func(context.Context, []string) ([]contract.DeviceSummary, error) {
		return []contract.DeviceSummary{
			{DeviceID: "旧网关", Ports: []contract.DevicePortSummary{{PortNo: 1}}},
			{DeviceID: "停用", Ports: []contract.DevicePortSummary{{PortNo: 1}}, AvailablePortCount: new(1)},
		}, nil
	})}
	devices, err := service.runtime(context.Background(), rows)
	if err != nil {
		t.Fatal(err)
	}
	station := portCounts{new(int), new(int)}
	check := func(counts portCounts, want string) {
		t.Helper()
		got, err := json.Marshal(counts)
		if err != nil || string(got) != want {
			t.Fatalf("端口字段 %s，预期 %s，错误 %v", got, want, err)
		}
	}
	check(station, `{"available_port_count":0,"total_port_count":0}`)
	check(devices[0].portCounts, `{"available_port_count":null,"total_port_count":1}`)
	check(devices[1].portCounts, `{"available_port_count":0,"total_port_count":1}`)
	for _, device := range devices[:2] {
		station.add(device.portCounts)
	}
	check(station, `{"available_port_count":null,"total_port_count":2}`)
	station.add(devices[2].portCounts)
	check(station, `{"available_port_count":null,"total_port_count":null}`)
}

// TestPublicStationIDsKeepAdjacentKeys 验证双精度 JSON 客户端仍能区分相邻站点，并保留数字 id 兼容字段。
func TestPublicStationIDsKeepAdjacentKeys(t *testing.T) {
	const id uint64 = 9054542607526958568
	data, err := json.Marshal([]stationView{
		stationPublicView(userStationRow{ID: id}), stationPublicView(userStationRow{ID: id + 1}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0]["id"] != float64(id) || decoded[1]["id"] != decoded[0]["id"] ||
		decoded[0]["id_text"] != "9054542607526958568" || decoded[1]["id_text"] != "9054542607526958569" {
		t.Fatalf("大整数站点编号未按兼容契约保留：%s", data)
	}
}
