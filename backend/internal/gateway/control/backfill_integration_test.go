//go:build integration

package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
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

// TestBackfillKeepsCommittedSamplesWhenAggregationIsCanceled 验证原始501样本已提交后首次聚合前取消的原503及持久事实。
// 原重放只跳过已有原始键，不额外补聚合；不能借职责迁移增加跨两步事务而改变已有恢复语义。
func TestBackfillKeepsCommittedSamplesWhenAggregationIsCanceled(t *testing.T) {
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
		if table == "telemetry" {
			want = int64(len(input))
		}
		var n int64
		if err := db.Table(table).Where("device_id=?", board).Count(&n).Error; err != nil || n != want {
			t.Fatalf("%s committed=%d want=%d err=%v", table, n, want, err)
		}
	}
	status, reply = callBackfill(t, db, board, input, context.Background())
	assertBackfillCounts(t, status, reply, 0, len(input))
	for _, table := range []string{"telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		var n int64
		if err := db.Table(table).Where("device_id=?", board).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("replay unexpectedly repaired %s=%d err=%v", table, n, err)
		}
	}
}
