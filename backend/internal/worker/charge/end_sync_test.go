package charge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/protocol"
)

// endSyncScenario 记录结束同步各阶段调用，按 fail 指定的阶段返回错误。
type endSyncScenario struct {
	// event 待同步协议结束事件 fixture，保留 Wh 与秒字段值。
	event protocol.Event
	// command 已确认 START 命令 fixture，调用方自行核对身份。
	command gatewaycontract.SyncCommand
	fail    string
	calls   []string
	// frozen 内存首份 JSON 载荷，后续候选不覆盖；不模拟真实数据库竞争。
	frozen string
	// results 收到的中央结束请求集合，仅供重放内容断言。
	results []centralcontract.EndResultRequest
	// releases 收到的网关端口释放请求集合，不表示实际占用变更。
	releases []gatewaycontract.ReleasePortRequest
	// marked 收到的处理事件 ID 集合，不表示提交标记。
	marked []uint64
}

// EndEvents 返回预设结束事件，事件 ID 为 77。
func (s *endSyncScenario) EndEvents(context.Context) ([]gatewaycontract.SyncEvent, error) {
	data, _ := json.Marshal(s.event)
	return []gatewaycontract.SyncEvent{{ID: 77, Payload: string(data)}}, nil
}

// ChargeCommand 返回预设启动命令。
func (s *endSyncScenario) ChargeCommand(context.Context, uint64) (*gatewaycontract.SyncCommand, error) {
	return &s.command, nil
}

// MeterSamples 核对设备、时间窗口和事件 ID，再返回预设错误或空列表。
func (s *endSyncScenario) MeterSamples(_ context.Context, q gatewaycontract.MeterSamplesRequest) ([]gatewaycontract.SyncEvent, error) {
	s.calls = append(s.calls, "samples")
	if q.EndID != 77 || q.DeviceID != s.event.DeviceID || !q.AckAt.Equal(*s.command.AckAt) || !q.EndAt.Equal(s.event.ReceivedAt) {
		return nil, errors.New("wrong evidence window")
	}
	if s.fail == "samples" {
		return nil, errors.New("samples failed")
	}
	return []gatewaycontract.SyncEvent{}, nil
}

// FreezeEndDelivery 校验事件和订单，保存内存中的首次载荷；固定返回 frozen=true，不测试数据库竞争。
func (s *endSyncScenario) FreezeEndDelivery(_ context.Context, r gatewaycontract.FreezeEndDeliveryRequest) (gatewaycontract.FreezeEndDeliveryReply, error) {
	s.calls = append(s.calls, "freeze")
	if s.fail == "freeze" {
		return gatewaycontract.FreezeEndDeliveryReply{}, errors.New("freeze failed")
	}
	if r.DeviceEventID != 77 || r.ChargeOrderID != 9 {
		return gatewaycontract.FreezeEndDeliveryReply{}, errors.New("wrong frozen identity")
	}
	if s.frozen == "" {
		s.frozen = r.Payload
	}
	return gatewaycontract.FreezeEndDeliveryReply{Payload: s.frozen, Frozen: true}, nil
}

// SubmitEndResult 记录请求和调用顺序，返回预设错误或回复；replayed 按请求次数生成。
func (s *endSyncScenario) SubmitEndResult(_ context.Context, r centralcontract.EndResultRequest) (centralcontract.ResultReply, error) {
	s.calls = append(s.calls, "central")
	s.results = append(s.results, r)
	if s.fail == "central" {
		return centralcontract.ResultReply{}, errors.New("central failed")
	}
	return centralcontract.ResultReply{Accepted: true, Replayed: len(s.results) > 1}, nil
}

// ReleasePort 记录释放请求，返回预设错误或已释放回复。
func (s *endSyncScenario) ReleasePort(_ context.Context, r gatewaycontract.ReleasePortRequest) (gatewaycontract.ReleasePortReply, error) {
	s.calls = append(s.calls, "release")
	s.releases = append(s.releases, r)
	if s.fail == "release" {
		return gatewaycontract.ReleasePortReply{}, errors.New("release failed")
	}
	return gatewaycontract.ReleasePortReply{Released: true, Already: len(s.releases) > 1}, nil
}

// MarkDeviceEventsProcessed 记录待标记 ID，返回预设错误或 ID 数量。
func (s *endSyncScenario) MarkDeviceEventsProcessed(_ context.Context, ids []uint64) (int, error) {
	s.calls = append(s.calls, "mark")
	s.marked = append(s.marked, ids...)
	if s.fail == "mark" {
		return 0, errors.New("mark failed")
	}
	return len(ids), nil
}

// newEndScenario 创建固定启动时刻、命令编号和计量读数的结束同步场景。
func newEndScenario() *endSyncScenario {
	start := time.Date(2026, 10, 5, 4, 0, 0, 0, time.UTC)
	command := startSyncRow()
	command.AckAt = &start
	command.StopCommandID = "stop-1"
	return &endSyncScenario{command: command, event: protocol.Event{Type: protocol.ChargeEnd, DeviceID: "D-1", Port: 1, OrderNumber: "9", ConsumerType: 2, StartedAt: start, EndedAt: start.Add(time.Hour), ReceivedAt: start.Add(time.Hour), EnergyMilliKWh: 1000, ChargedSeconds: 3600}}
}

func TestEndSyncFailureStopsAtRequiredBoundary(t *testing.T) {
	for _, tc := range []struct {
		fail  string
		want  []string
		count int
	}{
		{"samples", []string{"samples"}, 0}, {"freeze", []string{"samples", "freeze"}, 0},
		{"central", []string{"samples", "freeze", "central"}, 0}, {"release", []string{"samples", "freeze", "central", "release"}, 0},
		{"mark", []string{"samples", "freeze", "central", "release", "mark"}, 1},
	} {
		t.Run(tc.fail, func(t *testing.T) {
			s := newEndScenario()
			s.fail = tc.fail
			n, err := (EndSynchronizer{Gateway: s, Central: s}).SyncBatch(context.Background())
			if err == nil || n != tc.count || !reflect.DeepEqual(s.calls, tc.want) {
				t.Fatalf("count=%d calls=%v err=%v", n, s.calls, err)
			}
		})
	}
}

func TestEndSyncReplaysFrozenEvidenceBeforeReleaseAndMark(t *testing.T) {
	s := newEndScenario()
	frozen := centralcontract.EndResultRequest{OrderNo: "C-9", ChargeOrderID: 9, StartCommandID: "start-1", StopCommandID: "stop-1", DeviceID: "D-1", PortNo: 1, PortID: 9007199254740993,
		Meter: charging.EndMeter{ChargedWh: 1000, ChargedSeconds: 3600, EndedAt: s.event.EndedAt, Segments: []charging.MeterSegment{{StartedAt: s.event.StartedAt, EndedAt: s.event.EndedAt, EnergyWh: 1000}}}}
	data, _ := json.Marshal(frozen)
	s.frozen = string(data)
	worker := EndSynchronizer{Gateway: s, Central: s}
	for range 2 {
		if n, err := worker.SyncBatch(context.Background()); err != nil || n != 1 {
			t.Fatalf("count=%d err=%v", n, err)
		}
	}
	if len(s.results) != 2 || !reflect.DeepEqual(s.results[0], frozen) || !reflect.DeepEqual(s.results[0], s.results[1]) || !reflect.DeepEqual(s.marked, []uint64{77, 77}) {
		t.Fatalf("results=%+v marked=%v", s.results, s.marked)
	}
	if !reflect.DeepEqual(s.calls, []string{"samples", "freeze", "central", "release", "mark", "samples", "freeze", "central", "release", "mark"}) || s.releases[0].OrderNo != "C-9" || s.releases[0].PortID != 9007199254740993 {
		t.Fatalf("calls=%v releases=%+v", s.calls, s.releases)
	}
}

func TestEndSyncRejectsWrongCommandIdentity(t *testing.T) {
	s := newEndScenario()
	s.command.ChargeOrderID = 10
	if n, err := (EndSynchronizer{Gateway: s, Central: s}).SyncBatch(context.Background()); err == nil || n != 0 || len(s.calls) != 0 {
		t.Fatalf("count=%d calls=%v err=%v", n, s.calls, err)
	}
}
