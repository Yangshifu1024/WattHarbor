package charge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

type endSyncScenario struct {
	event    protocol.Event
	command  gatewaycontract.SyncCommand
	fail     string
	calls    []string
	frozen   string
	results  []centralcontract.EndResult
	releases []gatewaycontract.ReleasePortRequest
	marked   []uint64
}

func (s *endSyncScenario) EndEvents(context.Context) ([]gatewaycontract.SyncEvent, error) {
	data, _ := json.Marshal(s.event)
	return []gatewaycontract.SyncEvent{{ID: 77, Payload: string(data)}}, nil
}
func (s *endSyncScenario) ChargeCommand(context.Context, uint64) (*gatewaycontract.SyncCommand, error) {
	return &s.command, nil
}
func (s *endSyncScenario) MeterSamples(_ context.Context, q gatewaycontract.MeterSamplesQuery) ([]gatewaycontract.SyncEvent, error) {
	s.calls = append(s.calls, "samples")
	if q.EndID != 77 || q.DeviceID != s.event.DeviceID || !q.AckAt.Equal(*s.command.AckAt) || !q.EndAt.Equal(s.event.ReceivedAt) {
		return nil, errors.New("wrong evidence window")
	}
	if s.fail == "samples" {
		return nil, errors.New("samples failed")
	}
	return []gatewaycontract.SyncEvent{}, nil
}
func (s *endSyncScenario) FreezeEndDelivery(_ context.Context, r gatewaycontract.FreezeEndDeliveryRequest) (gatewaycontract.FreezeEndDeliveryResult, error) {
	s.calls = append(s.calls, "freeze")
	if s.fail == "freeze" {
		return gatewaycontract.FreezeEndDeliveryResult{}, errors.New("freeze failed")
	}
	if r.DeviceEventID != 77 || r.ChargeOrderID != 9 {
		return gatewaycontract.FreezeEndDeliveryResult{}, errors.New("wrong frozen identity")
	}
	if s.frozen == "" {
		s.frozen = r.Payload
	}
	return gatewaycontract.FreezeEndDeliveryResult{Payload: s.frozen, Frozen: true}, nil
}
func (s *endSyncScenario) SubmitEndResult(_ context.Context, r centralcontract.EndResult) (centralcontract.ResultAcceptance, error) {
	s.calls = append(s.calls, "central")
	s.results = append(s.results, r)
	if s.fail == "central" {
		return centralcontract.ResultAcceptance{}, errors.New("central failed")
	}
	return centralcontract.ResultAcceptance{Accepted: true, Replayed: len(s.results) > 1}, nil
}
func (s *endSyncScenario) ReleasePort(_ context.Context, r gatewaycontract.ReleasePortRequest) (gatewaycontract.ReleasePortResult, error) {
	s.calls = append(s.calls, "release")
	s.releases = append(s.releases, r)
	if s.fail == "release" {
		return gatewaycontract.ReleasePortResult{}, errors.New("release failed")
	}
	return gatewaycontract.ReleasePortResult{Released: true, Already: len(s.releases) > 1}, nil
}
func (s *endSyncScenario) MarkDeviceEventsProcessed(_ context.Context, ids []uint64) (int, error) {
	s.calls = append(s.calls, "mark")
	s.marked = append(s.marked, ids...)
	if s.fail == "mark" {
		return 0, errors.New("mark failed")
	}
	return len(ids), nil
}
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
	frozen := centralcontract.EndResult{OrderNo: "C-9", ChargeOrderID: 9, StartCommandID: "start-1", StopCommandID: "stop-1", DeviceID: "D-1", PortNo: 1, PortID: 9007199254740993,
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
