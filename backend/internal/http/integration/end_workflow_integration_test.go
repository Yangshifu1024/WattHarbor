//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chargepilot/internal/charging/finish"
	"chargepilot/internal/charging/measurement"
	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/gateway/control"
	gatewayrepo "chargepilot/internal/gateway/repo"
	centralhttp "chargepilot/internal/http/central"
	centralclient "chargepilot/internal/integration/central"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
	"chargepilot/internal/testfixture"
	workercharge "chargepilot/internal/worker/charge"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// endWorkflowLostResponse 表示真实服务已回包后由测试传输层丢弃的确认；不是业务拒绝或自动重试依据。
var endWorkflowLostResponse = errors.New("end workflow response discarded after real HTTP reply")

// endWorkflowFixture 保存两库中已提交的测试记录，以及各自独立的观察连接。
type endWorkflowFixture struct {
	// CentralWriter 真实中央写连接，实际 EndResultService 使用它开启并提交自己的事务。
	CentralWriter *gorm.DB
	// CentralObserver 不共享写连接或外层事务，用来读取中央提交后的状态。
	CentralObserver *gorm.DB
	// GatewayWriter 真实网关写连接，实际注册端点使用它执行 SQL。
	GatewayWriter *gorm.DB
	// GatewayObserver 独立观察网关冻结、占用与处理标记的提交状态。
	GatewayObserver *gorm.DB
	ID              uint64
	// PortID 私有网关端口主键，中央启动凭据引用同一身份。
	PortID uint64
	// EventID 私有结束事件主键，冻结与处理标记始终复用它。
	EventID uint64
	// DeviceID 私有设备业务编号，网关事件 Scope 只收取此设备。
	DeviceID string
	OrderNo  string
	// NewOrderNo 新占用的私有订单号，迟到旧回执不得释放它。
	NewOrderNo string
	// StartCommand 已成功启动命令 UUID，中央和网关保存相同编号。
	StartCommand string
	// StopCommand 停止命令 UUID，同时是中央结束日志与 Outbox 的去重标识。
	StopCommand string
	// StartedAt UTC 毫秒启动时刻，网关 ack 与中央启动时刻保持相同。
	StartedAt time.Time
	// EndedAt UTC 毫秒结束时刻，设备秒数及中央计量由此核对。
	EndedAt time.Time
	// Month 订单完整复合主键的分区日期，清理不能省略它。
	Month string
}

// endWorkflowTrace 记录 HTTP 请求和中央成功回复；互斥锁保护并发读写。
type endWorkflowTrace struct {
	// mu 保护调用顺序和真实回复副本，不串行化生产数据库事务。
	mu sync.Mutex
	// Calls 本轮按发生顺序记录的实际服务标记、HTTP 方法与路径。
	Calls []string
	// Replies 实际中央端点的成功 Reply，包含真实 Apply 返回的重放标志。
	Replies []centralcontract.ResultReply
	// LoseCentral 一次性控制收到中央真实 200 后丢弃响应，不阻止其提交。
	LoseCentral atomic.Bool
}

// endWorkflowTransport 调用真实 HTTP Transport，记录请求和回复，并模拟收到中央成功响应后丢失回包。
type endWorkflowTransport struct {
	// Base 实际 HTTP Transport，负责发出每次请求，不在装饰层重试。
	Base http.RoundTripper
	// Trace 共享请求路径和实际回复。
	Trace *endWorkflowTrace
	// Service 路径来源标记，区分 gateway 和 central 的实际请求。
	Service string
}

// RoundTrip 接收 req，调用 Base 获取真实响应并记录顺序；中央成功时保留真实 Reply，必要时丢弃已收到回包并返回故障。
// 读取后恢复响应正文供专用客户端自己验证，不构造成功 Reply，不更换请求编号或重试。
func (p endWorkflowTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p.Trace.mu.Lock()
	p.Trace.Calls = append(p.Trace.Calls, p.Service+" "+req.Method+" "+req.URL.Path)
	p.Trace.mu.Unlock()
	response, err := p.Base.RoundTrip(req)
	if err != nil || p.Service != "central" || response.StatusCode != http.StatusOK {
		return response, err
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Code int                         `json:"code"`
		Data centralcontract.ResultReply `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	p.Trace.mu.Lock()
	p.Trace.Replies = append(p.Trace.Replies, envelope.Data)
	p.Trace.mu.Unlock()
	if p.Trace.LoseCentral.Swap(false) {
		return nil, endWorkflowLostResponse
	}
	response.Body = io.NopCloser(strings.NewReader(string(body)))
	return response, nil
}

// reset 清空上一轮请求和回复记录，保留故障开关。
func (p *endWorkflowTrace) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls, p.Replies = nil, nil
}

// assert 核对本轮请求顺序和中央成功回复；reply 为 nil 时，要求未记录成功回复。
func (p *endWorkflowTrace) assert(t *testing.T, f *endWorkflowFixture, release, mark bool, reply *centralcontract.ResultReply) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	want := []string{
		"gateway GET " + gatewaycontract.EndEventsPath,
		"gateway GET " + strings.Replace(gatewaycontract.ChargeCommandRoute, ":order_id", strconv.FormatUint(f.ID, 10), 1),
		"gateway GET " + strings.Replace(gatewaycontract.MeterSamplesRoute, ":device_id", f.DeviceID, 1),
		"gateway POST " + gatewaycontract.FreezeEndDeliveryPath,
		"central POST " + strings.Replace(centralcontract.EndResultRoute, ":order_no", f.OrderNo, 1),
	}
	if release {
		want = append(want, "gateway POST "+gatewaycontract.ReleasePortPath)
	}
	if mark {
		want = append(want, "gateway POST "+gatewaycontract.MarkDeviceEventsPath)
	}
	if fmt.Sprint(p.Calls) != fmt.Sprint(want) {
		t.Fatalf("actual HTTP ordering = %v, want %v", p.Calls, want)
	}
	if reply == nil {
		if len(p.Replies) != 0 {
			t.Fatalf("unexpected real successful central reply: %+v", p.Replies)
		}
	} else if len(p.Replies) != 1 || p.Replies[0] != *reply {
		t.Fatalf("actual central replies = %+v, want %+v", p.Replies, *reply)
	}
}

// newEndWorkflowFixture 用两个写池和两个观察池创建并提交私有充电记录，先注册精确清理。
// 所有真实库必须配置；只创建订单、启动凭据、占用及网关事件／命令／端口，不访问支付或执行资金算法。
func newEndWorkflowFixture(t *testing.T) *endWorkflowFixture {
	t.Helper()
	id := testfixture.NewID()
	device := "EW" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ended := time.Now().UTC().Truncate(time.Millisecond)
	f := &endWorkflowFixture{
		CentralWriter: cardRouteDB(t, "TEST_MYSQL_URL"), CentralObserver: cardRouteDB(t, "TEST_MYSQL_URL"),
		GatewayWriter: cardRouteDB(t, "DATABASE_URL_GATEWAY"), GatewayObserver: cardRouteDB(t, "DATABASE_URL_GATEWAY"),
		ID: id, PortID: id + 1, EventID: id + 3, DeviceID: device,
		OrderNo: "C-EW-" + uuid.NewString(), NewOrderNo: "C-EW-" + uuid.NewString(),
		StartCommand: uuid.NewString(), StopCommand: uuid.NewString(),
		StartedAt: ended.Add(-time.Hour), EndedAt: ended,
		Month: ended.Format("2006-01") + "-01",
	}
	for _, pair := range [][2]*gorm.DB{{f.CentralWriter, f.CentralObserver}, {f.GatewayWriter, f.GatewayObserver}} {
		var connections [2]uint64
		for index, orm := range pair {
			pool, err := orm.DB()
			if err != nil {
				t.Fatal(err)
			}
			pool.SetMaxOpenConns(1)
			pool.SetMaxIdleConns(1)
			if err := orm.Raw("SELECT CONNECTION_ID()").Scan(&connections[index]).Error; err != nil {
				t.Fatal(err)
			}
		}
		if connections[0] == connections[1] {
			t.Fatal("writer and observer must be different physical MySQL connections")
		}
	}
	t.Cleanup(func() { f.cleanup(t) })
	if err := f.CentralWriter.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("charge_order").Create(map[string]any{"id": f.ID, "order_no": f.OrderNo, "user_id": f.ID, "device_id": f.DeviceID, "port_no": 1, "status": "charging", "payment_status": "paid", "started_at": f.StartedAt, "created_month": f.Month}).Error; err != nil {
			return err
		}
		if err := tx.Table("charge_start_receipt").Create(map[string]any{"charge_order_id": f.ID, "command_id": f.StartCommand, "success": true, "port_id": f.PortID}).Error; err != nil {
			return err
		}
		return tx.Table("active_port_charge").Create(map[string]any{"port_id": f.PortID, "device_id": f.DeviceID, "port_no": 1, "charge_order_id": f.ID, "user_id": f.ID, "started_at": f.StartedAt}).Error
	}); err != nil {
		t.Fatal(err)
	}
	event := protocol.Event{EventID: uuid.NewString(), Protocol: "dc589", Type: protocol.ChargeEnd, DeviceID: f.DeviceID, Port: 1, ConsumerType: 2, OrderNumber: strconv.FormatUint(f.ID, 10), StartedAt: f.StartedAt, EndedAt: f.EndedAt, ReceivedAt: f.EndedAt, ChargedSeconds: 3600, EnergyMilliKWh: 1200, StopReason: 7}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	// 同库另一私有设备的有效结束事件是隔离检查记录；若 Scope 失效，真实 EndSynchronizer 会多发 by-order 请求而破坏路径断言。
	foreign := event
	foreign.EventID, foreign.DeviceID, foreign.OrderNumber = uuid.NewString(), f.DeviceID+"x", strconv.FormatUint(f.ID+11, 10)
	foreignRaw, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.GatewayWriter.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("device_port").Create(map[string]any{"id": f.PortID, "device_id": f.DeviceID, "port_no": 1, "port_code": f.DeviceID + "-01", "status": "charging", "current_order_id": f.OrderNo}).Error; err != nil {
			return err
		}
		if err := tx.Table("charge_command").Create(map[string]any{"command_id": f.StartCommand, "stop_command_id": f.StopCommand, "charge_order_id": f.ID, "payment_order_id": f.ID, "order_no": f.OrderNo, "user_id": f.ID, "device_id": f.DeviceID, "port_no": 1, "port_code": f.DeviceID + "-01", "port_id": f.PortID, "owns_port": true, "status": "acked", "result_code": 0, "ack_at": f.StartedAt}).Error; err != nil {
			return err
		}
		if err := tx.Table("device_event").Create(map[string]any{"id": f.EventID, "event_key": event.EventID, "protocol_name": "dc589", "device_id": f.DeviceID, "event_type": "charge_end", "port_no": 1, "event_json": string(raw), "received_at": f.EndedAt}).Error; err != nil {
			return err
		}
		return tx.Table("device_event").Create(map[string]any{"id": f.ID + 4, "event_key": foreign.EventID, "protocol_name": "dc589", "device_id": foreign.DeviceID, "event_type": "charge_end", "port_no": 1, "event_json": string(foreignRaw), "received_at": f.EndedAt}).Error
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// cleanup 按测试记录完整主键清理，逐表检查没有残留；Outbox 使用 DeleteOutbox。
func (f *endWorkflowFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	central, gateway := f.CentralWriter.WithContext(ctx), f.GatewayWriter.WithContext(ctx)
	if err := testfixture.DeleteOutbox(central, "event_id=?", f.StopCommand); err != nil {
		t.Errorf("cleanup outbox: %v", err)
	}
	var logIDs []uint64
	if err := central.Table("charge_event_log").Where("event_id=?", f.StopCommand).Order("id").Pluck("id", &logIDs).Error; err != nil {
		t.Errorf("list private event logs: %v", err)
	}
	for _, id := range logIDs {
		if err := central.Table("charge_event_log").Where("id=? AND event_id=?", id, f.StopCommand).Delete(map[string]any{}).Error; err != nil {
			t.Errorf("cleanup private log: %v", err)
		}
	}
	for _, item := range []struct {
		db    *gorm.DB
		table string
		// where 完整私有主键与必要归属条件。
		where string
		args  []any
	}{
		{central, "charge_billing_job", "charge_order_id=?", []any{f.ID}},
		{central, "charge_end_receipt", "charge_order_id=?", []any{f.ID}},
		{central, "charge_start_receipt", "charge_order_id=?", []any{f.ID}},
		{central, "active_port_charge", "port_id=? AND device_id=?", []any{f.PortID, f.DeviceID}},
		{central, "charge_order", "id=? AND created_month=?", []any{f.ID, f.Month}},
		{central, "charge_order", "id=? AND created_month=?", []any{f.ID + 10, f.Month}},
		{gateway, "charge_end_delivery", "device_event_id=?", []any{f.EventID}},
		{gateway, "device_event", "id=? AND device_id=?", []any{f.EventID, f.DeviceID}},
		{gateway, "device_event", "id=? AND device_id=?", []any{f.ID + 2, f.DeviceID}},
		{gateway, "device_event", "id=? AND device_id=?", []any{f.ID + 4, f.DeviceID + "x"}},
		{gateway, "charge_command", "command_id=?", []any{f.StartCommand}},
		{gateway, "device_port", "id=? AND device_id=?", []any{f.PortID, f.DeviceID}},
	} {
		if err := item.db.Table(item.table).Where(item.where, item.args...).Delete(map[string]any{}).Error; err != nil {
			t.Errorf("cleanup %s: %v", item.table, err)
		}
		var remaining int64
		if err := item.db.Table(item.table).Where(item.where, item.args...).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Errorf("private %s residue=%d: %v", item.table, remaining, err)
		}
	}
	for _, table := range []string{"charge_event_log", "event_outbox"} {
		var remaining int64
		if err := central.Table(table).Where("event_id=?", f.StopCommand).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Errorf("private %s residue=%d: %v", table, remaining, err)
		}
	}
}

// servers 真实注册两侧端点并返回 URL 和两种前置故障开关；不替换业务 Service。
// Scope 只为 device_event 查询附加私有设备筛选，保留真实 SQL、事务与其它表访问。
func (f *endWorkflowFixture) servers(t *testing.T) (string, string, *atomic.Bool, *atomic.Bool) {
	t.Helper()
	denyCentral, denyRelease := &atomic.Bool{}, &atomic.Bool{}
	centralRouter := httpapi.NewRouter()
	centralRouter.Use(func(c *gin.Context) {
		if denyCentral.Load() {
			httpapi.Write(c, 503, 5001, "injected pre-apply unavailability", nil)
			c.Abort()
		}
	})
	(centralhttp.EndResultAPI{Service: finish.EndResultService{DB: f.CentralWriter}, ServiceToken: "end-workflow-token"}).Register(centralRouter)
	centralServer := httptest.NewServer(centralRouter)
	t.Cleanup(centralServer.Close)
	privateEvents := f.GatewayWriter.Scopes(func(q *gorm.DB) *gorm.DB {
		var eventTable bool
		switch q.Statement.Model.(type) {
		case *gatewayrepo.SyncEventRow:
			eventTable = true
		}
		switch q.Statement.Dest.(type) {
		case *[]gatewayrepo.SyncEventRow:
			eventTable = true
		}
		if eventTable || q.Statement.Table == "device_event" {
			return q.Where("device_event.device_id=?", f.DeviceID)
		}
		return q
	}).Session(&gorm.Session{})
	gatewayRouter := httpapi.NewRouter()
	gatewayRouter.Use(func(c *gin.Context) {
		if denyRelease.Load() && c.Request.URL.Path == gatewaycontract.ReleasePortPath {
			httpapi.Write(c, 503, 5003, "injected pre-release unavailability", nil)
			c.Abort()
		}
	})
	(control.TelemetryAPI{DB: privateEvents, ServiceToken: "end-workflow-token"}).Register(gatewayRouter)
	gatewayServer := httptest.NewServer(gatewayRouter)
	t.Cleanup(gatewayServer.Close)
	return centralServer.URL, gatewayServer.URL, denyCentral, denyRelease
}

// synchronizer 创建新的中央、网关客户端和结束同步器；客户端连接池由测试关闭。
func (f *endWorkflowFixture) synchronizer(t *testing.T, trace *endWorkflowTrace, centralURL, gatewayURL string) workercharge.EndSynchronizer {
	t.Helper()
	centralTransport := http.DefaultTransport.(*http.Transport).Clone()
	gatewayTransport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(centralTransport.CloseIdleConnections)
	t.Cleanup(gatewayTransport.CloseIdleConnections)
	central, err := centralclient.NewClient(serviceclient.Client{HTTP: &http.Client{Transport: endWorkflowTransport{Base: centralTransport, Trace: trace, Service: "central"}, Timeout: 10 * time.Second}}, centralURL, "end-workflow-token")
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: &http.Client{Transport: endWorkflowTransport{Base: gatewayTransport, Trace: trace, Service: "gateway"}, Timeout: 10 * time.Second}}, gatewayURL, "end-workflow-token")
	if err != nil {
		t.Fatal(err)
	}
	return workercharge.EndSynchronizer{Gateway: gateway, Central: central}
}

// assertCentral 从独立连接核对订单结束状态、四项结束记录及端口占用释放。
// 本测试不执行计费、退款或 Outbox 发布。
func (f *endWorkflowFixture) assertCentral(t *testing.T, ended bool) {
	t.Helper()
	var order struct {
		// Status 独立连接看到的订单状态。
		Status string
		// EndedAt 结束时刻，尚未结束时须为 NULL。
		EndedAt sql.NullTime
		// ChargedKWh 十进制千瓦时文本，真实 Apply 从 Wh 换算。
		ChargedKWh sql.NullString `gorm:"column:charged_kwh"`
		// ChargedSeconds 充电时长秒，尚未结束时须为 NULL。
		ChargedSeconds sql.NullInt64
	}
	if err := f.CentralObserver.Table("charge_order").Where("id=? AND created_month=?", f.ID, f.Month).Take(&order).Error; err != nil {
		t.Fatal(err)
	}
	want := int64(0)
	if ended {
		want = 1
		if order.Status != "completed" || !order.EndedAt.Valid || !order.EndedAt.Time.Equal(f.EndedAt) || !order.ChargedKWh.Valid || order.ChargedKWh.String != "1.2000" || !order.ChargedSeconds.Valid || order.ChargedSeconds.Int64 != 3600 {
			t.Fatalf("committed central order = %+v", order)
		}
	} else if order.Status != "charging" || order.EndedAt.Valid || order.ChargedKWh.Valid || order.ChargedSeconds.Valid {
		t.Fatalf("central advanced without successful Apply: %+v", order)
	}
	for _, table := range []string{"charge_end_receipt", "charge_billing_job", "charge_event_log", "event_outbox"} {
		query := f.CentralObserver.Table(table).Where("charge_order_id=?", f.ID)
		if table == "event_outbox" {
			query = f.CentralObserver.Table(table).Where("event_id=?", f.StopCommand)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil || count != want {
			t.Fatalf("central %s count=%d want=%d: %v", table, count, want, err)
		}
	}
	var occupied int64
	if err := f.CentralObserver.Table("active_port_charge").Where("port_id=? AND charge_order_id=?", f.PortID, f.ID).Count(&occupied).Error; err != nil || occupied != 1-want {
		t.Fatalf("old central occupancy=%d: %v", occupied, err)
	}
	if ended {
		var receipt struct {
			// StopCommandID 实际结束凭据的停机命令编号，不允许重放换号。
			StopCommandID string
			// MeterJSON 从数据库读取 Apply 保存的计量，另行核对 HTTP 回复。
			MeterJSON string
		}
		if err := f.CentralObserver.Table("charge_end_receipt").Where("charge_order_id=?", f.ID).Take(&receipt).Error; err != nil || receipt.StopCommandID != f.StopCommand {
			t.Fatalf("central original receipt=%+v: %v", receipt, err)
		}
		var expected centralcontract.EndResultRequest
		if err := json.Unmarshal([]byte(receipt.MeterJSON), &expected.Meter); err != nil || expected.Meter.ChargedWh != 1200 || expected.Meter.ChargedSeconds != 3600 || expected.Meter.StopReason != 7 || !expected.Meter.EndedAt.Equal(f.EndedAt) || len(expected.Meter.Segments) != 0 {
			t.Fatalf("central original meter=%+v: %v", expected.Meter, err)
		}
		var outbox struct {
			// Stream 结束任务目标 stream，核对结束事务写入队列。
			Stream string
			// Status 任务发布前状态；本回归不运行发布器。
			Status string
			// EnvelopeJSON 与冻结请求相同一标识的完整业务载荷。
			EnvelopeJSON string
		}
		if err := f.CentralObserver.Table("event_outbox").Where("event_id=?", f.StopCommand).Take(&outbox).Error; err != nil || outbox.Stream != "charge_ended_stream" || outbox.Status != "pending" {
			t.Fatalf("central original outbox=%+v: %v", outbox, err)
		}
		var envelope centralcontract.EndResultRequest
		if err := json.Unmarshal([]byte(outbox.EnvelopeJSON), &envelope); err != nil || envelope.ChargeOrderID != f.ID || envelope.PortID != f.PortID || envelope.StopCommandID != f.StopCommand || envelope.StartCommandID != f.StartCommand || envelope.OrderNo != f.OrderNo || envelope.DeviceID != f.DeviceID || !sameCardJSON(t, receipt.MeterJSON, endWorkflowMeterJSON(t, envelope)) {
			t.Fatalf("central original outbox identity=%+v: %v", envelope, err)
		}
		var job struct {
			// Status 实际结束事务创建的待计费状态，尚未执行结算。
			Status string
			// Attempts 任务初始尝试数，回执重放不应更新它。
			Attempts uint32
		}
		if err := f.CentralObserver.Table("charge_billing_job").Where("charge_order_id=?", f.ID).Take(&job).Error; err != nil || job.Status != "pending" || job.Attempts != 0 {
			t.Fatalf("central billing queue=%+v: %v", job, err)
		}
	}
}

// endWorkflowMeterJSON 将结束计量编码为 JSON，用于比较数据库保存值。
func endWorkflowMeterJSON(t *testing.T, result centralcontract.EndResultRequest) string {
	t.Helper()
	raw, err := json.Marshal(result.Meter)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// assertGateway 从独立连接核对端口归属、事件处理标记和唯一结束记录，返回保存的 JSON。
// 无 owner 时要求 idle/NULL，否则要求 charging 且订单号一致。
func (f *endWorkflowFixture) assertGateway(t *testing.T, owner string, processed bool) string {
	t.Helper()
	var port gatewayrepo.SyncPortRow
	if err := f.GatewayObserver.Where("id=?", f.PortID).Take(&port).Error; err != nil {
		t.Fatal(err)
	}
	if owner == "" {
		if port.Status != "idle" || port.CurrentOrderID.Valid {
			t.Fatalf("gateway not released: %+v", port)
		}
	} else if port.Status != "charging" || !port.CurrentOrderID.Valid || port.CurrentOrderID.String != owner {
		t.Fatalf("gateway owner changed: %+v want=%s", port, owner)
	}
	var event gatewayrepo.SyncEventRow
	if err := f.GatewayObserver.Where("id=? AND device_id=?", f.EventID, f.DeviceID).Take(&event).Error; err != nil || event.ProcessedAt.Valid != processed {
		t.Fatalf("event processing=%+v want=%v: %v", event, processed, err)
	}
	var foreign gatewayrepo.SyncEventRow
	if err := f.GatewayObserver.Where("id=? AND device_id=?", f.ID+4, f.DeviceID+"x").Take(&foreign).Error; err != nil || foreign.ProcessedAt.Valid {
		t.Fatalf("Scope modified another private device event=%+v: %v", foreign, err)
	}
	var frozen struct {
		// ChargeOrderID 唯一冻结记录引用的中央订单大整数。
		ChargeOrderID uint64
		// PayloadJSON 首次冻结的完整结束请求，后续候选不能替换它。
		PayloadJSON string
	}
	if err := f.GatewayObserver.Table("charge_end_delivery").Where("device_event_id=?", f.EventID).Take(&frozen).Error; err != nil || frozen.ChargeOrderID != f.ID {
		t.Fatalf("frozen delivery=%+v: %v", frozen, err)
	}
	var payload centralcontract.EndResultRequest
	if err := json.Unmarshal([]byte(frozen.PayloadJSON), &payload); err != nil || payload.ChargeOrderID != f.ID || payload.PortID != f.PortID || payload.OrderNo != f.OrderNo || payload.StartCommandID != f.StartCommand || payload.StopCommandID != f.StopCommand || payload.Meter.ChargedWh != 1200 || payload.Meter.ChargedSeconds != 3600 || !payload.Meter.EndedAt.Equal(f.EndedAt) || len(payload.Meter.Segments) != 0 {
		t.Fatalf("frozen exact identity=%+v: %v", payload, err)
	}
	return frozen.PayloadJSON
}

// TestEndWorkflowRebuildReplaysCommittedResultBeforeRelease 使用真实两服务与独立两库观察，验证失败不提前释放及响应丢失后重放。
// 提交后的回包丢失由网络装饰层制造；不替换 Apply、资金结果或专用客户端验证，全部提交夹具精确清理。
func TestEndWorkflowRebuildReplaysCommittedResultBeforeRelease(t *testing.T) {
	f := newEndWorkflowFixture(t)
	trace := &endWorkflowTrace{}
	centralURL, gatewayURL, denyCentral, _ := f.servers(t)
	syncer := f.synchronizer(t, trace, centralURL, gatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	denyCentral.Store(true)
	if count, err := syncer.SyncBatch(ctx); count != 0 || err == nil {
		t.Fatalf("pre-Apply denial count=%d err=%v", count, err)
	}
	f.assertCentral(t, false)
	frozen := f.assertGateway(t, f.OrderNo, false)
	trace.assert(t, f, false, false, nil)
	denyCentral.Store(false)
	trace.reset()
	trace.LoseCentral.Store(true)
	if count, err := syncer.SyncBatch(ctx); count != 0 || !errors.Is(err, endWorkflowLostResponse) {
		t.Fatalf("lost committed response count=%d err=%v", count, err)
	}
	trace.assert(t, f, false, false, &centralcontract.ResultReply{Accepted: true, Replayed: false})
	f.assertCentral(t, true)
	if current := f.assertGateway(t, f.OrderNo, false); !sameCardJSON(t, current, frozen) {
		t.Fatal("lost confirmation changed frozen evidence")
	}
	// 首次冻结以后才落库的较早心跳位于计量查询窗口；重建的 worker 会生成不同分段候选，仍返回首次保存的结束请求。
	heartbeat := protocol.Event{EventID: uuid.NewString(), Protocol: "dc589", Type: protocol.Heartbeat, DeviceID: f.DeviceID, ReceivedAt: f.StartedAt.Add(30 * time.Minute), ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedSeconds: 1800, ChargedMWh: 600000, PowerDeciWatts: 12000}}}
	raw, err := json.Marshal(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.GatewayWriter.Table("device_event").Create(map[string]any{"id": f.ID + 2, "event_key": heartbeat.EventID, "protocol_name": "dc589", "device_id": f.DeviceID, "event_type": "heartbeat", "event_json": string(raw), "received_at": heartbeat.ReceivedAt}).Error; err != nil {
		t.Fatal(err)
	}
	terminal := protocol.Event{DeviceID: f.DeviceID, Port: 1, StartedAt: f.StartedAt, EndedAt: f.EndedAt, ChargedSeconds: 3600, EnergyMilliKWh: 1200}
	if len(measurement.MeasuredSegmentsOf(f.StartedAt, terminal, []protocol.Event{heartbeat})) == 0 {
		t.Fatal("late heartbeat must change the newly computed candidate segments")
	}
	trace.reset()
	rebuilt := f.synchronizer(t, trace, centralURL, gatewayURL)
	if count, err := rebuilt.SyncBatch(ctx); count != 1 || err != nil {
		t.Fatalf("rebuilt replay count=%d err=%v", count, err)
	}
	trace.assert(t, f, true, true, &centralcontract.ResultReply{Accepted: true, Replayed: true})
	f.assertCentral(t, true)
	if current := f.assertGateway(t, "", true); !sameCardJSON(t, current, frozen) {
		t.Fatal("new client/synchronizer or late heartbeat replaced original freeze")
	}
	trace.reset()
	if count, err := rebuilt.SyncBatch(ctx); count != 0 || err != nil {
		t.Fatalf("processed event replay count=%d err=%v", count, err)
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.Calls) != 1 || trace.Calls[0] != "gateway GET "+gatewaycontract.EndEventsPath || len(trace.Replies) != 0 {
		t.Fatalf("processed event caused further calls: %v %+v", trace.Calls, trace.Replies)
	}
}

// TestEndWorkflowLateReplayCannotReleaseNewOwner 使用真实结束确认后网关释放前的可控 503，保留事件入口。
// 新订单重新占用同一端口后，冻结重放在真实 release 端点得到 409；中央和网关新占用均不得被迟到回执修改。
func TestEndWorkflowLateReplayCannotReleaseNewOwner(t *testing.T) {
	f := newEndWorkflowFixture(t)
	trace := &endWorkflowTrace{}
	centralURL, gatewayURL, _, denyRelease := f.servers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	denyRelease.Store(true)
	if count, err := f.synchronizer(t, trace, centralURL, gatewayURL).SyncBatch(ctx); count != 0 || err == nil {
		t.Fatalf("release unavailability count=%d err=%v", count, err)
	}
	trace.assert(t, f, true, false, &centralcontract.ResultReply{Accepted: true, Replayed: false})
	f.assertCentral(t, true)
	frozen := f.assertGateway(t, f.OrderNo, false)
	if err := f.CentralWriter.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("charge_order").Create(map[string]any{"id": f.ID + 10, "order_no": f.NewOrderNo, "user_id": f.ID, "device_id": f.DeviceID, "port_no": 1, "status": "charging", "started_at": f.EndedAt, "created_month": f.Month}).Error; err != nil {
			return err
		}
		return tx.Table("active_port_charge").Create(map[string]any{"port_id": f.PortID, "device_id": f.DeviceID, "port_no": 1, "charge_order_id": f.ID + 10, "user_id": f.ID, "started_at": f.EndedAt}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if result := f.GatewayWriter.Table("device_port").Where("id=? AND device_id=?", f.PortID, f.DeviceID).Updates(map[string]any{"status": "charging", "current_order_id": f.NewOrderNo}); result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("private new gateway occupancy: %d %v", result.RowsAffected, result.Error)
	}
	denyRelease.Store(false)
	trace.reset()
	count, err := f.synchronizer(t, trace, centralURL, gatewayURL).SyncBatch(ctx)
	var rejection *serviceclient.HTTPError
	if current := f.assertGateway(t, f.NewOrderNo, false); !sameCardJSON(t, current, frozen) {
		t.Fatal("late old receipt changed frozen evidence")
	}
	if count != 0 || !errors.As(err, &rejection) || rejection.StatusCode != http.StatusConflict || rejection.Path != gatewaycontract.ReleasePortPath {
		t.Fatalf("late old release count=%d err=%v", count, err)
	}
	trace.assert(t, f, true, false, &centralcontract.ResultReply{Accepted: true, Replayed: true})
	f.assertCentral(t, true)
	var occupied int64
	if err := f.CentralObserver.Table("active_port_charge").Where("port_id=? AND charge_order_id=? AND device_id=?", f.PortID, f.ID+10, f.DeviceID).Count(&occupied).Error; err != nil || occupied != 1 {
		t.Fatalf("new central owner lost: %d %v", occupied, err)
	}
	var status string
	if err := f.CentralObserver.Table("charge_order").Where("id=? AND created_month=?", f.ID+10, f.Month).Pluck("status", &status).Error; err != nil || status != "charging" {
		t.Fatalf("new central order changed: %s %v", status, err)
	}
}
