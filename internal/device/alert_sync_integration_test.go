//go:build integration

package device_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/control"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// alertReplyLossTransport 只在真实网关首次标记已提交后丢弃响应，后续使用原Transport，不修改数据库。
type alertReplyLossTransport struct {
	inner http.RoundTripper // 受测试服务管理的实际Transport
	lost  bool              // 首次标记回复已丢弃
}

// TestConcurrentAlertSyncKeepsOneActiveEpisode 两条独立central连接在首次缺行当前读后同时插入，验证RR间隙锁仲裁后只一条活动告警。
// 冲突事务保留错误交给原事件下一轮重扫，不内部重试；两真库实际提交并只清理本测试身份。
func TestConcurrentAlertSyncKeepsOneActiveEpisode(t *testing.T) {
	setup, first, second := importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL")
	gw := importDB(t, "DATABASE_URL_GATEWAY")
	board, key := "AC"+uuid.NewString(), uuid.NewString()
	at := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		for _, item := range []struct {
			// db 该清理项所属真库的 GORM 句柄；告警记录位于 central，原协议事件位于 gateway，池由测试管理。
			db *gorm.DB
			// table,where table 为本测试清理或计数的目标表；where 为限定本测试记录身份的参数化条件，保持两者原顺序。
			table, where string
			// arg 当前条件占位符对应的订单、设备或用户身份值，不转换其数值类型。
			arg any
		}{{setup, "alert_event", "device_id=?", board}, {gw, "device_event", "event_key=?", key}} {
			if err := item.db.Table(item.table).Where(item.where, item.arg).Delete(nil).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := item.db.Table(item.table).Where(item.where, item.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("remaining=%s %d %v", item.table, n, err)
			}
		}
	})
	randomID := uuid.New()
	eventID := binary.LittleEndian.Uint64(randomID[:8])%(1<<54) + (1 << 54)
	event := protocol.Event{Protocol: "dc589", DeviceID: board, Type: protocol.Fault, Port: 1, FaultCode: 3, ReceivedAt: at}
	payload, _ := json.Marshal(event)
	if err := gw.Table("device_event").Create(map[string]any{"id": eventID, "event_key": key, "protocol_name": "dc589", "device_id": board, "event_type": "fault", "port_no": 1, "event_json": string(payload), "received_at": at}).Error; err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	(control.TelemetryAPI{DB: gw, ServiceToken: "alert-concurrent-token"}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "alert-concurrent-token")
	if err != nil {
		t.Fatal(err)
	}
	ownerCallback := "alert_gateway_owner_" + uuid.NewString()
	if err := gw.Callback().Query().Before("gorm:query").Register(ownerCallback, func(q *gorm.DB) {
		if q.Statement.Table == "device_event" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "device_id", Value: board}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gw.Callback().Query().Remove(ownerCallback) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	release := make(chan struct{})
	var pass sync.Once
	allow := func() { pass.Do(func() { close(release) }) }
	defer allow()
	ready := []chan struct{}{make(chan struct{}), make(chan struct{})}
	callback := "alert_concurrent_" + uuid.NewString()
	for i, db := range []*gorm.DB{first, second} {
		var once sync.Once
		if err := db.Callback().Query().Before("gorm:query").Register(callback+"_scope", func(q *gorm.DB) {
			if q.Statement.Table == "alert_event" {
				q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "device_id", Value: board}}})
			}
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
			_, one := q.Statement.Clauses["LIMIT"]
			if q.Statement.Table == "alert_event" && one {
				once.Do(func() {
					close(ready[i])
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Callback().Query().Remove(callback); db.Callback().Query().Remove(callback + "_scope") })
	}
	// result 将真实告警同步用例的新增数量与错误传回测试控制方。
	type result struct {
		// raised 真实告警同步调用新增的告警数量，用于核对并发重复事件不重复生成告警。
		raised int
		// err 当前调用返回的执行错误；nil 表示该调用成功，不替代持久状态核验。
		err error
	}
	call := func(db *gorm.DB) chan result {
		ch := make(chan result, 1)
		go func() { n, err := (device.AlertSyncService{DB: db, Gateway: client}).Run(ctx); ch <- result{n, err} }()
		return ch
	}
	a := call(first)
	select {
	case <-ready[0]:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	b := call(second)
	select {
	case <-ready[1]:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	allow()
	one, two := <-a, <-b
	if one.raised+two.raised != 1 {
		t.Fatalf("first=%+v second=%+v", one, two)
	}
	var n int64
	if err := setup.Table("alert_event").Where("device_id=? AND status='active'", board).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("active=%d %v", n, err)
	}
	if _, err := (device.AlertSyncService{DB: first, Gateway: client}).Run(ctx); err != nil {
		t.Fatalf("original scan recovery=%v", err)
	}
}

// RoundTrip 执行r的实际请求后仅一次丢弃原标记响应，返回传输未知错误；不重试，不改变请求主键。
func (s *alertReplyLossTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := s.inner.RoundTrip(r)
	if err == nil && !s.lost && r.URL.Path == "/api/v1/internal/device-faults/mark-processed" {
		s.lost = true
		response.Body.Close()
		return nil, errors.New("mark response lost after gateway commit")
	}
	return response, err
}

// TestAlertSyncUsesRegisteredGatewayAndPersistentWatermark 验证两真库及真实注册gateway：回复丢失重扫、持续／乱序与完整恢复。
// 源事件为协议fixture且全部回滚，不代表实机故障；两端仍使用实际表访问及具名客户端。
func TestAlertSyncUsesRegisteredGatewayAndPersistentWatermark(t *testing.T) {
	central, gateway := importDB(t, "TEST_MYSQL_URL").Begin(), importDB(t, "DATABASE_URL_GATEWAY").Begin()
	defer central.Rollback()
	defer gateway.Rollback()
	if central.Error != nil || gateway.Error != nil {
		t.Fatal(central.Error, gateway.Error)
	}
	board := "AS" + uuid.NewString()
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	// 共享测试库中的其他业务fixture不属于本轮恢复扫描，持续限定原测试设备。
	key := "alert_scope_" + uuid.NewString()
	if err := central.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "alert_event" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "device_id", Value: board}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer central.Callback().Query().Remove(key)
	if err := gateway.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "device_event" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "device_id", Value: board}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer gateway.Callback().Query().Remove(key)
	router := httpapi.NewRouter()
	(control.TelemetryAPI{DB: gateway, ServiceToken: "alert-test-token"}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	httpClient := *server.Client()
	httpClient.Transport = &alertReplyLossTransport{inner: httpClient.Transport}
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: &httpClient}, server.URL, "alert-test-token")
	if err != nil {
		t.Fatal(err)
	}
	s := device.AlertSyncService{DB: central, Gateway: client}
	randomID := uuid.New()
	eventID := binary.LittleEndian.Uint64(randomID[:8])%(1<<54) + (1 << 54)
	seed := func(eventKey string, e protocol.Event) {
		eventID++
		t.Helper()
		payload, _ := json.Marshal(e)
		if err := gateway.Table("device_event").Create(map[string]any{"id": eventID, "event_key": eventKey, "protocol_name": e.Protocol, "device_id": board, "event_type": string(e.Type), "port_no": e.Port, "event_json": string(payload), "received_at": e.ReceivedAt}).Error; err != nil {
			t.Fatal(err)
		}
	}
	fault := func(when time.Time) protocol.Event {
		return protocol.Event{Protocol: "dc589", DeviceID: board, Type: protocol.Fault, Port: 1, FaultCode: 3, ReceivedAt: when}
	}
	first, next, old := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seed(first, fault(at))
	raised, err := s.Run(ctx)
	if err == nil || raised != 1 {
		t.Fatalf("first=%d %v", raised, err)
	}
	var alert device.DeviceAlert
	read := func() {
		t.Helper()
		alert = device.DeviceAlert{}
		if err := central.Table("alert_event").Where("device_id=?", board).Order("id DESC").Take(&alert).Error; err != nil {
			t.Fatal(err)
		}
	}
	read()
	if alert.Status != "active" || alert.EventID != first {
		t.Fatalf("initial=%+v", alert)
	}
	// 原标记响应虽然丢失，下一轮实际扫描为空；确认提交与unknown并存，无重复告警。
	raised, err = s.Run(ctx)
	if err != nil || raised != 0 {
		t.Fatalf("lost response recovery=%d %v", raised, err)
	}
	// 模拟原标记回复丢失后的同一原证据重扫，不产生新告警。
	if err := gateway.Table("device_event").Where("event_key=?", first).Update("processed_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	raised, err = s.Run(ctx)
	if err != nil || raised != 0 {
		t.Fatalf("replay=%d %v", raised, err)
	}
	seed(next, fault(at.Add(10*time.Second)))
	raised, err = s.Run(ctx)
	if err != nil || raised != 0 {
		t.Fatalf("ongoing=%d %v", raised, err)
	}
	read()
	if alert.EventID != next || !alert.CreatedAt.Equal(at) {
		t.Fatalf("latest=%+v", alert)
	}
	seed(old, fault(at.Add(5*time.Second)))
	raised, err = s.Run(ctx)
	if err != nil || raised != 0 {
		t.Fatalf("out of order=%d %v", raised, err)
	}
	read()
	if alert.EventID != next {
		t.Fatal("watermark moved backwards")
	}
	// 已有正常心跳早于最新故障不恢复；缺少端口状态也不恢复。
	seed(uuid.NewString(), protocol.Event{Protocol: "dc589", DeviceID: board, Type: protocol.Heartbeat, ReceivedAt: at.Add(9 * time.Second), PortStates: []uint8{0}})
	if _, err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	read()
	if alert.Status != "active" {
		t.Fatal("stale heartbeat resolved")
	}
	seed(uuid.NewString(), protocol.Event{Protocol: "dc589", DeviceID: board, Type: protocol.Heartbeat, ReceivedAt: at.Add(11 * time.Second)})
	if _, err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	read()
	if alert.Status != "active" {
		t.Fatal("incomplete heartbeat resolved")
	}
	// 错设备载荷不能恢复，即使持久头部设备与时刻正确。
	seed(uuid.NewString(), protocol.Event{Protocol: "dc589", DeviceID: "other", Type: protocol.Heartbeat, ReceivedAt: at.Add(12 * time.Second), PortStates: []uint8{0}})
	if _, err := s.Run(ctx); err == nil {
		t.Fatal("wrong device accepted")
	}
	read()
	if alert.Status != "active" {
		t.Fatal("wrong device resolved")
	}
	seed(uuid.NewString(), protocol.Event{Protocol: "dc589", DeviceID: board, Type: protocol.Heartbeat, ReceivedAt: at.Add(13 * time.Second), PortStates: []uint8{0}})
	if _, err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	read()
	if alert.Status != "auto_resolved" || alert.ResolvedAt == nil || !alert.ResolvedAt.Equal(at.Add(13*time.Second)) {
		t.Fatalf("recovery=%+v", alert)
	}
	// 旧回执不会复活，严格较新的故障建立下一告警。
	seed(uuid.NewString(), fault(at.Add(12*time.Second)))
	raised, err = s.Run(ctx)
	if err != nil || raised != 0 {
		t.Fatalf("old after recovery=%d %v", raised, err)
	}
	seed(uuid.NewString(), fault(at.Add(14*time.Second)))
	raised, err = s.Run(ctx)
	if err != nil || raised != 1 {
		t.Fatalf("new episode=%d %v", raised, err)
	}
	var n int64
	if err := central.Table("alert_event").Where("device_id=?", board).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("episodes=%d %v", n, err)
	}
	missing, err := client.HeartbeatAfterKey(ctx, board, "missing-original-key")
	if err != nil || missing.Found || missing.AfterReceivedAt != nil {
		t.Fatalf("missing=%+v %v", missing, err)
	}
	// 最新故障更新后，旧告警对象所附原键不得核销新状态。
	read()
	previous := alert
	if err := central.Table("alert_event").Where("id=? AND created_month=?", alert.ID, alert.CreatedMonth).Update("event_id", "new-watermark").Error; err != nil {
		t.Fatal(err)
	}
	if err := (device.AlertRepo{DB: central}).AutoResolveAlert(central, previous, at.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	read()
	if alert.Status != "active" {
		t.Fatal("stale CAS resolved")
	}
	badKey, goodKey := uuid.NewString(), uuid.NewString()
	bad := fault(at.Add(16 * time.Second))
	bad.DeviceID = "wrong-payload-device"
	seed(badKey, bad)
	good := fault(at.Add(17 * time.Second))
	good.Port = 2
	seed(goodKey, good)
	raised, err = s.Run(ctx)
	if err == nil || raised != 1 {
		t.Fatalf("independent progress=%d %v", raised, err)
	}
	var pending int64
	if err := gateway.Table("device_event").Where("event_key=? AND processed_at IS NULL", badKey).Count(&pending).Error; err != nil || pending != 1 {
		t.Fatalf("bad marked=%d %v", pending, err)
	}
	if err := gateway.Table("device_event").Where("event_key=? AND processed_at IS NOT NULL", goodKey).Count(&pending).Error; err != nil || pending != 1 {
		t.Fatalf("good not marked=%d %v", pending, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
