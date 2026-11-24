package stop

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewaycontract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/protocol"
)

// fakeGateway 仅以注入回调提供心跳与停止响应，供调用顺序检查，不执行服务间网络调用。
type fakeGateway struct {
	// evidence 心跳分页回调，接收查询切片并返回分页回复或错误，仅用于调用顺序检查。
	evidence func([]gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error)
	// stop 停止回调，接收请求并返回受理回复或错误，不实际发送设备指令。
	stop func(gatewaycontract.StopRequest) (gatewaycontract.StopReply, error)
}

// ChargingEvidence 将查询交给注入回调，不发送 HTTP 或读取数据库。
func (f fakeGateway) ChargingEvidence(_ context.Context, q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
	return f.evidence(q)
}

// RequestStop 将停止请求交给注入回调，不发送设备指令。
func (f fakeGateway) RequestStop(_ context.Context, r gatewaycontract.StopRequest) (gatewaycontract.StopReply, error) {
	return f.stop(r)
}

// TestEvidenceContinuesOnlyUnfinishedOrders 验证只续查未完成订单，保留设备、端口、起点与游标。
func TestEvidenceContinuesOnlyUnfinishedOrders(t *testing.T) {
	started := time.Now().UTC().Add(-time.Hour)
	orders := []AutoStopOrder{{ID: 1, DeviceID: "D-1", PortNo: 1, StartedAt: started}, {ID: 2, DeviceID: "D-2", PortNo: 2, StartedAt: started}}
	pageCalls := 0
	sample := func(device string, at time.Time) []gatewaycontract.EvidenceSample {
		return []gatewaycontract.EvidenceSample{{Type: protocol.Heartbeat, DeviceID: device, ReceivedAt: at}}
	}
	stopper := AutoStopper{Gateway: fakeGateway{evidence: func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
		pageCalls++
		if pageCalls == 1 {
			if len(q) != 2 || q[0].AfterID != 0 || q[1].AfterID != 0 {
				t.Fatalf("first queries=%+v", q)
			}
			return []gatewaycontract.EvidencePageReply{
				{DeviceID: "D-1", PortNo: 1, Samples: sample("D-1", started), Complete: true, NextAfterID: 11},
				{DeviceID: "D-2", PortNo: 2, Samples: sample("D-2", started), Complete: false, NextAfterID: 22},
			}, nil
		}
		if len(q) != 1 || q[0].DeviceID != "D-2" || q[0].PortNo != 2 || q[0].AfterID != 22 || !q[0].StartedAt.Equal(started) {
			t.Fatalf("continuation queries=%+v", q)
		}
		return []gatewaycontract.EvidencePageReply{{DeviceID: "D-2", PortNo: 2, Samples: sample("D-2", started.Add(time.Minute)), Complete: true, NextAfterID: 33}}, nil
	}}}
	got, err := stopper.evidence(context.Background(), orders)
	if err != nil || pageCalls != 2 || len(got[1]) != 1 || len(got[2]) != 2 || got[2][1].DeviceID != "D-2" {
		t.Fatalf("evidence=%+v calls=%d err=%v", got, pageCalls, err)
	}
}

// TestEvidenceBatchesAtGatewayLimit 验证 23 笔订单分为 20 与 3 两批；只使用内存回调。
func TestEvidenceBatchesAtGatewayLimit(t *testing.T) {
	orders := make([]AutoStopOrder, 23)
	for i := range orders {
		orders[i] = AutoStopOrder{ID: uint64(i + 1), DeviceID: "D-1", PortNo: uint8(i + 1)}
	}
	var sizes []int
	stopper := AutoStopper{Gateway: fakeGateway{evidence: func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
		sizes = append(sizes, len(q))
		pages := make([]gatewaycontract.EvidencePageReply, len(q))
		for i, query := range q {
			pages[i] = gatewaycontract.EvidencePageReply{DeviceID: query.DeviceID, PortNo: query.PortNo, Complete: true}
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

// TestEvidenceRejectsPartialOrInvalidPages 验证缺项、身份错配、无效游标、页数超限或网关错误均拒绝使用返回心跳。
// 仅覆盖内存编排，不代表 HTTP 或数据库验证。
func TestEvidenceRejectsPartialOrInvalidPages(t *testing.T) {
	cases := []struct {
		name   string
		result func([]gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error)
		// want 当前非法分页或网关失败预期匹配的错误，按 errors.Is 比对。
		want error
		// calls 期望的心跳查询次数，用于确认拒绝点、游标停滞或页数上限。
		calls int
	}{
		{"missing entry", func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
			return nil, nil
		}, ErrEvidenceIncomplete, 1},
		{"identity mismatch", func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
			return []gatewaycontract.EvidencePageReply{{DeviceID: "other", PortNo: 1, Complete: true}}, nil
		}, ErrEvidenceIncomplete, 1},
		{"zero cursor", func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
			return []gatewaycontract.EvidencePageReply{{DeviceID: "D-1", PortNo: 1}}, nil
		}, ErrEvidenceIncomplete, 1},
		{"stuck cursor", func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
			return []gatewaycontract.EvidencePageReply{{DeviceID: "D-1", PortNo: 1, NextAfterID: 1}}, nil
		}, ErrEvidenceIncomplete, 2},
		{"page limit", func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
			return []gatewaycontract.EvidencePageReply{{DeviceID: "D-1", PortNo: 1, NextAfterID: q[0].AfterID + 1}}, nil
		}, ErrEvidenceIncomplete, autoStopEvidenceMaxPages},
		{"gateway failure", func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
			return nil, context.DeadlineExceeded
		}, context.DeadlineExceeded, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			stopper := AutoStopper{Gateway: fakeGateway{evidence: func(q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
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

// TestRequestStopUsesOrderIdentityAndReturnsGatewayError 验证停止请求保留订单号、大整数用户标识与自动来源，且返回网关错误。
// 只记录内存请求，不实际停机。
func TestRequestStopUsesOrderIdentityAndReturnsGatewayError(t *testing.T) {
	want := errors.New("gateway unavailable")
	stopper := AutoStopper{Gateway: fakeGateway{stop: func(r gatewaycontract.StopRequest) (gatewaycontract.StopReply, error) {
		if r.OrderNo != "C-1" || r.UserID != 9007199254740993 || r.Source != gatewaycontract.StopSourceAuto {
			t.Fatalf("request=%+v", r)
		}
		return gatewaycontract.StopReply{}, want
	}}}
	if err := stopper.requestStop(context.Background(), AutoStopOrder{OrderNo: "C-1", UserID: 9007199254740993}); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}
