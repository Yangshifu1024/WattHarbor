//go:build integration

package control

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	gatewayrepo "chargepilot/internal/gateway/repo"
	"chargepilot/internal/protocol"
	"chargepilot/internal/testfixture"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// gatewayIdentityFixture 为 t 在真实网关库创建私有停用设备、一个有效及一个已删除端口，返回连接与设备编号。
// 各表仅按本次设备编号或厂商主键清理并核对零残留，不触发开发设备、命令或中央资金。
func gatewayIdentityFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	db := gatewayContractDB(t)
	board := "BF" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	vendor := gatewayrepo.Vendor{VendorCode: board, VendorName: "私有测试厂商", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"device_port", "device"} {
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

// TestGatewayActiveIdentityConstraints 核对有效设备编号与二维码由数据库唯一约束兜底，软删后可复用业务编码。
// 端口硬件身份 device_id/port_no 永久唯一；复用二维码使用不同端口，全部新增记录在事务中回滚。
func TestGatewayActiveIdentityConstraints(t *testing.T) {
	db, board := gatewayIdentityFixture(t)
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

// TestGatewayRecordsEventsAndOrderSamples 验证真实建库后的心跳、C2 档位和结束回执仍能入库。
func TestGatewayRecordsEventsAndOrderSamples(t *testing.T) {
	db, board := gatewayIdentityFixture(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()

	orderID := testfixture.NewID()
	orderNo := "ORDER-" + board
	received := time.Now().UTC().Truncate(time.Millisecond)
	port := tx.Table("device_port").Where("device_id=? AND port_no=1", board).Session(&gorm.Session{})
	var portID uint64
	if err := port.Select("id").Scan(&portID).Error; err != nil || portID == 0 {
		t.Fatalf("读取测试端口 id=%d err=%v", portID, err)
	}
	if err := port.Updates(map[string]any{"status": "charging", "current_order_id": orderNo}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("charge_command").Create(map[string]any{
		"command_id": uuid.NewString(), "stop_command_id": uuid.NewString(),
		"charge_order_id": orderID, "payment_order_id": orderID, "order_no": orderNo, "user_id": orderID,
		"device_id": board, "port_no": 1, "port_id": portID, "port_code": board + "-1",
		"status": "acked", "ack_at": received.Add(-time.Minute), "result_reported": true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	sink := gatewayrepo.MySQLSink{DB: tx}
	heartbeat := protocol.Event{
		Protocol: protocol.AdapterIDDC589, DeviceID: board, Type: protocol.Heartbeat, ReceivedAt: received,
		VoltageV: 220, TemperatureC: -2, PortStates: []uint8{1},
		ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedMWh: 2500, ChargedSeconds: 60, PowerDeciWatts: 1234}},
	}
	band := protocol.Event{
		Protocol: protocol.AdapterIDDC589, DeviceID: board, Type: protocol.Telemetry, ReceivedAt: received.Add(time.Second),
		Port: 1, ChargingPorts: []protocol.PortTelemetry{{Port: 1, PowerDeciWatts: 1999}},
	}
	ended := protocol.Event{
		Protocol: protocol.AdapterIDDC589, DeviceID: board, Type: protocol.ChargeEnd, ReceivedAt: received.Add(2 * time.Second),
		Port: 1, EnergyMilliKWh: 3, StartedAt: received.Add(-time.Minute), EndedAt: received.Add(2 * time.Second),
	}
	for _, event := range []protocol.Event{heartbeat, band, ended} {
		if err := sink.Record(t.Context(), event); err != nil {
			t.Fatalf("保存 %s 事件: %v", event.Type, err)
		}
	}
	var count int64
	if err := tx.Table("device_event").Where("device_id=?", board).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("设备事件数=%d err=%v", count, err)
	}
	points, err := gatewayrepo.ListChargeProcessPoints(t.Context(), tx, orderNo, orderID, board, 1, 0, 10)
	if err != nil || len(points) != 1 || points[0].PowerW != 123.4 || points[0].ChargedKWh != 0.0025 ||
		points[0].TemperatureC == nil || *points[0].TemperatureC != -2 {
		t.Fatalf("订单采样=%+v err=%v", points, err)
	}
}
