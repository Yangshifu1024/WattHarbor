package charge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
)

// startSyncScenario 是启动同步调用方的测试替身，记录请求及顺序并注入错误；不模拟真实 SQL、设备或资金事务。
type startSyncScenario struct {
	// rows 启动回执 fixture 列表，不表示数据库查询结果已经提交。
	rows []gatewaycontract.SyncCommand
	// deliverErr,compensateErr,markErr 分别控制中央投递、启动补偿和上报标记 fixture 的错误值。
	deliverErr, compensateErr, markErr error
	// filter fixture 记录的命令筛选值，仅供断言。
	filter string
	// results fixture 收到的中央启动请求，用于身份与时间比较。
	results []centralcontract.StartResultRequest
	// compensations,reported fixture 分别记录的补偿身份文本和请求上报命令集合，不代表已保存状态。
	compensations, reported []string
	// calls fixture 按调用顺序记录的动作文本，不创建后台任务。
	calls []string
}

// StartResults 接收但不使用 ctx，记录 filter 并返回配置 rows 和 nil；仅 fixture 读取，不查询网关或标记。
func (s *startSyncScenario) StartResults(_ context.Context, filter string) ([]gatewaycontract.SyncCommand, error) {
	s.filter = filter
	return s.rows, nil
}

// SubmitStartResult 接收但不使用 ctx，记录 r 与中央调用顺序，返回按 deliverErr 配置的接受 Reply 及错误，不执行真实中央事务。
func (s *startSyncScenario) SubmitStartResult(_ context.Context, r centralcontract.StartResultRequest) (centralcontract.ResultReply, error) {
	s.calls = append(s.calls, "central:"+r.CommandID)
	s.results = append(s.results, r)
	return centralcontract.ResultReply{Accepted: s.deliverErr == nil}, s.deliverErr
}

// CompensateStart 记录订单号、命令号和调用顺序，返回预设错误；不发送 STOP 或处理资金。
func (s *startSyncScenario) CompensateStart(_ context.Context, orderNo, commandID string) error {
	s.calls = append(s.calls, "compensate:"+commandID)
	s.compensations = append(s.compensations, orderNo+":"+commandID)
	return s.compensateErr
}

// MarkStartResultsReported 接收但不使用 ctx，记录 ids 及调用顺序，返回输入数和 markErr；不产生持久标记。
func (s *startSyncScenario) MarkStartResultsReported(_ context.Context, ids []string) (int, error) {
	s.calls = append(s.calls, "mark")
	s.reported = append(s.reported, ids...)
	return len(ids), s.markErr
}

// startSyncRow 返回带大整数端口和固定 UTC+8 确认时刻的已成功 START 测试替身，无 IO，不分配真实命令。
func startSyncRow() gatewaycontract.SyncCommand {
	port, code := int64(9007199254740993), int16(0)
	ack := time.Date(2026, 10, 5, 12, 0, 0, 123000000, time.FixedZone("CST", 8*3600))
	return gatewaycontract.SyncCommand{CommandID: "start-1", ChargeOrderID: 9, OrderNo: "C-9", DeviceID: "D-1", PortNo: 1, PortID: &port, Status: "acked", ResultCode: &code, AckAt: &ack}
}

// TestStartSyncOnlyMarksPersistedResultsAndUsesOriginalIdentity 使用测试替身核对两次同步的身份、UTC 转换和中央→标记顺序，不证明真实持久重放。
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

// TestStartSyncConflictCompensatesOnlySuccessfulStartAndNeverMarks 使用测试替身验证仅成功 START 冲突请求补偿且不标记，补偿成功返回 nil；不证明设备断电或退款条件校验。
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
		if n != 0 || len(s.reported) != 0 || (len(s.compensations) == 1) != success || (err == nil) != success {
			t.Fatalf("success=%t count=%d reported=%v compensation=%v err=%v", success, n, s.reported, s.compensations, err)
		}
		if success && (!reflect.DeepEqual(s.calls, []string{"central:start-1", "compensate:start-1"}) || s.compensations[0] != "C-9:start-1") {
			t.Fatalf("wrong compensation identity/order: %v %v", s.calls, s.compensations)
		}
	}
}

// TestStartSyncFailuresKeepUnreportedReceipt 使用测试替身验证中央／补偿失败及缺失、不一致证据不更新标记，保留上述检查；无网络、SQL 或实机。
func TestStartSyncFailuresKeepUnreportedReceipt(t *testing.T) {
	unavailable := errors.New("unavailable")
	for _, tc := range []struct {
		// name 失败场景名，供子测试定位。
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
