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

	"chargepilot/internal/device"
	"chargepilot/internal/gateway/control"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// alertReplyLossTransport 仅丢弃网关首次确认已提交的响应，随后恢复真实传输，不改数据库。
type alertReplyLossTransport struct {
	inner http.RoundTripper // 受测试服务管理的实际 Transport
	lost  bool              // 首次标记回复已丢弃
}

// TestConcurrentAlertSyncKeepsOneActiveEpisode 验证两个中央真库连接首次插入同告警时，RR 间隙锁保证仅一条活动告警。
// 冲突交给下一轮重扫，实际提交后仅清理本测试数据。
func TestConcurrentAlertSyncKeepsOneActiveEpisode(t *testing.T) {
	setup, first, second := importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL")
	gw := importDB(t, "DATABASE_URL_GATEWAY")
	board, key := "AC"+uuid.NewString(), uuid.NewString()
	at := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		for _, item := range []struct {
			// db 指定清理所属数据库：告警在 central，协议事件在 gateway。
			db *gorm.DB
			// table、where 配对指定清理表和本测试条件。
			table, where string
			arg          any
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
	// result 汇总告警同步新增数量和错误。
	type result struct {
		// raised 是本次新增告警数，用于检查重复事件不会重复建告警。
		raised int
		err    error
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

// RoundTrip 执行真实请求，仅首次丢弃确认响应并返回结果未知错误；不重试或改事件主键。
func (s *alertReplyLossTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := s.inner.RoundTrip(r)
	if err == nil && !s.lost && r.URL.Path == "/api/v1/internal/device-faults/mark-processed" {
		s.lost = true
		response.Body.Close()
		return nil, errors.New("mark response lost after gateway commit")
	}
	return response, err
}

// TestAlertSyncUsesRegisteredGatewayAndPersistentWatermark 用两套真库和已注册网关验证响应丢失重扫、故障乱序和恢复。
// 协议事件来自测试夹具并全部回滚，不代表实机验证。
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
	// 恢复扫描持续限定本测试设备，排除共享库中其他测试数据。
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
	// 确认响应丢失但已提交，下一轮扫描为空，不能重复建告警。
	raised, err = s.Run(ctx)
	if err != nil || raised != 0 {
		t.Fatalf("lost response recovery=%d %v", raised, err)
	}
	// 重扫响应丢失时的同一故障证据，不能新建告警。
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
	// 最新故障改变后，旧告警的事件键不能清除新故障状态。
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
