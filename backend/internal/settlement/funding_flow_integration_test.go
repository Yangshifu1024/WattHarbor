//go:build integration

package settlement

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TestWalletRefundQueryUsesStoredApprovalAndBoundedReads 使用真实申请、退款及审核行验证批量组合。
// 外层事务回滚全部造数；两用户隔离，查询次数包含分页总数且固定为五次。
func TestWalletRefundQueryUsesStoredApprovalAndBoundedReads(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	ctx := context.Background()
	user := testfixture.NewID()
	wanted := map[string]bool{}
	for i := 0; i < 12; i++ {
		request := uuid.NewString()
		owner := user
		if i == 11 {
			owner++
		} else {
			wanted[request] = true
		}
		if err := tx.Table("wallet_refund_request").Create(map[string]any{"request_id": request, "user_id": owner, "amount_cents": 100, "reason": "query"}).Error; err != nil {
			t.Fatal(err)
		}
		approved := i%2 == 0
		if err := tx.Table("wallet_risk_review").Create(map[string]any{"request_id": request, "actor_id": uint64(99), "approved": approved, "comment": "结论", "response_json": "{}"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Table("wallet_risk_freeze_link").Create(map[string]any{"request_id": request, "freeze_id": uint64(i + 1)}).Error; err != nil {
			t.Fatal(err)
		}
		refundID, err := (RefundRecorder{}).CreateInTx(tx, money.RefundDraft{RefundNo: "Q-" + rand.Text(), BizType: "wallet_recharge", PaymentOrderID: user + uint64(i), UserID: owner, AmountCents: 100, Reason: "query", CreatedMonth: time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC), NextAttemptAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Table("wallet_refund_part").Create(map[string]any{"request_id": request, "refund_record_id": refundID, "wallet_account_id": user, "amount_cents": 100}).Error; err != nil {
			t.Fatal(err)
		}
	}
	reads := 0
	name := "query_wallet_" + rand.Text()
	if err := db.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "wallet_") {
			reads++
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(name)
	out, err := (wallet.QueryService{Repo: wallet.UserWalletRepo{DB: tx}}).Refunds(ctx, user, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 11 || len(out.Items) != 11 || reads != 5 {
		t.Fatalf("page total=%d items=%d reads=%d", out.Total, len(out.Items), reads)
	}
	for _, row := range out.Items {
		if !wanted[row.RequestID] || row.Review == nil || row.Review.ActorID != 99 || len(row.RefundOrders) != 1 || row.RefundOrders[0].RefundCents != 100 {
			t.Fatalf("missing or foreign detail: %+v", row)
		}
		want := "pending"
		if !row.Review.Approved {
			want = "rejected"
		}
		if row.Status != want {
			t.Fatalf("approval ignored: %+v", row)
		}
	}
}

// recoverablePrepay 在渠道响应边界模拟未知结果；数据库申请和保存仍使用真 MySQL。
type recoverablePrepay struct {
	fail   bool     // 当前请求是否丢失渠道响应
	orders []string // 每次向渠道请求的原商户单号
}

// checkoutFixture 固定测试设备及套餐；准入读取为 fixture，申请、回调与退款持久化为真库。
type checkoutFixture struct {
	port        money.ScanResult // 原端口身份、连接事实及中央运营状态
	rule        charging.Rule    // 测试时冻结的完整套餐
	unavailable bool             // 首次申请之后模拟当前方案／设备读取不可用
	reads       int              // 发现读取次数，重放不得重新取当前事实
}

// ResolveScan 在 ctx 下返回 code 的固定事实；恢复故障用于检测不必要的新准入读取。
func (f *checkoutFixture) ResolveScan(ctx context.Context, code string) (money.ScanResult, error) {
	f.reads++
	if f.unavailable {
		return money.ScanResult{}, errors.New("fixture unavailable")
	}
	return f.port, ctx.Err()
}

// ActiveDeviceRule 返回测试 stationID／deviceID 的原方案，故障时拒绝新报价。
func (f *checkoutFixture) ActiveDeviceRule(ctx context.Context, stationID uint64, deviceID string) (charging.Rule, error) {
	if f.unavailable {
		return charging.Rule{}, errors.New("fixture unavailable")
	}
	return f.rule, ctx.Err()
}

// CheckDeviceScheme 为测试中的已支持套餐返回 ctx 取消状态，不使用实际设备能力判断。
func (f *checkoutFixture) CheckDeviceScheme(ctx context.Context, stationID uint64, deviceID string, scheme charging.Scheme) error {
	return ctx.Err()
}

// failingLateRecorder 在事务内能力边界注入故障，正常调用转交实际退款记录及 Outbox 实现。
type failingLateRecorder struct {
	fail bool /* 是否模拟退款存储失败 */
}

// RecordLateInTx 返回受控故障，恢复后在原 tx 内写入 in 和 eventID 的真实凭据。
func (r *failingLateRecorder) RecordLateInTx(tx *gorm.DB, in money.RefundDraft, eventID string) error {
	if r.fail {
		return errors.New("late refund storage unavailable")
	}
	return (RefundRecorder{}).RecordLateInTx(tx, in, eventID)
}

// TestCheckoutUnknownPrepayAndLateRefundShareOriginalIdentity 验证支付准备恢复及迟到退款的归属注入。
// 当前方案读取为 fixture、渠道响应为 stub；金额、唯一键、事务回滚及重放均使用真库。
func TestCheckoutUnknownPrepayAndLateRefundShareOriginalIdentity(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	openid := "checkout-" + rand.Text()
	device := "CHECK-" + rand.Text()
	port := device + ":1"
	if err := tx.Table("user").Create(map[string]any{"id": id, "openid": openid, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	// 原repo过期扫描持续限定本测试用户；Scopes会被前次重放查询消费，不能隔离后续UPDATE。
	const expiryScope = "funding_checkout_expiry_scope"
	if err := tx.Callback().Update().Before("gorm:update").Register(expiryScope, func(q *gorm.DB) {
		if q.Statement.Table == "charge_payment_intent" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "user_id", Value: id}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer tx.Callback().Update().Remove(expiryScope)
	scoped := tx.Session(&gorm.Session{})
	fixture := &checkoutFixture{port: money.ScanResult{Kind: "port", DeviceID: device, StationID: 1, DeviceStatus: "enabled", Port: &money.ScanPort{PortID: port, DeviceID: device, PortNo: 1, Online: true, Available: true, DeviceStatus: "enabled"}}, rule: charging.Rule{ID: 42, StationID: 1, Version: 1, Spec: charging.Spec{Scheme: &charging.Scheme{Name: "支付恢复", Packages: []charging.Package{{ID: 1, Name: "时长套餐", Mode: "duration", PriceCents: 400, Minutes: 60}}}}}}
	provider := &recoverablePrepay{fail: true}
	service := payment.CheckoutService{Ports: fixture, Pricing: fixture, Intents: payment.PaymentIntentRepo{DB: scoped}, Provider: provider}
	in := payment.CheckoutInput{ClientRequestID: uuid.NewString(), PortID: port, OfferID: 4201}
	if _, err := service.Start(context.Background(), id, in); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("prepay fault=%v", err)
	}
	provider.fail = false
	fixture.unavailable = true
	out, err := service.Start(context.Background(), id, in)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.reads != 1 || len(provider.orders) != 2 || provider.orders[0] != out.MerchantOrderNo || provider.orders[1] != out.MerchantOrderNo || out.PayableCents != 400 {
		t.Fatalf("lost original intent: %+v calls=%v reads=%d", out, provider.orders, fixture.reads)
	}
	if _, err := service.Start(context.Background(), id, in); err != nil || len(provider.orders) != 2 {
		t.Fatalf("saved params not reused: %v", err)
	}
	changed := in
	changed.OfferID++
	if _, err := service.Start(context.Background(), id, changed); !errors.Is(err, payment.ErrPaymentIntentConflict) {
		t.Fatalf("different offer accepted: %v", err)
	}
	if err := tx.Model(&payment.PaymentIntentRecord{}).Where("intent_id=?", out.IntentID).Updates(map[string]any{"status": "expired", "expires_at": time.Now().Add(-time.Minute).UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	refunder := &failingLateRecorder{fail: true}
	callback := payment.CallbackService{DB: tx, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: "checkout-app", Refunds: refunder}
	notice := payment.VerifiedPayment{Provider: "simulation", MerchantID: "local-simulation", AppID: "checkout-app", MerchantOrderNo: out.MerchantOrderNo, TransactionID: "SIMLATE" + rand.Text(), OpenID: openid, PaidCents: 400, PaidAt: time.Now().UTC()}
	if _, err := callback.Apply(context.Background(), notice); err == nil {
		t.Fatal("refund failure committed payment")
	}
	var pay payment.PaymentOrderRecord
	if err := tx.Where("order_no=?", out.MerchantOrderNo).Take(&pay).Error; err != nil {
		t.Fatal(err)
	}
	if pay.Status != "initiated" || pay.PaidCents != 0 {
		t.Fatalf("payment not rolled back: %+v", pay)
	}
	refunder.fail = false
	first, err := callback.Apply(context.Background(), notice)
	if err != nil || !first.RefundRequired || first.ChargeOrderID != 0 {
		t.Fatalf("late callback=%+v %v", first, err)
	}
	second, err := callback.Apply(context.Background(), notice)
	if err != nil || !second.RefundRequired || !second.Replayed {
		t.Fatalf("late replay=%+v %v", second, err)
	}
	var refund RefundRecord
	if err := tx.Where("payment_order_id=?", pay.ID).Take(&refund).Error; err != nil {
		t.Fatal(err)
	}
	if refund.RefundCents != 400 || refund.Status != "pending" || refund.ExecutionPolicy != "automatic" {
		t.Fatalf("late refund=%+v", refund)
	}
	for _, q := range []struct {
		// table,where table 为本测试清理或计数的目标表；where 为限定本测试记录身份的参数化条件，保持两者原顺序。
		table, where string
		// arg 当前条件占位符对应的订单、设备或用户身份值，不转换其数值类型。
		arg any
		// want 原支付或退款身份下预期的持久记录数量，用于核对迟到到账路径。
		want int64
	}{
		{"refund_record", "payment_order_id=?", pay.ID, 1},
		{"charge_order", "payment_order_id=?", pay.ID, 0},
		{"payment_callback_idempotent", "wechat_transaction_id=?", notice.TransactionID, 1},
	} {
		var n int64
		if err := tx.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != q.want {
			t.Fatalf("%s count=%d want=%d err=%v", q.table, n, q.want, err)
		}
	}
}

// Prepay 记录 ctx 下 request 的商户单号，故障时不返回参数，恢复时返回原号参数。
func (p *recoverablePrepay) Prepay(ctx context.Context, request payment.PrepayRequest) (payment.PrepayParams, error) {
	p.orders = append(p.orders, request.MerchantOrderNo)
	if p.fail {
		return payment.PrepayParams{}, context.DeadlineExceeded
	}
	return payment.Simulator{}.Prepay(ctx, request)
}

// TestRechargeRecoversOriginalPaymentAndCreditsFrozenWallet 贯通申请、渠道未知、原号恢复与回调到账。
// 除渠道响应故障外使用实际 payment／wallet／settlement 能力；所有数据外层事务回滚。
func TestRechargeRecoversOriginalPaymentAndCreditsFrozenWallet(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	openid := "fund-" + rand.Text()
	if err := tx.Table("user").Create(map[string]any{"id": id, "openid": openid, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("wallet_account").Create(map[string]any{"user_id": id, "balance_cents": 100, "frozen_cents": 0, "version": 1, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	provider := &recoverablePrepay{fail: true}
	service := wallet.RechargeService{DB: tx, Payments: payment.RechargePaymentRepo{DB: tx, Provider: provider}}
	in := wallet.RechargeInput{RequestID: uuid.NewString(), AmountCents: 500}
	if _, err := service.Create(context.Background(), id, in); !errors.Is(err, wallet.ErrPrepayUnavailable) {
		t.Fatalf("unknown prepay: %v", err)
	}
	if len(provider.orders) != 1 {
		t.Fatal("channel was not called")
	}
	provider.fail = false
	out, err := service.Create(context.Background(), id, in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.CanPay || len(provider.orders) != 2 || provider.orders[0] != out.MerchantOrderNo || provider.orders[1] != out.MerchantOrderNo {
		t.Fatalf("new payment on retry: %+v calls=%v", out, provider.orders)
	}
	prior, err := service.Create(context.Background(), id, in)
	if err != nil || prior.PaymentOrderID != out.PaymentOrderID || len(provider.orders) != 2 {
		t.Fatalf("saved prepay not reused: %+v %v", prior, err)
	}
	changed := in
	changed.AmountCents++
	if _, err := service.Create(context.Background(), id, changed); !errors.Is(err, wallet.ErrRechargeConflict) {
		t.Fatalf("amount conflict: %v", err)
	}
	if err := tx.Table("wallet_account").Where("user_id=?", id).Update("status", "frozen").Error; err != nil {
		t.Fatal(err)
	}
	callback := payment.CallbackService{DB: tx, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: "fund-app", Refunds: RefundRecorder{}}
	notice := payment.VerifiedPayment{Provider: "simulation", MerchantID: "local-simulation", AppID: "fund-app", MerchantOrderNo: out.MerchantOrderNo, TransactionID: "SIMFUND" + rand.Text(), OpenID: openid, PaidCents: 500, PaidAt: time.Now().UTC()}
	first, err := callback.Apply(context.Background(), notice)
	if err != nil || first.Replayed {
		t.Fatalf("first callback: %+v %v", first, err)
	}
	replay, err := callback.Apply(context.Background(), notice)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	var account wallet.WalletRow
	if err := tx.Table("wallet_account").Where("user_id=?", id).Take(&account).Error; err != nil {
		t.Fatal(err)
	}
	if account.BalanceCents != 600 || account.Version != 2 || account.Status != "frozen" {
		t.Fatalf("account not credited once: %+v", account)
	}
	for _, q := range []struct {
		// table,where table 为本测试清理或计数的目标表；where 为限定本测试记录身份的参数化条件，保持两者原顺序。
		table, where string
		// arg 当前条件占位符对应的订单、设备或用户身份值，不转换其数值类型。
		arg any
	}{
		{"wallet_recharge_request", "request_id=?", in.RequestID},
		{"payment_order", "id=?", out.PaymentOrderID},
		{"wallet_txn", "user_id=?", id},
		{"payment_callback_idempotent", "wechat_transaction_id=?", notice.TransactionID},
	} {
		var count int64
		if err := tx.Table(q.table).Where(q.where, q.arg).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", q.table, count, err)
		}
	}
	post, err := service.Create(context.Background(), id, in)
	// 原钱包冻结时申请接口仍拒绝；渠道回调已经到账不自动解除冻结。
	if err == nil || !errors.Is(err, wallet.ErrFrozen) || post.CanPay {
		t.Fatalf("frozen wallet silently reopened: %+v %v", post, err)
	}
}

// TestRefundServiceKeepsAuditAtomicAndRechecksSigner 证明新用例保持申请、审计及实时第二签边界。
// MySQL 与所有退款 repo 均为实际实现，身份读取用受控实时 fixture；全部数据外层回滚。
func TestRefundServiceKeepsAuditAtomicAndRechecksSigner(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	seedRefundableOrder(t, tx)
	audit := ops.AuditContext{Actor: operations.Profile{ID: refundActorID, Username: "fund-review", Role: "customer_finance", Permissions: []string{"order.refund.create", "order.refund.review"}}, RequestID: uuid.NewString(), ClientIP: "127.0.0.1"}
	firstPermitted := true
	service := RefundAdminService{DB: tx, ReviewRepo: RefundReviewRepo{DB: tx, LookupSigner: func(context.Context, uint64) (SignerStatus, error) {
		return SignerStatus{Role: "customer_finance", Permitted: firstPermitted}, nil
	}}}
	in := ManualRefundRequest{RequestID: uuid.NewString(), AmountCents: 3000, Reason: "事务及双审验证"}
	fault := true
	const key = "funding-test:audit-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if fault && q.Statement.Table == "audit_log" {
			q.AddError(errors.New("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove(key)
	if _, _, err := service.Request(context.Background(), audit, refundOrderID, in); err == nil {
		t.Fatal("audit failure allowed refund commit")
	}
	for _, table := range []string{"refund_record", "manual_refund_request"} {
		var own int64
		where, arg := "payment_order_id=?", any(refundPayOrdID)
		if table == "manual_refund_request" {
			where, arg = "request_id=?", in.RequestID
		}
		if err := tx.Table(table).Where(where, arg).Count(&own).Error; err != nil || own != 0 {
			t.Fatalf("%s survived audit rollback: %d %v", table, own, err)
		}
	}
	fault = false
	created, no, err := service.Request(context.Background(), audit, refundOrderID, in)
	if err != nil || !created {
		t.Fatalf("request: %v %t", err, created)
	}
	created, prior, err := service.Request(context.Background(), audit, refundOrderID, in)
	if err != nil || created || prior != no {
		t.Fatalf("request replay: %v %t %s", err, created, prior)
	}
	changed := in
	changed.AmountCents++
	if _, _, err := service.Request(context.Background(), audit, refundOrderID, changed); !errors.Is(err, ErrRefundConflict) {
		t.Fatalf("changed content accepted: %v", err)
	}
	var count int64
	if err := tx.Table("audit_log").Where("module='refund' AND action='request' AND request_id=?", in.RequestID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("request audit count=%d %v", count, err)
	}
	if state, err := service.Review(context.Background(), audit, no, true, RefundReviewInput{ApproveComment: "首次复核"}); err != nil || state != "awaiting_second" {
		t.Fatalf("first sign=%s %v", state, err)
	}
	second := audit
	second.Actor.ID++
	firstPermitted = false
	if _, err := service.Review(context.Background(), second, no, true, RefundReviewInput{ApproveComment: "第二次复核"}); !errors.Is(err, ErrRefundConflict) {
		t.Fatalf("revoked signer accepted: %v", err)
	}
	firstPermitted = true
	if state, err := service.Review(context.Background(), second, no, true, RefundReviewInput{ApproveComment: "第二次复核"}); err != nil || state != "approved" {
		t.Fatalf("second sign=%s %v", state, err)
	}
	var record RefundRecord
	if err := tx.Where("refund_no=?", no).Take(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.Status != "pending" || record.ExecutionPolicy != "automatic" {
		t.Fatalf("approval prematurely completed refund: %+v", record)
	}
}
