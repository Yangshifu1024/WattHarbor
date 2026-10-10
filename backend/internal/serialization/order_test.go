//go:build contract

package serialization

import (
	"testing"
	"time"

	charging "chargepilot/internal/domain/charging"
	orderdomain "chargepilot/internal/domain/order"
	"chargepilot/internal/order"
)

// 固定时间戳：所有期望串里的时间字段都用它，保证序列化字节可复现。
var ts = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// TestOrderStatus 锁定 order.OrderView.status 的 8 个取值。
// 载体是真实的 backend/internal/order.OrderView（后台订单列表/详情）。
func TestOrderStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value order.OrderView
		Want  string
	}{
		{"pending_payment", order.OrderView{CreatedAt: ts, Status: orderdomain.StatusPendingPayment}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"pending_payment","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"paid", order.OrderView{CreatedAt: ts, Status: orderdomain.StatusPaid}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"paid","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"charging", order.OrderView{CreatedAt: ts, Status: orderdomain.StatusCharging}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"charging","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"completed", order.OrderView{CreatedAt: ts, Status: orderdomain.StatusCompleted}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"completed","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"cancelled", order.OrderView{CreatedAt: ts, Status: orderdomain.StatusCancelled}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"cancelled","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"failed", order.OrderView{CreatedAt: ts, Status: orderdomain.StatusFailed}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"failed","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"refunding", order.OrderView{CreatedAt: ts, Status: orderdomain.StatusRefunding}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"refunding","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"refunded", order.OrderView{CreatedAt: ts, Status: orderdomain.StatusRefunded}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"refunded","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
	})
}

// TestOrderBusinessStatus 锁定 business_status 的 3 个取值（数据库生成列，Go 侧只读）。
func TestOrderBusinessStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value order.OrderView
		Want  string
	}{
		{"pending_start", order.OrderView{CreatedAt: ts, BusinessStatus: orderdomain.BusinessPendingStart}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"pending_start","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"charging", order.OrderView{CreatedAt: ts, BusinessStatus: orderdomain.BusinessCharging}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"charging","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"completed", order.OrderView{CreatedAt: ts, BusinessStatus: orderdomain.BusinessCompleted}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"completed","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
	})
}

// TestOrderPaymentStatus 锁定 order.OrderView.payment_status 的 4 个取值。
func TestOrderPaymentStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value order.OrderView
		Want  string
	}{
		{"pending", order.OrderView{CreatedAt: ts, PaymentStatus: orderdomain.PaymentPending}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"pending","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"paid", order.OrderView{CreatedAt: ts, PaymentStatus: orderdomain.PaymentPaid}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"paid","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"refunded", order.OrderView{CreatedAt: ts, PaymentStatus: orderdomain.PaymentRefunded}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"refunded","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"partial_refunded", order.OrderView{CreatedAt: ts, PaymentStatus: orderdomain.PaymentPartialRefunded}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"partial_refunded","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
	})
}

// TestOrderStartSourcePointerSemantics 锁死 StartSource 的**指针**语义：
// 有来源时序列化出字符串，无来源时必须是 null 而不是 ""。
// 这条单独成用例是因为改动最容易发生在这里：把 *OrderStartSource 改成值类型
// 会让"无来源"从 null 变成 ""，而 JSON 消费者无法区分两种含义。
func TestOrderStartSourcePointerSemantics(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value order.OrderView
		Want  string
	}{
		{"nil_序列化为_null", order.OrderView{CreatedAt: ts, StartSource: nil}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"payment", order.OrderView{CreatedAt: ts, StartSource: ptr(order.StartSourcePayment)}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":"payment","selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"balance", order.OrderView{CreatedAt: ts, StartSource: ptr(order.StartSourceBalance)}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":"balance","selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"card", order.OrderView{CreatedAt: ts, StartSource: ptr(order.StartSourceCard)}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":"card","selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
	})
}

// TestOrderRefundViewStatus 锁定 order.RefundViewStatus 的 4 个取值
// （SQL CASE 派生，无数据库列）。
func TestOrderRefundViewStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value order.OrderView
		Want  string
	}{
		{"none", order.OrderView{CreatedAt: ts, RefundStatus: order.RefundViewNone}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"none","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"processing", order.OrderView{CreatedAt: ts, RefundStatus: order.RefundViewProcessing}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"processing","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"partial_refunded", order.OrderView{CreatedAt: ts, RefundStatus: order.RefundViewPartialRefunded}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"partial_refunded","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
		{"refunded", order.OrderView{CreatedAt: ts, RefundStatus: order.RefundViewRefunded}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"refunded","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
	})
}

// TestOrderViewOmitemptyBoundaries 锁定 OrderView 里带 omitempty 的两个字段
// （live、live_unavailable）在「有值」与「零值省略」两种情况下的字节差异。
func TestOrderViewOmitemptyBoundaries(t *testing.T) {
	base := `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`
	withLive := `{"live":{"at":"0001-01-01T00:00:00Z","stale":false,"kwh":0,"seconds":0},"live_unavailable":"超时","order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`
	assertEach(t, []struct {
		Name  string
		Value order.OrderView
		Want  string
	}{
		{"零值_两个字段都省略", order.OrderView{CreatedAt: ts}, base},
		{"有值_字段出现在最前面", order.OrderView{CreatedAt: ts, LiveUnavailable: "超时", Live: &charging.LiveMeterView{}}, withLive},
	})
}
