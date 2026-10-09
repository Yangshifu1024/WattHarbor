package dc589

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"chargepilot/internal/protocol"
)

// recordingSink 收集适配器产生的事件，供测试断言解码结果与运维审计内容。
type recordingSink struct {
	mu       sync.Mutex       // 保护适配器 goroutine 与测试读取的事件集合
	events   []protocol.Event // 按实际解析顺序保存的协议事件
	recorded chan struct{}    // 每次成功记录的确认，供发送方等待帧已处理
}

// Register 接受 context 下注册信息并返回 nil；测试只断言后续帧事件，不执行持久化。
func (s *recordingSink) Register(context.Context, protocol.Registration) error { return nil }

// Record 在接收者锁内保存 event 并确认已处理，返回 nil；context 不控制此内存操作。
func (s *recordingSink) Record(_ context.Context, event protocol.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	if s.recorded != nil {
		s.recorded <- struct{}{}
	}
	return nil
}

// all 返回接收者事件的独立副本，避免测试读取与适配器追加竞态，无错误或 IO。
func (s *recordingSink) all() []protocol.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Event(nil), s.events...)
}

// findConfigResults 返回接收者事件中配置结果的副本；无结果为空集合，不修改事件。
func (s *recordingSink) findConfigResults() []protocol.Event {
	var out []protocol.Event
	for _, event := range s.all() {
		if event.Type == protocol.ConfigResult {
			out = append(out, event)
		}
	}
	return out
}

// serveWithFrames 通过回环 TCP 建立适配器会话，发送注册及指定帧并返回记录事件的 sink。
// t 报告连接或解析失败，replies 中每帧须产生一条事件；等待记录确认后取消会话并收回读取 goroutine。
func serveWithFrames(t *testing.T, replies []Frame) *recordingSink {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	sink := &recordingSink{recorded: make(chan struct{}, len(replies)+1)}
	adapter := TCPAdapter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		served <- adapter.ServeConn(ctx, conn, sink)
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// 后台读取注册应答及 A6 心跳周期请求，避免下行写入阻塞被测上报帧。
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 512)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	login, err := BuildRegistration(testIdentity())
	if err != nil {
		t.Fatalf("build login: %v", err)
	}
	raw, err := Encode(login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("write login: %v", err)
	}
	for _, reply := range replies {
		raw, err := Encode(reply)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(raw); err != nil {
			t.Fatalf("write reply: %v", err)
		}
	}
	// 固定睡眠无法证明解析已经完成，CPU 忙时会在注册应答之前关闭连接。
	// 每个上报等待 sink 的实际记录确认，不以机器速度决定协议断言是否通过。
	for range replies {
		select {
		case <-sink.recorded:
		case err := <-served:
			t.Fatalf("adapter ended before recording replies: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("adapter did not record the supplied frame")
		}
	}
	cancel()
	_ = conn.Close()
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("the adapter did not release the connection")
	}
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("test downstream reader did not exit")
	}
	return sink
}

// 参数表拒绝应记录具体错误原因，不能归为未知命令。
func TestConfigRejectionIsRecordedRatherThanCalledUnknown(t *testing.T) {
	// 错误码 5 是文档里的「刷卡扣费金额超出范围」。
	sink := serveWithFrames(t, []Frame{{Command: ConfigAck, Data: []byte{5}}})

	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a parameter-table refusal produced no config result; events were %+v", sink.all())
	}
	var rejection protocol.Event
	for _, event := range results {
		if event.ResultCode == 5 {
			rejection = event
		}
	}
	if rejection.Type != protocol.ConfigResult || rejection.ResultCode != 5 {
		t.Fatalf("config results = %+v, want one carrying code 5", results)
	}
	if rejection.Signal != ConfigAck {
		t.Fatalf("signal = %#x, want the 0xC4 that was answered", rejection.Signal)
	}
}

// 设备明确拒绝配置和未收到回复是两种情况；前者需要检查配置参数。
func TestConfigAcceptanceAndRefusalAreDistinguishable(t *testing.T) {
	sink := serveWithFrames(t, []Frame{{Command: ConfigAck, Data: []byte{0}}})
	results := sink.findConfigResults()
	if len(results) == 0 || results[0].ResultCode != 0 {
		t.Fatalf("an accepted table produced %+v, want a zero result code", results)
	}
}

// 验证配置主动上报被识别为正常命令，并保留 RawPayload 原始字节。
func TestConfigReportIsNotTreatedAsUnknown(t *testing.T) {
	table := ConfigTable{RunMode: 0, LocalCoinTime: 10, LocalCardTime: 20, CardAmountCents: 500,
		TemperatureGuard: 0xFF, FloatSeconds: 300, FloatDeciWatts: 100, RemoveSeconds: 60}
	report, err := BuildConfigReport(table)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	sink := serveWithFrames(t, []Frame{report})
	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a configuration report produced no config result; events were %+v", sink.all())
	}
	if results[0].ResultCode != 0 {
		t.Fatalf("result code = %#x, want 0 for a report that decoded", results[0].ResultCode)
	}
	if len(results[0].RawPayload) != len(report.Data) {
		t.Fatalf("raw payload = %d bytes, want the %d that arrived", len(results[0].RawPayload), len(report.Data))
	}
	// 验证无法解析的上报记录明确错误，不伪装为有效配置。
	sink = serveWithFrames(t, []Frame{{Command: ConfigReport, Data: []byte{0, 1, 2}}})
	results = sink.findConfigResults()
	if len(results) == 0 || results[0].ResultCode != 0xFF {
		t.Fatalf("an unreadable report produced %+v, want the undecodable marker", results)
	}
}

// 主板拒绝功率控制时应记录明确的拒绝结果。
func TestPowerControlRefusalIsRecorded(t *testing.T) {
	sink := serveWithFrames(t, []Frame{{Command: cmdPowerControlReply,
		Data: []byte{byte(PowerSet), byte(PowerOpRemove), 0xF1, 0xFF, 0, 0}}})
	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a power-control refusal produced no config result; events were %+v", sink.all())
	}
	if results[0].ResultCode != 1 {
		t.Fatalf("result code = %#x, want the refusal marker", results[0].ResultCode)
	}
}
