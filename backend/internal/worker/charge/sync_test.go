package charge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	centralcontract "wattharbor/internal/contracts/central"
	gatewaycontract "wattharbor/internal/contracts/gateway"
)

// startSyncScenario 记录启动同步请求和次序，按配置返回错误。
type startSyncScenario struct {
	deferred []gatewaycontract.DeferSyncDeliveryRequest // 记录失败分类和投递身份
	// rows 启动回执 fixture 列表，不表示数据库查询结果已经提交。
	rows []gatewaycontract.SyncCommand
	// deliverErr,compensateErr,markErr 分别控制中央投递、启动补偿和上报标记 fixture 的错误值。
	deliverErr, compensateErr, markErr error
	filter                             string
	// results fixture 收到的中央启动请求，用于身份与时间比较。
	results []centralcontract.StartResultRequest
	// compensations,reported fixture 分别记录的补偿身份文本和请求上报命令集合，不代表已保存状态。
	compensations, reported []string
	calls                   []string
}

// DeferSyncDelivery 记录固定分类，不改变 START 回执。
func (s *startSyncScenario) DeferSyncDelivery(_ context.Context, r gatewaycontract.DeferSyncDeliveryRequest) error {
	s.deferred = append(s.deferred, r)
	return nil
}

// StartResults 记录筛选条件，返回预设启动结果。
func (s *startSyncScenario) StartResults(_ context.Context, filter string) ([]gatewaycontract.SyncCommand, error) {
	s.filter = filter
	return s.rows, nil
}

// SubmitStartResult 记录中央请求和次序，返回预设错误；没有错误时 accepted=true。
func (s *startSyncScenario) SubmitStartResult(_ context.Context, r centralcontract.StartResultRequest) (centralcontract.ResultReply, error) {
	s.calls = append(s.calls, "central:"+r.CommandID)
	s.results = append(s.results, r)
	return centralcontract.ResultReply{Accepted: s.deliverErr == nil}, s.deliverErr
}

// CompensateStart 记录订单号、命令号和次序，返回预设补偿错误。
func (s *startSyncScenario) CompensateStart(_ context.Context, orderNo, commandID string) error {
	s.calls = append(s.calls, "compensate:"+commandID)
	s.compensations = append(s.compensations, orderNo+":"+commandID)
	return s.compensateErr
}

// MarkStartResultsReported 记录命令 ID 和次序，返回 ID 数量和预设错误。
func (s *startSyncScenario) MarkStartResultsReported(_ context.Context, ids []string) (int, error) {
	s.calls = append(s.calls, "mark")
	s.reported = append(s.reported, ids...)
	return len(ids), s.markErr
}

// startSyncRow 构造成功启动结果，端口 ID 超过 2^53，确认时刻使用 UTC+8。
func startSyncRow() gatewaycontract.SyncCommand {
	port, code := int64(9007199254740993), int16(0)
	ack := time.Date(2026, 10, 5, 12, 0, 0, 123000000, time.FixedZone("CST", 8*3600))
	return gatewaycontract.SyncCommand{CommandID: "start-1", ChargeOrderID: 9, OrderNo: "C-9", DeviceID: "D-1", PortNo: 1, PortID: &port, Status: "acked", ResultCode: &code, AckAt: &ack}
}

func TestStartSyncOnlyMarksPersistedResultsAndUsesOriginalIdentity(t *testing.T) {
	s := &startSyncScenario{rows: []gatewaycontract.SyncCommand{startSyncRow()}}
	worker := Synchronizer{Gateway: s, Central: s, CommandFilter: "start-1"}
	for range 2 {
		n, err := worker.SyncBatch(context.Background())
		if err != nil || n != 1 {
			t.Fatalf("count=%d err=%v", n, err)
		}
	}
	if s.filter != "start-1" || !reflect.DeepEqual(s.calls, []string{"central:start-1", "mark", "central:start-1", "mark"}) || len(s.results) != 2 || !reflect.DeepEqual(s.results[0], s.results[1]) {
		t.Fatalf("calls=%v results=%v", s.calls, s.results)
	}
	r := s.results[0]
	if r.CommandID != "start-1" || r.PortID != 9007199254740993 || r.OccurredAt.Location() != time.UTC || !r.Success || r.ResultCode != 0 {
		t.Fatalf("identity or receipt changed: %+v", r)
	}
}

func TestStartSyncConflictCompensatesOnlySuccessfulStartAndNeverMarks(t *testing.T) {
	for _, success := range []bool{true, false} {
		row := startSyncRow()
		if !success {
			row.Status = "rejected"
			code := int16(1)
			row.ResultCode = &code
		}
		s := &startSyncScenario{rows: []gatewaycontract.SyncCommand{row}, deliverErr: centralcontract.ErrResultConflict}
		n, err := (Synchronizer{Gateway: s, Central: s}).SyncBatch(context.Background())
		if n != 0 || len(s.reported) != 0 || (len(s.compensations) == 1) != success || !errors.Is(err, centralcontract.ErrResultConflict) {
			t.Fatalf("success=%t count=%d reported=%v compensation=%v err=%v", success, n, s.reported, s.compensations, err)
		}
		if success && (!reflect.DeepEqual(s.calls, []string{"central:start-1", "compensate:start-1"}) || s.compensations[0] != "C-9:start-1") {
			t.Fatalf("wrong compensation identity/order: %v %v", s.calls, s.compensations)
		}
	}
}

func TestStartSyncFailuresKeepUnreportedReceipt(t *testing.T) {
	unavailable := errors.New("unavailable")
	for _, tc := range []struct {
		name string
		// delivery,compensation 该场景中央投递与补偿能力注入的错误。
		delivery, compensation error
	}{
		{"central failure", unavailable, nil}, {"compensation failure", centralcontract.ErrResultConflict, unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &startSyncScenario{rows: []gatewaycontract.SyncCommand{startSyncRow()}, deliverErr: tc.delivery, compensateErr: tc.compensation}
			n, err := (Synchronizer{Gateway: s, Central: s}).SyncBatch(context.Background())
			if n != 0 || err == nil || len(s.reported) != 0 {
				t.Fatalf("count=%d reported=%v err=%v", n, s.reported, err)
			}
		})
	}
	for _, change := range []func(*gatewaycontract.SyncCommand){
		func(r *gatewaycontract.SyncCommand) { r.PortID = nil }, func(r *gatewaycontract.SyncCommand) { r.AckAt = nil },
		func(r *gatewaycontract.SyncCommand) { r.ResultCode = nil }, func(r *gatewaycontract.SyncCommand) { v := int16(254); r.ResultCode = &v; r.Status = "rejected" },
		func(r *gatewaycontract.SyncCommand) { r.Status = "rejected" },
	} {
		row := startSyncRow()
		change(&row)
		s := &startSyncScenario{rows: []gatewaycontract.SyncCommand{row}}
		n, err := (Synchronizer{Gateway: s, Central: s}).SyncBatch(context.Background())
		if n != 0 || err == nil || len(s.calls) != 0 {
			t.Fatalf("invalid evidence advanced: count=%d calls=%v err=%v", n, s.calls, err)
		}
	}
}
