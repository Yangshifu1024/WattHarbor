package stop

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/protocol"
)

// fakeGateway 仅以注入回调提供证据与停止响应，供纯编排检查，不执行服务间网络调用。
type fakeGateway struct {
	// evidence 证据分页回调，接收原查询切片并返回分页回复或错误，仅用于纯编排检查。
	evidence func([]gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error)
	// stop 停止回调，接收原请求并返回受理回复或错误，不实际发送设备指令。
	stop func(gatewaycontract.StopRequest) (gatewaycontract.StopReply, error)
}

// ChargingEvidence 忽略传入的 context，将查询切片 q 原样交给非 nil 的 f.evidence；返回其分页回复与错误，不发送 HTTP 或读取数据库，回调可记录查询副作用。
func (f fakeGateway) ChargingEvidence(_ context.Context, q []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
	return f.evidence(q)
}

// RequestStop 忽略传入的 context，将停止请求 r 原样交给非 nil 的 f.stop；返回其回复与错误，不发送设备指令，回调可记录请求副作用。
func (f fakeGateway) RequestStop(_ context.Context, r gatewaycontract.StopRequest) (gatewaycontract.StopReply, error) {
	return f.stop(r)
}

// TestEvidenceContinuesOnlyUnfinishedOrders 使用 t 核对仅对未完成订单继续分页并保持设备、端口、起点与游标身份；无返回值，失败由 t 报告，网关回调只构造内存页，不证明数据库证据。
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

// TestEvidenceBatchesAtGatewayLimit 使用 t 核对二十三个订单分为二十与三个查询的两次调用；无返回值，失败由 t 报告，夹具不发 HTTP 请求或读取数据库。
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

// TestEvidenceRejectsPartialOrInvalidPages 使用 t 核对缺项、身份不符、零或停滞游标、页数上限及网关错误均拒绝证据，并比对调用次数；无返回值，仅以内存回调验证编排，不证明 HTTP 或数据库行为。
func TestEvidenceRejectsPartialOrInvalidPages(t *testing.T) {
	cases := []struct {
		// name 当前边界场景名称，供 t.Run 区分断言；不作为业务身份或数据库条件。
		name string
		// result 当前分页故障夹具；接收原查询并返回不完整、非法页或执行错误，不访问网关。
		result func([]gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error)
		// want 当前非法分页或网关失败预期匹配的错误，按 errors.Is 比对。
		want error
		// calls 期望的证据查询调用次数，用于确认拒绝点、游标停滞或页数上限。
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

// TestRequestStopUsesOrderIdentityAndReturnsGatewayError 使用 t 核对停止请求保留订单号、超过 JavaScript 安全整数范围的用户身份与自动来源，并传回网关错误；无返回值，仅记录内存请求，不真正停机。
func TestRequestStopUsesOrderIdentityAndReturnsGatewayError(t *testing.T) {
	want := errors.New("gateway unavailable")
	stopper := AutoStopper{Gateway: fakeGateway{stop: func(r gatewaycontract.StopRequest) (gatewaycontract.StopReply, error) {
		if r.OrderNo != "C-1" || r.UserID != 9007199254740993 || r.Source != "auto" {
			t.Fatalf("request=%+v", r)
		}
		return gatewaycontract.StopReply{}, want
	}}}
	if err := stopper.requestStop(context.Background(), AutoStopOrder{OrderNo: "C-1", UserID: 9007199254740993}); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}
