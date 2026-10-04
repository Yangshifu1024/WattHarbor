package stop

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

type fakeGateway struct {
	evidence func([]gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error)
	stop     func(gatewaycontract.StopRequest) (gatewaycontract.StopAcceptance, error)
}

func (f fakeGateway) ChargingEvidence(_ context.Context, q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
	return f.evidence(q)
}
func (f fakeGateway) RequestStop(_ context.Context, r gatewaycontract.StopRequest) (gatewaycontract.StopAcceptance, error) {
	return f.stop(r)
}

func TestEvidenceContinuesOnlyUnfinishedOrders(t *testing.T) {
	started := time.Now().UTC().Add(-time.Hour)
	orders := []AutoStopOrder{{ID: 1, DeviceID: "D-1", PortNo: 1, StartedAt: started}, {ID: 2, DeviceID: "D-2", PortNo: 2, StartedAt: started}}
	pageCalls := 0
	sample := func(device string, at time.Time) []gatewaycontract.EvidenceSample {
		return []gatewaycontract.EvidenceSample{{Type: protocol.Heartbeat, DeviceID: device, ReceivedAt: at}}
	}
	stopper := AutoStopper{Gateway: fakeGateway{evidence: func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
		pageCalls++
		if pageCalls == 1 {
			if len(q) != 2 || q[0].AfterID != 0 || q[1].AfterID != 0 {
				t.Fatalf("first queries=%+v", q)
			}
			return []gatewaycontract.EvidencePage{
				{DeviceID: "D-1", PortNo: 1, Samples: sample("D-1", started), Complete: true, NextAfterID: 11},
				{DeviceID: "D-2", PortNo: 2, Samples: sample("D-2", started), Complete: false, NextAfterID: 22},
			}, nil
		}
		if len(q) != 1 || q[0].DeviceID != "D-2" || q[0].PortNo != 2 || q[0].AfterID != 22 || !q[0].StartedAt.Equal(started) {
			t.Fatalf("continuation queries=%+v", q)
		}
		return []gatewaycontract.EvidencePage{{DeviceID: "D-2", PortNo: 2, Samples: sample("D-2", started.Add(time.Minute)), Complete: true, NextAfterID: 33}}, nil
	}}}
	got, err := stopper.evidence(context.Background(), orders)
	if err != nil || pageCalls != 2 || len(got[1]) != 1 || len(got[2]) != 2 || got[2][1].DeviceID != "D-2" {
		t.Fatalf("evidence=%+v calls=%d err=%v", got, pageCalls, err)
	}
}

func TestEvidenceBatchesAtGatewayLimit(t *testing.T) {
	orders := make([]AutoStopOrder, 23)
	for i := range orders {
		orders[i] = AutoStopOrder{ID: uint64(i + 1), DeviceID: "D-1", PortNo: uint8(i + 1)}
	}
	var sizes []int
	stopper := AutoStopper{Gateway: fakeGateway{evidence: func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
		sizes = append(sizes, len(q))
		pages := make([]gatewaycontract.EvidencePage, len(q))
		for i, query := range q {
			pages[i] = gatewaycontract.EvidencePage{DeviceID: query.DeviceID, PortNo: query.PortNo, Complete: true}
		}
		return pages, nil
	}}}
	if _, err := stopper.evidence(context.Background(), orders); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 2 || sizes[0] != 20 || sizes[1] != 3 {
		t.Fatalf("batch sizes=%v", sizes)
	}
}

func TestEvidenceRejectsPartialOrInvalidPages(t *testing.T) {
	cases := []struct {
		name   string
		result func([]gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error)
		want   error
		calls  int
	}{
		{"missing entry", func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) { return nil, nil }, ErrEvidenceIncomplete, 1},
		{"identity mismatch", func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
			return []gatewaycontract.EvidencePage{{DeviceID: "other", PortNo: 1, Complete: true}}, nil
		}, ErrEvidenceIncomplete, 1},
		{"zero cursor", func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
			return []gatewaycontract.EvidencePage{{DeviceID: "D-1", PortNo: 1}}, nil
		}, ErrEvidenceIncomplete, 1},
		{"stuck cursor", func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
			return []gatewaycontract.EvidencePage{{DeviceID: "D-1", PortNo: 1, NextAfterID: 1}}, nil
		}, ErrEvidenceIncomplete, 2},
		{"page limit", func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
			return []gatewaycontract.EvidencePage{{DeviceID: "D-1", PortNo: 1, NextAfterID: q[0].AfterID + 1}}, nil
		}, ErrEvidenceIncomplete, autoStopEvidenceMaxPages},
		{"gateway failure", func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
			return nil, context.DeadlineExceeded
		}, context.DeadlineExceeded, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			stopper := AutoStopper{Gateway: fakeGateway{evidence: func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
				calls++
				return tc.result(q)
			}}}
			got, err := stopper.evidence(context.Background(), []AutoStopOrder{{ID: 1, DeviceID: "D-1", PortNo: 1}})
			if !errors.Is(err, tc.want) || got != nil || calls != tc.calls {
				t.Fatalf("evidence=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestRequestStopUsesOrderIdentityAndReturnsGatewayError(t *testing.T) {
	want := errors.New("gateway unavailable")
	stopper := AutoStopper{Gateway: fakeGateway{stop: func(r gatewaycontract.StopRequest) (gatewaycontract.StopAcceptance, error) {
		if r.OrderNo != "C-1" || r.UserID != 9007199254740993 || r.Source != "auto" {
			t.Fatalf("request=%+v", r)
		}
		return gatewaycontract.StopAcceptance{}, want
	}}}
	if err := stopper.requestStop(context.Background(), AutoStopOrder{OrderNo: "C-1", UserID: 9007199254740993}); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}
