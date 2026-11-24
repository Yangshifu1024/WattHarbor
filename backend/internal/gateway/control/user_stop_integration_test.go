//go:build integration

package control

import (
	gatewaycontract "chargepilot/internal/contracts/gateway"
	gatewayrepo "chargepilot/internal/gateway/repo"
	"chargepilot/internal/gateway/session"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
	"chargepilot/internal/testfixture"
	"context"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stopRecordingSession 记录真实恢复用例发出的命令；不模拟数据库语义或真实断电。
type stopRecordingSession struct{ sends atomic.Int32 }

// gatedStopAuthorizer 让两个首次请求都确认未有STOP后再返回授权，不依赖调度时机。
type gatedStopAuthorizer struct {
	active  gatewayrepo.ActiveOrder
	entered chan struct{}
	proceed <-chan struct{}
}

func (a gatedStopAuthorizer) ActiveStop(ctx context.Context, _ string, _ uint64) (gatewayrepo.ActiveOrder, error) {
	a.entered <- struct{}{}
	select {
	case <-a.proceed:
		return a.active, nil
	case <-ctx.Done():
		return gatewayrepo.ActiveOrder{}, ctx.Err()
	}
}

// Send 记录一次命令下发，返回成功只代表本地发送接口受理。
func (s *stopRecordingSession) Send(context.Context, protocol.Command) error {
	s.sends.Add(1)
	return nil
}

// Close 测试连接无需外部资源，直接返回。
func (*stopRecordingSession) Close() error { return nil }

// TestConcurrentFirstUserStopsReuseCommand 验证同进程服务及绕过设备许可的仓储竞争都复用同一持久STOP。
func TestConcurrentFirstUserStopsReuseCommand(t *testing.T) {
	for _, useService := range []bool{false, true} {
		t.Run(fmt.Sprint(useService), func(t *testing.T) {
			setup := gatewayContractDB(t)
			p1, p2 := gatewayContractDB(t), gatewayContractDB(t)
			id := testfixture.NewID()
			device := "FIRST-" + uuid.NewString()
			order := "C-" + device
			if err := setup.Transaction(func(tx *gorm.DB) error {
				stop := seedRecoveryStop(t, tx, id, device, order, false, time.Now().UTC())
				return tx.Table("charge_stop_command").Where("command_id=?", stop).Delete(nil).Error
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, row := range []struct {
					table, where string
					key          any
				}{
					{"charge_stop_command", "order_no", order}, {"charge_command", "order_no", order}, {"device_port", "id", id},
				} {
					if err := setup.Table(row.table).Where(row.where+"=?", row.key).Delete(nil).Error; err != nil {
						t.Error(err)
					}
					var n int64
					if err := setup.Table(row.table).Where(row.where+"=?", row.key).Count(&n).Error; err != nil || n != 0 {
						t.Errorf("%s residual=%d err=%v", row.table, n, err)
					}
				}
			})
			var start struct{ CommandID string }
			if err := setup.Table("charge_command").Where("order_no=?", order).Take(&start).Error; err != nil {
				t.Fatal(err)
			}
			active := gatewayrepo.ActiveOrder{ChargeOrderID: id, OrderNo: order, UserID: id, DeviceID: device, PortNo: 1, PortID: id, StartCommandID: start.CommandID}
			entered, proceed := make(chan struct{}, 2), make(chan struct{})
			release := sync.OnceFunc(func() { close(proceed) })
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			registry := &session.Registry{}
			connection := &stopRecordingSession{}
			defer registry.Attach(device, connection, nil)()
			if !useService {
				for _, p := range []*gorm.DB{p1, p2} {
					var once atomic.Bool
					if err := p.Callback().Query().Before("gorm:query").Register("test:first_stop", func(tx *gorm.DB) {
						if tx.Statement.Table != "charge_command" || !once.CompareAndSwap(false, true) {
							return
						}
						entered <- struct{}{}
						select {
						case <-proceed:
						case <-ctx.Done():
							tx.AddError(ctx.Err())
						}
					}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = p.Callback().Query().Remove("test:first_stop") })
				}
			}
			type outcome struct {
				reservation gatewayrepo.StopReservation
				err         error
			}
			done := make(chan outcome, 2)
			for _, p := range []*gorm.DB{p1, p2} {
				go func() {
					sink := gatewayrepo.MySQLSink{DB: p}
					if useService {
						result, err := (UserStopService{Orders: gatedStopAuthorizer{active, entered, proceed}, Repo: sink, Devices: registry}).Stop(ctx, order, id)
						done <- outcome{result, err}
					} else {
						result, err := sink.ReserveUserStop(ctx, active)
						done <- outcome{result, err}
					}
				}()
			}
			for range 2 {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("requests did not reach barrier", ctx.Err())
				}
			}
			release()
			first, second := <-done, <-done
			if first.err != nil || second.err != nil || first.reservation.CommandID == "" || first.reservation.CommandID != second.reservation.CommandID {
				t.Fatalf("first=%+v second=%+v", first, second)
			}
			if useService && connection.sends.Load() != 1 {
				t.Fatalf("duplicate send=%d", connection.sends.Load())
			}
			var n int64
			if err := setup.Table("charge_stop_command").Where("order_no=?", order).Count(&n).Error; err != nil || n != 1 {
				t.Fatalf("commands=%d err=%v", n, err)
			}
		})
	}
}

// seedRecoveryStop 保存独立的原START/端口及用户STOP；调用方通过外层事务回滚全部写入。
func seedRecoveryStop(t *testing.T, db *gorm.DB, id uint64, device, order string, compensation bool, at time.Time) string {
	t.Helper()
	command := uuid.NewString()
	status, portStatus := "acked", "charging"
	if compensation {
		status, portStatus = "stopping", "idle"
	}
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"device_port", map[string]any{"id": id, "device_id": device, "port_no": 1, "port_code": device + ":1", "status": portStatus, "current_order_id": order}},
		{"charge_command", map[string]any{"command_id": command, "stop_command_id": uuid.NewString(), "charge_order_id": id, "payment_order_id": id, "order_no": order, "user_id": id, "device_id": device, "port_no": 1, "port_code": device + ":1", "port_id": id, "owns_port": true, "status": status, "session_id": "010203040506", "stop_session_id": "060504030201", "created_at": at, "updated_at": at}},
	} {
		if err := db.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if !compensation {
		stop := uuid.NewString()
		if err := db.Table("charge_stop_command").Create(map[string]any{"command_id": stop, "start_command_id": command, "charge_order_id": id, "order_no": order, "user_id": id, "device_id": device, "port_no": 1, "port_id": id, "status": "pending", "session_id": "060504030201", "created_at": at}).Error; err != nil {
			t.Fatal(err)
		}
		return stop
	}
	return command
}

// TestStopRecoverySkipsOfflinePrefixAndNeverSendsToNewOwner 真实SQL验证两类队列在LIMIT前过滤离线及旧归属。
func TestStopRecoverySkipsOfflinePrefixAndNeverSendsToNewOwner(t *testing.T) {
	for _, compensation := range []bool{false, true} {
		t.Run(fmt.Sprint(compensation), func(t *testing.T) {
			db := gatewayContractDB(t)
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			base := testfixture.NewID()
			prefix := "STOP-" + uuid.NewString()
			at := time.Now().UTC().Add(-time.Hour)
			for i := range 50 {
				seedRecoveryStop(t, tx, base+uint64(i), fmt.Sprintf("%s-%d", prefix, i), fmt.Sprintf("C-%s-%d", prefix, i), compensation, at)
			}
			id := base + 50
			device := prefix + "-online"
			order := "C-" + prefix + "-online"
			command := seedRecoveryStop(t, tx, id, device, order, compensation, at.Add(time.Minute))
			registry := &session.Registry{}
			connection := &stopRecordingSession{}
			defer registry.Attach(device, connection, nil)()
			sink := gatewayrepo.MySQLSink{DB: tx}
			if compensation {
				if err := (Compensation{Repo: sink, Devices: registry}).RetryStopping(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := (UserStopService{Repo: sink, Devices: registry}).RetryPending(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if connection.sends.Load() != 1 {
				t.Fatalf("online task hidden by offline prefix: sends=%d", connection.sends.Load())
			}
			// 模拟旧订单已结束、同端口已被另一订单占用；原命令即使仍在数据库，也不能再次发送。
			if err := tx.Table("device_port").Where("id=?", id).Updates(map[string]any{"status": "charging", "current_order_id": "NEW-" + order}).Error; err != nil {
				t.Fatal(err)
			}
			var eligible bool
			var err error
			if compensation {
				eligible, err = sink.MarkStopSent(t.Context(), command)
			} else {
				eligible, err = sink.MarkUserStopSent(t.Context(), command)
			}
			if err != nil || eligible {
				t.Fatalf("old stop still eligible=%v err=%v", eligible, err)
			}
			// 新服务实例从数据库重读仍排除旧归属，不依赖进程内游标。
			if compensation {
				if err := (Compensation{Repo: sink, Devices: registry}).RetryStopping(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := (UserStopService{Repo: sink, Devices: registry}).RetryPending(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if connection.sends.Load() != 1 {
				t.Fatal("old STOP sent to new owner")
			}
		})
	}
}

// blockingStopSession 用闸门暂停发送，明确控制最后归属核对与端口释放之间的交错。
type blockingStopSession struct {
	entered, proceed chan struct{}
	sends            atomic.Int32
}

// Send 首次停在闸门直到测试放行或取消，不把写入受理当作设备断电。
func (s *blockingStopSession) Send(ctx context.Context, _ protocol.Command) error {
	s.sends.Add(1)
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.proceed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 没有外部连接资源，由测试 context 结束阻塞发送。
func (*blockingStopSession) Close() error { return nil }

// TestPortReleaseWaitsForOldStopBeforeReuse 真实HTTP/SQL验证发送中不能释放占用，释放并换单后旧STOP不再下发。
func TestPortReleaseWaitsForOldStopBeforeReuse(t *testing.T) {
	db := gatewayContractDB(t)
	janitor := gatewayContractDB(t)
	id := testfixture.NewID()
	device := "GUARD-" + uuid.NewString()
	order := "C-" + device
	var command string
	if err := db.Transaction(func(tx *gorm.DB) error {
		command = seedRecoveryStop(t, tx, id, device, order, false, time.Now().UTC().Add(-time.Hour))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, item := range []struct {
			table, key string
			value      any
		}{{"charge_stop_command", "command_id", command}, {"charge_command", "order_no", order}, {"device_port", "id", id}} {
			if err := janitor.Table(item.table).Where(item.key+"=?", item.value).Delete(nil).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := janitor.Table(item.table).Where(item.key+"=?", item.value).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s residual=%d err=%v", item.table, n, err)
			}
		}
	})
	registry := &session.Registry{}
	connection := &blockingStopSession{entered: make(chan struct{}, 1), proceed: make(chan struct{})}
	defer registry.Attach(device, connection, nil)()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	release := sync.OnceFunc(func() { close(connection.proceed) })
	defer release()
	stops := UserStopService{Repo: gatewayrepo.MySQLSink{DB: db}, Devices: registry}
	stopDone := make(chan error, 1)
	go func() { stopDone <- stops.RetryPending(ctx) }()
	select {
	case <-connection.entered:
	case <-ctx.Done():
		t.Fatal("stop never reached send", ctx.Err())
	}
	beforeRelease := make(chan struct{})
	notify := sync.OnceFunc(func() { close(beforeRelease) })
	hook := "test:release-guard-" + uuid.NewString()
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(q *gorm.DB) {
		if q.Statement.Table == "device_port" {
			notify()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(hook)
	router := httpapi.NewRouter()
	DeviceEventAPI{DB: db, Devices: registry, ServiceToken: "guard-test"}.Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "guard-test")
	if err != nil {
		t.Fatal(err)
	}
	type released struct {
		reply gatewaycontract.ReleasePortReply
		err   error
	}
	releaseDone := make(chan released, 1)
	go func() {
		reply, err := client.ReleasePort(ctx, gatewaycontract.ReleasePortRequest{PortID: id, OrderNo: order})
		releaseDone <- released{reply, err}
	}()
	select {
	case <-beforeRelease:
	case <-ctx.Done():
		t.Fatal("release never entered", ctx.Err())
	}
	var port struct{ CurrentOrderID string }
	if err := janitor.Table("device_port").Where("id=?", id).Take(&port).Error; err != nil || port.CurrentOrderID != order {
		t.Fatalf("old send lost owner: %+v err=%v", port, err)
	}
	select {
	case r := <-releaseDone:
		t.Fatalf("release crossed pending send: %+v", r)
	default:
	}
	release()
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	r := <-releaseDone
	if r.err != nil || !r.reply.Released {
		t.Fatalf("release=%+v", r)
	}
	if err := janitor.Table("device_port").Where("id=?", id).Updates(map[string]any{"status": "charging", "current_order_id": "NEW-" + order}).Error; err != nil {
		t.Fatal(err)
	}
	if err := janitor.Table("charge_stop_command").Where("command_id=?", command).Update("sent_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := stops.RetryPending(ctx); err != nil || connection.sends.Load() != 1 {
		t.Fatalf("late STOP sent to new owner: sends=%d err=%v", connection.sends.Load(), err)
	}
}
