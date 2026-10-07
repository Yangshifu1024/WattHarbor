//go:build integration

package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// backfillFixture 为 t 在真实网关库创建私有停用设备、一个有效及一个已删除端口，返回连接与设备身份。
// 各表仅按本次设备身份或厂商主键清理并核对零残留，不触发开发设备、命令或中央资金。
func backfillFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	db := gatewayContractDB(t)
	board := "BF" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	vendor := gatewayrepo.Vendor{VendorCode: board, VendorName: "私有补录厂商", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"telemetry_aggregate_hourly", "telemetry_aggregate_15min", "telemetry", "device_port", "device"} {
			if err := db.Table(table).Where("device_id=?", board).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := db.Table(table).Where("device_id=?", board).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d err=%v", table, n, err)
			}
		}
		if err := db.Table("vendor").Where("id=?", vendor.ID).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		var n int64
		if err := db.Table("vendor").Where("id=?", vendor.ID).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("vendor remaining=%d err=%v", n, err)
		}
	})
	if err := db.Table("device").Create(map[string]any{"device_id": board, "vendor_id": vendor.ID, "port_count": 2, "status": "disabled"}).Error; err != nil {
		t.Fatal(err)
	}
	for port := 1; port <= 2; port++ {
		var deleted any
		if port == 2 {
			deleted = time.Now().UTC()
		}
		if err := db.Table("device_port").Create(map[string]any{"device_id": board, "port_no": port, "port_code": board + "-" + strconv.Itoa(port), "status": "idle", "deleted_at": deleted}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, board
}

// callBackfill 用 ctx 经实际 Register 同步提交 input，返回 HTTP 状态与原信封；不创建脱离请求的任务。
func callBackfill(t *testing.T, db *gorm.DB, board string, input []backfillFrame, ctx context.Context) (int, faultEnvelopeReply) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"frames": input})
	if err != nil {
		t.Fatal(err)
	}
	return callFaultEndpoint(t, db, http.MethodPost, "/api/v1/internal/devices/"+board+"/backfill", string(body), ctx)
}

// assertBackfillCounts 核对 status/reply 原成功计数是否与 inserted/skipped 一致，不读取或写入数据库。
func assertBackfillCounts(t *testing.T, status int, reply faultEnvelopeReply, inserted, skipped int) {
	t.Helper()
	var data struct {
		Inserted int `json:"inserted"` // 本次实际提交的原始读数条数。
		Skipped  int `json:"skipped"`  // 原已存身份跳过条数，纯解析同批去重不计入。
	}
	if err := json.Unmarshal(reply.Data, &data); err != nil || status != 200 || reply.Code != 0 || data.Inserted != inserted || data.Skipped != skipped {
		t.Fatalf("backfill status=%d reply=%+v count=%+v err=%v", status, reply, data, err)
	}
}

// TestBackfillKeepsAdmissionDecimalsNullPortsAndReplay 核对停用设备补传、设备级 NULL、精确小数、毫秒去重及原重放。
// 未知／已删除端口拒绝整批，退役／已删除／缺失设备保持原404，不以空读数或聚合替代最终结算。
func TestBackfillKeepsAdmissionDecimalsNullPortsAndReplay(t *testing.T) {
	db, board := backfillFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	port := int16(1)
	input := []backfillFrame{
		{DeviceID: board, MsgType: "telemetry", TS: at.In(time.FixedZone("fixture", 8*60*60)).Format(time.RFC3339Nano), Payload: map[string]json.RawMessage{"voltage_v": json.RawMessage(`"220.1234567"`)}},
		{DeviceID: board, PortNo: &port, MsgType: "telemetry", TS: at.Format(time.RFC3339Nano), Payload: map[string]json.RawMessage{"power_w": json.RawMessage(`10`)}},
		{DeviceID: board, PortNo: &port, MsgType: "telemetry", TS: at.Add(400 * time.Microsecond).Format(time.RFC3339Nano), Payload: map[string]json.RawMessage{"power_w": json.RawMessage(`"10"`)}},
	}
	status, reply := callBackfill(t, db, board, input, ctx)
	assertBackfillCounts(t, status, reply, 2, 0)
	status, reply = callBackfill(t, db, board, input, ctx)
	assertBackfillCounts(t, status, reply, 0, 2)
	var raw []struct {
		PortNo sql.NullInt16 // 原始设备级读数为空、端口级保持实际端口。
		Metric string        // 原指标名称，不替换单位或构造汇总指标。
		Value  string        // MySQL精确十进制文本，不经过float64。
		TS     time.Time     // 原UTC毫秒采样时刻。
	}
	if err := db.Table("telemetry").Select("port_no,metric,CAST(value_num AS CHAR) AS value,ts").Where("device_id=?", board).Order("metric").Find(&raw).Error; err != nil || len(raw) != 2 {
		t.Fatalf("raw=%+v err=%v", raw, err)
	}
	if raw[0].Metric != "power_w" || !raw[0].PortNo.Valid || raw[0].PortNo.Int16 != 1 || raw[0].Value != "10.000000" || raw[1].Metric != "voltage_v" || raw[1].PortNo.Valid || raw[1].Value != "220.123457" || !raw[0].TS.Equal(at) || !raw[1].TS.Equal(at) {
		t.Fatalf("raw evidence changed=%+v", raw)
	}
	for _, table := range []string{"telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		var aggregates []struct {
			PortNo sql.NullInt16 // NULL端口原样存储，唯一键使用库中生成列。
			Metric string        // 原指标名，设备／端口样本分别聚合。
			Value  string        // 当前单样本平均值，不重复累加重放。
			Count  int           // 当前原聚合样本数。
		}
		if err := db.Table(table).Select("port_no,metric,CAST(avg_value AS CHAR) AS value,`count`").Where("device_id=?", board).Order("metric").Find(&aggregates).Error; err != nil || len(aggregates) != 2 || aggregates[0].Count != 1 || aggregates[1].Count != 1 || aggregates[0].Value != "10.000000" || aggregates[1].Value != "220.123457" || !aggregates[0].PortNo.Valid || aggregates[1].PortNo.Valid {
			t.Fatalf("%s aggregate=%+v err=%v", table, aggregates, err)
		}
	}
	for _, invalid := range []int16{2, 3} {
		bad := input[1]
		bad.PortNo, bad.TS = &invalid, at.Add(time.Minute).Format(time.RFC3339)
		good := input[1]
		good.TS = bad.TS
		status, reply := callBackfill(t, db, board, []backfillFrame{good, bad}, ctx)
		if status != 400 || reply.Code != 1005 || reply.Message != "frame port_no is not a port of this device" || string(reply.Data) != "null" {
			t.Fatalf("invalid port=%d status=%d reply=%+v", invalid, status, reply)
		}
		var n int64
		if err := db.Table("telemetry").Where("device_id=?", board).Count(&n).Error; err != nil || n != 2 {
			t.Fatalf("invalid batch partially written=%d err=%v", n, err)
		}
	}
	if err := db.Table("device").Where("device_id=?", board).Update("status", "retired").Error; err != nil {
		t.Fatal(err)
	}
	status, reply = callBackfill(t, db, board, input, ctx)
	if status != 404 || reply.Code != 1004 || reply.Message != "设备不存在或已退役" || string(reply.Data) != "null" {
		t.Fatalf("retired status=%d reply=%+v", status, reply)
	}
	if err := db.Table("device").Where("device_id=?", board).Updates(map[string]any{"status": "disabled", "deleted_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	status, reply = callBackfill(t, db, board, input, ctx)
	if status != 404 || reply.Code != 1004 || string(reply.Data) != "null" {
		t.Fatalf("deleted device status=%d reply=%+v", status, reply)
	}
	missing := "BF" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	input[0].DeviceID = missing
	status, reply = callBackfill(t, db, missing, input[:1], ctx)
	if status != 404 || reply.Code != 1004 || string(reply.Data) != "null" {
		t.Fatalf("missing device status=%d reply=%+v", status, reply)
	}
}

// TestBackfillRollsBackSamplesWhenAggregationIsCanceled 验证聚合前取消回滚整批501样本，重试完整恢复。
func TestBackfillRollsBackSamplesWhenAggregationIsCanceled(t *testing.T) {
	db, board := backfillFixture(t)
	at, port := time.Now().UTC().Add(-time.Hour).Truncate(time.Second), int16(1)
	input := make([]backfillFrame, 501)
	for i := range input {
		input[i] = backfillFrame{DeviceID: board, PortNo: &port, MsgType: "telemetry", TS: at.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano), Payload: map[string]json.RawMessage{"power_w": json.RawMessage(`"10.125"`)}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook, reached := "test:backfill-aggregate-"+uuid.NewString(), 0
	if err := db.Callback().Raw().Before("gorm:raw").Register(hook, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "INSERT INTO telemetry_aggregate_15min") {
			reached++
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Raw().Remove(hook)
	status, reply := callBackfill(t, db, board, input, ctx)
	if reached != 1 || ctx.Err() != context.Canceled || status != 503 || reply.Code != 5003 || reply.Message != "断线补传写入失败" || string(reply.Data) != "null" {
		t.Fatalf("aggregation cancel reached=%d status=%d reply=%+v", reached, status, reply)
	}
	if err := db.Callback().Raw().Remove(hook); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"telemetry", "telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		want := int64(0)
		var n int64
		if err := db.Table(table).Where("device_id=?", board).Count(&n).Error; err != nil || n != want {
			t.Fatalf("%s committed=%d want=%d err=%v", table, n, want, err)
		}
	}
	status, reply = callBackfill(t, db, board, input, context.Background())
	assertBackfillCounts(t, status, reply, len(input), 0)
	for _, table := range []string{"telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		var n int64
		if err := db.Table(table).Where("device_id=?", board).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("replay aggregate %s=%d err=%v", table, n, err)
		}
	}
}

// TestBackfillConcurrentReplayKeepsFirstSamples 用独立连接和真实设备锁等待证明并发重放只累计一次。
func TestBackfillConcurrentReplayKeepsFirstSamples(t *testing.T) {
	first, board := backfillFixture(t)
	second, observer := gatewayContractDB(t), gatewayContractDB(t)
	blocker, waiter := gatewayConnectionID(t, first), gatewayConnectionID(t, second)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	at := time.Now().UTC().Truncate(time.Second)
	samples := []gatewayrepo.BackfillSample{{DeviceID: board, Metric: "voltage_v", Value: "220", TS: at}}
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	hook := "test:backfill-device-lock-" + uuid.NewString()
	if err := first.Callback().Query().After("gorm:query").Register(hook, func(q *gorm.DB) {
		if q.Statement.Table == "device" && q.Error == nil {
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Query().Remove(hook)
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() {
		n, k, err := gatewayrepo.PersistBackfill(ctx, first, board, samples)
		if err == nil && (n != 1 || k != 0) {
			err = fmt.Errorf("首次计数 %d/%d", n, k)
		}
		doneA <- err
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	replay := []gatewayrepo.BackfillSample{{DeviceID: board, Metric: "voltage_v", Value: "999", TS: at.Add(400 * time.Microsecond)}}
	go func() {
		n, k, err := gatewayrepo.PersistBackfill(ctx, second, board, replay)
		if err == nil && (n != 0 || k != 1) {
			err = fmt.Errorf("重放计数 %d/%d", n, k)
		}
		doneB <- err
	}()
	awaitGatewayDeviceLock(t, ctx, observer, waiter, blocker, doneB)
	once.Do(func() { close(release) })
	for _, done := range []<-chan error{doneA, doneB} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for _, table := range []string{"telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		var row struct {
			Count int
			Value string
		}
		if err := observer.Table(table).Select("`count`,CAST(avg_value AS CHAR) value").Where("device_id=?", board).Take(&row).Error; err != nil || row.Count != 1 || row.Value != "220.000000" {
			t.Fatalf("%s=%+v err=%v", table, row, err)
		}
	}
	// 直接写入设备级重复样本必须被唯一键拒绝，不能只证明应用锁有效。
	if err := observer.Table("telemetry").Create(map[string]any{"device_id": board, "port_no": nil, "metric": "voltage_v", "value_num": "999", "ts": at}).Error; err == nil {
		t.Fatal("设备级重复样本绕过唯一键")
	}
}

// TestRealtimeAndBackfillShareSampleIdentity 核对实时和补传同毫秒身份保留首次事实，重复实时不增加聚合。
func TestRealtimeAndBackfillShareSampleIdentity(t *testing.T) {
	db, board := backfillFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if err := db.Exec("DELETE FROM event_outbox WHERE event_id IN (SELECT event_key FROM device_event WHERE device_id=?)", board).Error; err != nil {
			t.Error(err)
		}
		if err := db.Table("device_event").Where("device_id=?", board).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		for _, table := range []string{"device_event", "event_outbox"} {
			var count int64
			query := db.Table(table)
			if table == "device_event" {
				query = query.Where("device_id=?", board)
			} else {
				query = query.Where("JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.data.DeviceID'))=?", board)
			}
			if err := query.Count(&count).Error; err != nil || count != 0 {
				t.Errorf("%s 残留=%d err=%v", table, count, err)
			}
		}
	})
	at := time.Now().UTC().Truncate(time.Second)
	event := protocol.Event{Protocol: "dc589", DeviceID: board, Type: protocol.Telemetry, ReceivedAt: at.Add(400 * time.Microsecond), ChargingPorts: []protocol.PortTelemetry{{Port: 1, PowerDeciWatts: 100, ChargedMWh: 1000000}}}
	sink := gatewayrepo.MySQLSink{DB: db}
	if err := sink.Record(ctx, event); err != nil {
		t.Fatal(err)
	}
	event.ChargingPorts[0].PowerDeciWatts = 999
	if err := sink.Record(ctx, event); err != nil {
		t.Fatal(err)
	}
	n, k, err := gatewayrepo.PersistBackfill(ctx, db, board, []gatewayrepo.BackfillSample{{DeviceID: board, PortNo: sql.NullInt16{Int16: 1, Valid: true}, Metric: "power_w", Value: "999", TS: at}})
	if err != nil || n != 0 || k != 1 {
		t.Fatalf("补传计数=%d/%d err=%v", n, k, err)
	}
	for _, table := range []string{"telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		var row struct {
			Count int
			Value string
		}
		if err := db.Table(table).Select("`count`,CAST(avg_value AS CHAR) value").Where("device_id=? AND metric='power_w'", board).Take(&row).Error; err != nil || row.Count != 1 || row.Value != "10.000000" {
			t.Fatalf("%s=%+v err=%v", table, row, err)
		}
	}
}

// TestBackfillRollsBackFirstAggregateWhenHourlyFails 验证原始读数及已写十五分钟汇总随小时汇总失败一起回滚。
func TestBackfillRollsBackFirstAggregateWhenHourlyFails(t *testing.T) {
	db, board := backfillFixture(t)
	injected := errors.New("小时聚合写入故障")
	hook := "test:backfill-hourly-failure-" + uuid.NewString()
	if err := db.Callback().Raw().Before("gorm:raw").Register(hook, func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "INSERT INTO telemetry_aggregate_hourly") {
			q.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Raw().Remove(hook)
	samples := []gatewayrepo.BackfillSample{{DeviceID: board, Metric: "voltage_v", Value: "220", TS: time.Now().UTC().Truncate(time.Millisecond)}}
	n, k, err := gatewayrepo.PersistBackfill(t.Context(), db, board, samples)
	if !errors.Is(err, injected) || n != 0 || k != 0 {
		t.Fatalf("故障返回=%d/%d err=%v", n, k, err)
	}
	for _, table := range []string{"telemetry", "telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		var count int64
		if err := db.Table(table).Where("device_id=?", board).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s 未完整回滚: %d err=%v", table, count, err)
		}
	}
	if err := db.Callback().Raw().Remove(hook); err != nil {
		t.Fatal(err)
	}
	n, k, err = gatewayrepo.PersistBackfill(t.Context(), db, board, samples)
	if err != nil || n != 1 || k != 0 {
		t.Fatalf("重试返回=%d/%d err=%v", n, k, err)
	}
}

// TestGatewayActiveIdentityConstraints 核对有效设备编号与二维码由数据库唯一约束兜底，软删后可复用业务编码。
// 端口硬件身份 device_id/port_no 永久唯一；复用二维码使用不同端口，全部新增记录在事务中回滚。
func TestGatewayActiveIdentityConstraints(t *testing.T) {
	db, board := backfillFixture(t)
	for _, table := range []string{"device", "device_port"} {
		t.Run(table, func(t *testing.T) {
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			var row map[string]any
			if table == "device" {
				var vendor uint64
				if err := tx.Table("device").Select("vendor_id").Where("device_id=? AND deleted_at IS NULL", board).Scan(&vendor).Error; err != nil {
					t.Fatal(err)
				}
				row = map[string]any{"device_id": board, "vendor_id": vendor, "status": "enabled"}
			} else {
				row = map[string]any{"device_id": board, "port_no": 3, "port_code": board + "-1", "status": "idle"}
			}
			err := tx.Table(table).Create(row).Error
			var duplicate *mysql.MySQLError
			if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
				t.Fatalf("重复有效身份未被唯一键拒绝: %v", err)
			}
			query := tx.Table(table).Where("device_id=? AND deleted_at IS NULL", board)
			if table == "device_port" {
				query = query.Where("port_no=1")
			}
			if err := query.Update("deleted_at", time.Now().UTC()).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Table(table).Create(row).Error; err != nil {
				t.Fatalf("软删后业务编码不可复用: %v", err)
			}
			var active int64
			query = tx.Table(table).Where("device_id=? AND deleted_at IS NULL", board)
			if table == "device_port" {
				query = query.Where("port_code=?", board+"-1")
			}
			if err := query.Count(&active).Error; err != nil || active != 1 {
				t.Fatalf("有效身份数=%d err=%v", active, err)
			}
		})
	}
}
