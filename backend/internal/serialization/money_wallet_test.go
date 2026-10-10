//go:build contract

package serialization

import (
	"testing"
	"time"

	"chargepilot/internal/card"
	"chargepilot/internal/contracts/gateway"
	carddomain "chargepilot/internal/domain/card"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/domain/money"
	walletdomain "chargepilot/internal/domain/wallet"
	"chargepilot/internal/payment"
	"chargepilot/internal/protocol"
	"chargepilot/internal/settlement"
	"chargepilot/internal/wallet"
)

// TestMoneyPaymentStatus 锁定 money.PaymentStatus 的 6 个取值，
// 载体是真实的 payment.OrderPaymentBrief（订单详情支付摘要）。
func TestMoneyPaymentStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value payment.OrderPaymentBrief
		Want  string
	}{
		{"initiated", payment.OrderPaymentBrief{Status: money.PayStatusInitiated}, `{"order_no":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"status":"initiated"}`},
		{"paid", payment.OrderPaymentBrief{Status: money.PayStatusPaid}, `{"order_no":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"status":"paid"}`},
		{"closed", payment.OrderPaymentBrief{Status: money.PayStatusClosed}, `{"order_no":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"status":"closed"}`},
		{"refunded", payment.OrderPaymentBrief{Status: money.PayStatusRefunded}, `{"order_no":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"status":"refunded"}`},
		{"partial_refunded", payment.OrderPaymentBrief{Status: money.PayStatusPartialRefunded}, `{"order_no":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"status":"partial_refunded"}`},
		{"failed", payment.OrderPaymentBrief{Status: money.PayStatusFailed}, `{"order_no":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"status":"failed"}`},
	})
}

// TestMoneyPayMethod 与 BizType 锁定 payment_order.pay_method（4 值）与 biz_type（2 值）。
func TestMoneyPayMethodAndBizType(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value payment.PaymentOrderView
		Want  string
	}{
		{"wechat", payment.PaymentOrderView{CreatedAt: ts, PayMethod: money.PayMethodWechat}, `{"payment_order_id":0,"order_no":"","user_id":"0","biz_type":"","pay_method":"wechat","status":"","payment_status":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"paid_at":null,"created_at":"2026-01-02T03:04:05Z","charge_order_id":null,"charge_order_no":null}`},
		{"balance", payment.PaymentOrderView{CreatedAt: ts, PayMethod: money.PayMethodBalance}, `{"payment_order_id":0,"order_no":"","user_id":"0","biz_type":"","pay_method":"balance","status":"","payment_status":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"paid_at":null,"created_at":"2026-01-02T03:04:05Z","charge_order_id":null,"charge_order_no":null}`},
		{"mixed", payment.PaymentOrderView{CreatedAt: ts, PayMethod: money.PayMethodMixed}, `{"payment_order_id":0,"order_no":"","user_id":"0","biz_type":"","pay_method":"mixed","status":"","payment_status":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"paid_at":null,"created_at":"2026-01-02T03:04:05Z","charge_order_id":null,"charge_order_no":null}`},
		{"coupon", payment.PaymentOrderView{CreatedAt: ts, PayMethod: money.PayMethodCoupon}, `{"payment_order_id":0,"order_no":"","user_id":"0","biz_type":"","pay_method":"coupon","status":"","payment_status":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"paid_at":null,"created_at":"2026-01-02T03:04:05Z","charge_order_id":null,"charge_order_no":null}`},
		{"biz_charge", payment.PaymentOrderView{CreatedAt: ts, BizType: money.PaymentBizCharge}, `{"payment_order_id":0,"order_no":"","user_id":"0","biz_type":"charge","pay_method":"","status":"","payment_status":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"paid_at":null,"created_at":"2026-01-02T03:04:05Z","charge_order_id":null,"charge_order_no":null}`},
		{"biz_wallet_recharge", payment.PaymentOrderView{CreatedAt: ts, BizType: money.PaymentBizWalletRecharge}, `{"payment_order_id":0,"order_no":"","user_id":"0","biz_type":"wallet_recharge","pay_method":"","status":"","payment_status":"","total_cents":0,"paid_cents":0,"refunded_cents":0,"paid_at":null,"created_at":"2026-01-02T03:04:05Z","charge_order_id":null,"charge_order_no":null}`},
	})
}

// TestMoneyPaymentIntentStatus 锁定 charge_payment_intent.status 的 5 个取值，
// 载体是真实的 payment.PaymentIntent。
func TestMoneyPaymentIntentStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value payment.PaymentIntent
		Want  string
	}{
		{"initiated", payment.PaymentIntent{Status: money.IntentInitiated}, `{"intent_id":"","merchant_order_no":"","payment_order_id":0,"user_id":"0","device_id":"","port_no":0,"port_id":"","station_id":0,"estimate":{"estimated_kwh":"","estimated_minutes":0,"mode":"","electric_cents":0,"service_cents":0,"total_cents":0,"charge_mode":0,"charge_quantity":0},"expires_at":"0001-01-01T00:00:00Z","status":"initiated","discount_cents":0,"payable_cents":0}`},
		{"paid", payment.PaymentIntent{Status: money.IntentPaid}, `{"intent_id":"","merchant_order_no":"","payment_order_id":0,"user_id":"0","device_id":"","port_no":0,"port_id":"","station_id":0,"estimate":{"estimated_kwh":"","estimated_minutes":0,"mode":"","electric_cents":0,"service_cents":0,"total_cents":0,"charge_mode":0,"charge_quantity":0},"expires_at":"0001-01-01T00:00:00Z","status":"paid","discount_cents":0,"payable_cents":0}`},
		{"expired", payment.PaymentIntent{Status: money.IntentExpired}, `{"intent_id":"","merchant_order_no":"","payment_order_id":0,"user_id":"0","device_id":"","port_no":0,"port_id":"","station_id":0,"estimate":{"estimated_kwh":"","estimated_minutes":0,"mode":"","electric_cents":0,"service_cents":0,"total_cents":0,"charge_mode":0,"charge_quantity":0},"expires_at":"0001-01-01T00:00:00Z","status":"expired","discount_cents":0,"payable_cents":0}`},
		{"closed", payment.PaymentIntent{Status: money.IntentClosed}, `{"intent_id":"","merchant_order_no":"","payment_order_id":0,"user_id":"0","device_id":"","port_no":0,"port_id":"","station_id":0,"estimate":{"estimated_kwh":"","estimated_minutes":0,"mode":"","electric_cents":0,"service_cents":0,"total_cents":0,"charge_mode":0,"charge_quantity":0},"expires_at":"0001-01-01T00:00:00Z","status":"closed","discount_cents":0,"payable_cents":0}`},
		{"refund_required", payment.PaymentIntent{Status: money.IntentRefundRequired}, `{"intent_id":"","merchant_order_no":"","payment_order_id":0,"user_id":"0","device_id":"","port_no":0,"port_id":"","station_id":0,"estimate":{"estimated_kwh":"","estimated_minutes":0,"mode":"","electric_cents":0,"service_cents":0,"total_cents":0,"charge_mode":0,"charge_quantity":0},"expires_at":"0001-01-01T00:00:00Z","status":"refund_required","discount_cents":0,"payable_cents":0}`},
	})
}

// TestMoneyRefundStatus 锁定 refund_record.status 的 5 个取值，
// 载体是真实的 wallet.WalletRefundPartRow。
func TestMoneyRefundStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value wallet.WalletRefundPartRow
		Want  string
	}{
		{"pending", wallet.WalletRefundPartRow{Status: money.RefundPending}, `{"refund_no":"","refund_cents":0,"status":"pending","failure_reason":null,"completed_at":null}`},
		{"processing", wallet.WalletRefundPartRow{Status: money.RefundProcessing}, `{"refund_no":"","refund_cents":0,"status":"processing","failure_reason":null,"completed_at":null}`},
		{"success", wallet.WalletRefundPartRow{Status: money.RefundSuccess}, `{"refund_no":"","refund_cents":0,"status":"success","failure_reason":null,"completed_at":null}`},
		{"failed", wallet.WalletRefundPartRow{Status: money.RefundFailed}, `{"refund_no":"","refund_cents":0,"status":"failed","failure_reason":null,"completed_at":null}`},
		{"rejected", wallet.WalletRefundPartRow{Status: money.RefundRejected}, `{"refund_no":"","refund_cents":0,"status":"rejected","failure_reason":null,"completed_at":null}`},
	})
}

// TestMoneyScanKindAndPortStatus 锁定扫码种类（2 值）与端口状态（5 值），
// 载体是真实的 money.ScanResult（同时覆盖 omitempty 的 port/ports 两种形态）。
func TestMoneyScanKindAndPortStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value money.ScanResult
		Want  string
	}{
		{"kind_device_省略port与ports", money.ScanResult{Kind: money.ScanKindDevice}, `{"kind":"device","device_id":"","station_id":0,"device_status":""}`},
		{"kind_port_带单端口", money.ScanResult{Kind: money.ScanKindPort, Port: &money.ScanPort{Status: money.PortIdle}}, `{"kind":"port","device_id":"","station_id":0,"device_status":"","port":{"port_id":"","device_id":"","port_no":0,"port_status":"idle","online":false,"available":false,"device_status":""}}`},
		{"port_charging", money.ScanResult{Port: &money.ScanPort{Status: money.PortCharging}}, `{"kind":"","device_id":"","station_id":0,"device_status":"","port":{"port_id":"","device_id":"","port_no":0,"port_status":"charging","online":false,"available":false,"device_status":""}}`},
		{"port_full", money.ScanResult{Port: &money.ScanPort{Status: money.PortFull}}, `{"kind":"","device_id":"","station_id":0,"device_status":"","port":{"port_id":"","device_id":"","port_no":0,"port_status":"full","online":false,"available":false,"device_status":""}}`},
		{"port_fault", money.ScanResult{Port: &money.ScanPort{Status: money.PortFault}}, `{"kind":"","device_id":"","station_id":0,"device_status":"","port":{"port_id":"","device_id":"","port_no":0,"port_status":"fault","online":false,"available":false,"device_status":""}}`},
		{"port_disabled", money.ScanResult{Port: &money.ScanPort{Status: money.PortDisabled}}, `{"kind":"","device_id":"","station_id":0,"device_status":"","port":{"port_id":"","device_id":"","port_no":0,"port_status":"disabled","online":false,"available":false,"device_status":""}}`},
		{"ports_数组形态", money.ScanResult{Ports: []money.ScanPort{{Status: money.PortIdle}}}, `{"kind":"","device_id":"","station_id":0,"device_status":"","ports":[{"port_id":"","device_id":"","port_no":0,"port_status":"idle","online":false,"available":false,"device_status":""}]}`},
	})
}

// TestWalletRefundViewStatus 锁定 wallet.RefundViewStatus 的**全部 6 个**取值。
//
// needs_review 单独成一条的理由：它只出现在本词表，**不属于** money.RefundStatus，
// money.RefundStatus.Valid() 对它返回 false 是刻意的。把它与那 5 个持久化取值
// 放在一起断言，就是防止有人日后把两者合并。
func TestWalletRefundViewStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value wallet.RefundView
		Want  string
	}{
		{"pending", wallet.RefundView{CreatedAt: ts, Status: wallet.RefundViewPending}, `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"pending","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":null}`},
		{"processing", wallet.RefundView{CreatedAt: ts, Status: wallet.RefundViewProcessing}, `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"processing","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":null}`},
		{"needs_review_不属于_money_RefundStatus", wallet.RefundView{CreatedAt: ts, Status: wallet.RefundViewNeedsReview}, `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"needs_review","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":null}`},
		{"success", wallet.RefundView{CreatedAt: ts, Status: wallet.RefundViewSuccess}, `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"success","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":null}`},
		{"manual_review", wallet.RefundView{CreatedAt: ts, Status: wallet.RefundViewManualReview}, `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"manual_review","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":null}`},
		{"rejected", wallet.RefundView{CreatedAt: ts, Status: wallet.RefundViewRejected}, `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"rejected","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":null}`},
	})
}

// TestWalletRefundViewReviewOmitEmpty 锁定 RefundView.Review 的 omitempty 边界：
// nil 时整段省略，nil 切片时输出 null，有值时输出对象。
func TestWalletRefundViewReviewOmitEmpty(t *testing.T) {
	base := `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":null}`
	withReview := `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":null,"review":{"approved":true,"comment":"","actor_id":0}}`
	withParts := `{"request_id":"","amount_cents":0,"refunded_cents":0,"status":"","reason":null,"created_at":"2026-01-02T03:04:05Z","refund_orders":[]}`
	assertEach(t, []struct {
		Name  string
		Value wallet.RefundView
		Want  string
	}{
		{"nil_review_整段省略", wallet.RefundView{CreatedAt: ts}, base},
		{"有_review_追加在末尾", wallet.RefundView{CreatedAt: ts, Review: &wallet.RefundReviewView{Approved: true}}, withReview},
		{"空切片_part输出空数组而非null", wallet.RefundView{CreatedAt: ts, RefundOrders: []wallet.WalletRefundPartRow{}}, withParts},
	})
}

// TestWalletAccountStatus 锁定 wallet_account.status 的 2 个取值，载体为 BalanceView。
func TestWalletAccountStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value wallet.BalanceView
		Want  string
	}{
		{"active", wallet.BalanceView{Status: walletdomain.AccountActive}, `{"balance_cents":0,"frozen_cents":0,"available_cents":0,"status":"active"}`},
		{"frozen", wallet.BalanceView{Status: walletdomain.AccountFrozen}, `{"balance_cents":0,"frozen_cents":0,"available_cents":0,"status":"frozen"}`},
	})
}

// TestSettlementStatuses 集中锁定结算域三项状态：计费任务（3 值）、账单（3 值）、
// 人工兜底单（2 值）与发票申请审核（4 值）。
func TestSettlementStatuses(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value settlement.Bill
		Want  string
	}{
		{"bill_issued", settlement.Bill{IssuedAt: ts, Status: settlement.BillIssued}, `{"id":"0","bill_no":"","charge_order_id":0,"payment_order_id":null,"user_id":"0","device_id":"","port_no":0,"electric_cents":0,"service_cents":0,"total_cents":0,"prepaid_cents":0,"refund_cents":0,"shortfall_cents":0,"charged_kwh":null,"charged_seconds":null,"status":"issued","issued_at":"2026-01-02T03:04:05Z","settled_at":null,"read":false}`},
		{"bill_settled", settlement.Bill{IssuedAt: ts, Status: settlement.BillSettled}, `{"id":"0","bill_no":"","charge_order_id":0,"payment_order_id":null,"user_id":"0","device_id":"","port_no":0,"electric_cents":0,"service_cents":0,"total_cents":0,"prepaid_cents":0,"refund_cents":0,"shortfall_cents":0,"charged_kwh":null,"charged_seconds":null,"status":"settled","issued_at":"2026-01-02T03:04:05Z","settled_at":null,"read":false}`},
		{"bill_voided", settlement.Bill{IssuedAt: ts, Status: settlement.BillVoided}, `{"id":"0","bill_no":"","charge_order_id":0,"payment_order_id":null,"user_id":"0","device_id":"","port_no":0,"electric_cents":0,"service_cents":0,"total_cents":0,"prepaid_cents":0,"refund_cents":0,"shortfall_cents":0,"charged_kwh":null,"charged_seconds":null,"status":"voided","issued_at":"2026-01-02T03:04:05Z","settled_at":null,"read":false}`},
	})

	assertEach(t, []struct {
		Name  string
		Value settlement.ManualFeeReviewListRow
		Want  string
	}{
		{"manual_fee_pending", settlement.ManualFeeReviewListRow{Status: settlement.ManualFeeReviewPending}, `{"charge_order_id":0,"order_no":"","reason":"","status":"pending","source":null,"created_at":"","reviews":null}`},
		{"manual_fee_resolved", settlement.ManualFeeReviewListRow{Status: settlement.ManualFeeReviewResolved}, `{"charge_order_id":0,"order_no":"","reason":"","status":"resolved","source":null,"created_at":"","reviews":null}`},
	})

	assertEach(t, []struct {
		Name  string
		Value settlement.InvoiceApplication
		Want  string
	}{
		{"invoice_pending", settlement.InvoiceApplication{ReviewStatus: settlement.InvoiceRequestPending}, `{"invoice_no":"","id":0,"review_status":"pending","total_cents":0}`},
		{"invoice_approved", settlement.InvoiceApplication{ReviewStatus: settlement.InvoiceRequestApproved}, `{"invoice_no":"","id":0,"review_status":"approved","total_cents":0}`},
		{"invoice_rejected", settlement.InvoiceApplication{ReviewStatus: settlement.InvoiceRequestRejected}, `{"invoice_no":"","id":0,"review_status":"rejected","total_cents":0}`},
		{"invoice_issued", settlement.InvoiceApplication{ReviewStatus: settlement.InvoiceRequestIssued}, `{"invoice_no":"","id":0,"review_status":"issued","total_cents":0}`},
	})
}

// TestDeviceCommandStatuses 锁定启动命令（含 stopping，5 值）与停机命令（无 stopping，4 值），
// 载体是真实的 contracts/gateway.StartReply 与 StopReply。
//
// 两个包同名的「命令状态」最容易在后续合并，这条用例把停止流的四值与
// 启动流的 stopping 明确区分开。
func TestDeviceCommandStatuses(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.StartReply
		Want  string
	}{
		{"charge_pending", gateway.StartReply{Status: devices.CommandPending}, `{"order_no":"","command_id":"","status":"pending","device_acknowledged":false}`},
		{"charge_sent", gateway.StartReply{Status: devices.CommandSent}, `{"order_no":"","command_id":"","status":"sent","device_acknowledged":false}`},
		{"charge_acked", gateway.StartReply{Status: devices.CommandAcked}, `{"order_no":"","command_id":"","status":"acked","device_acknowledged":false}`},
		{"charge_stopping_只属于启动命令", gateway.StartReply{Status: devices.CommandStopping}, `{"order_no":"","command_id":"","status":"stopping","device_acknowledged":false}`},
		{"charge_rejected", gateway.StartReply{Status: devices.CommandRejected}, `{"order_no":"","command_id":"","status":"rejected","device_acknowledged":false}`},
	})

	assertEach(t, []struct {
		Name  string
		Value gateway.StopReply
		Want  string
	}{
		{"stop_pending", gateway.StopReply{Status: devices.StopCommandPending}, `{"accepted":false,"stopped":false,"command_id":"","status":"pending"}`},
		{"stop_sent", gateway.StopReply{Status: devices.StopCommandSent}, `{"accepted":false,"stopped":false,"command_id":"","status":"sent"}`},
		{"stop_acked", gateway.StopReply{Status: devices.StopCommandAcked}, `{"accepted":false,"stopped":false,"command_id":"","status":"acked"}`},
		{"stop_rejected", gateway.StopReply{Status: devices.StopCommandRejected}, `{"accepted":false,"stopped":false,"command_id":"","status":"rejected"}`},
	})
}

// TestAdapterTransportVendorProtocol 锁定三个容易被混为一谈的「协议」词表：
// protocol.AdapterID（设备说哪种协议）、devices.VendorProtocol（厂商侧怎么接，含 hybrid）、
// protocol.Transport（单条会话实际传输，两值）。载体是真实的 VendorReply。
func TestAdapterTransportVendorProtocol(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.VendorReply
		Want  string
	}{
		{"adapter_dc589", gateway.VendorReply{AdapterClass: protocol.AdapterIDDC589}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"dc589","protocol":"","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
		{"vendor_protocol_tcp", gateway.VendorReply{Protocol: devices.VendorProtocolTCP}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"","protocol":"tcp","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
		{"vendor_protocol_mqtt", gateway.VendorReply{Protocol: devices.VendorProtocolMQTT}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"","protocol":"mqtt","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
		{"vendor_protocol_hybrid_不在_Transport", gateway.VendorReply{Protocol: devices.VendorProtocolHybrid}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"","protocol":"hybrid","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
		{"vendor_status_enabled", gateway.VendorReply{Status: devices.VendorEnabled}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"","protocol":"","status":"enabled","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
		{"vendor_status_disabled", gateway.VendorReply{Status: devices.VendorDisabled}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"","protocol":"","status":"disabled","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
	})
}

// TestChannelRefundStatusCaseShape 锁定渠道退款状态的**大写**形状。
//
// 这四个取值取自渠道响应并原样透传，与 money.PaymentStatus 的小写形成对照。
// 断言的目的是防止有人按「本地词表风格」把它们改成小写：本地不得归一化渠道大小写。
// 载体是真实的 payment.RefundResult。
func TestChannelRefundStatusCaseShape(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value payment.RefundResult
		Want  string
	}{
		{"SUCCESS_大写", payment.RefundResult{Status: payment.ChannelRefundStatusSuccess}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"SUCCESS","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
		{"PROCESSING_大写", payment.RefundResult{Status: payment.ChannelRefundStatusProcessing}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"PROCESSING","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
		{"CLOSED_大写", payment.RefundResult{Status: payment.ChannelRefundStatusClosed}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"CLOSED","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
		{"ABNORMAL_大写", payment.RefundResult{Status: payment.ChannelRefundStatusAbnormal}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"ABNORMAL","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
	})
}

// TestCardStatuses 锁定 card.CardStatus（4 值）与 domain/card.CardOperationStatus（3 值），
// 载体分别是真实的 card.OnlineCard 与 card.CardOperation。
//
// 两者都叫「卡状态」但分属 online_card.status 与 card_operation.status，
// 取值不重叠，放在一起断言就是防止误合并。
func TestCardStatuses(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value card.OnlineCard
		Want  string
	}{
		{"active", card.OnlineCard{Status: card.CardActive}, `{"id":0,"card_no":"","user_id":"0","status":"active"}`},
		{"lost", card.OnlineCard{Status: card.CardLost}, `{"id":0,"card_no":"","user_id":"0","status":"lost"}`},
		{"disabled", card.OnlineCard{Status: card.CardDisabled}, `{"id":0,"card_no":"","user_id":"0","status":"disabled"}`},
		{"unbound", card.OnlineCard{Status: card.CardUnbound}, `{"id":0,"card_no":"","user_id":"0","status":"unbound"}`},
	})
	assertEach(t, []struct {
		Name  string
		Value card.CardOperation
		Want  string
	}{
		{"confirming", card.CardOperation{Status: carddomain.CardStatusConfirming}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"confirming"}`},
		{"confirmed", card.CardOperation{Status: carddomain.CardStatusConfirmed}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"confirmed"}`},
		{"failed", card.CardOperation{Status: carddomain.CardStatusFailed}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"failed"}`},
		{"kind_start", card.CardOperation{Kind: carddomain.CardKindStart}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"start","price_cents":0,"minutes":0,"status":""}`},
		{"kind_extend", card.CardOperation{Kind: carddomain.CardKindExtend}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"extend","price_cents":0,"minutes":0,"status":""}`},
	})
}

var _ = time.Time{}
