//go:build integration

package settlement

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestConcurrentInvoiceApplicationsSerializeOnOwnedOrder 验证两连接对同一订单不同请求号只能创建一份申请。
// channel 固定首个订单锁及第二次锁请求顺序；提交造数和结果显式清理并核对零残留。
func TestConcurrentInvoiceApplicationsSerializeOnOwnedOrder(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	first, closeFirst := testConn(t)
	defer closeFirst()
	second, closeSecond := testConn(t)
	defer closeSecond()
	id := testfixture.NewID()
	no := "C-IV-" + rand.Text()
	defer func() {
		for _, q := range []struct {
			// table、where 配对指定清理表和本测试条件。
			table, where string
		}{{"invoice_request", "biz_type='charge' AND biz_id=?"}, {"charge_fee_receipt", "charge_order_id=?"}, {"charge_order", "id=?"}} {
			if err := setup.Table(q.table).Where(q.where, id).Delete(nil).Error; err != nil {
				t.Errorf("cleanup %s: %v", q.table, err)
			}
			var n int64
			if err := setup.Table(q.table).Where(q.where, id).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("remaining %s=%d %v", q.table, n, err)
			}
		}
	}()
	if err := setup.Table("charge_order").Create(map[string]any{"id": id, "order_no": no, "user_id": id, "device_id": "invoice-proof", "port_no": 1, "status": "completed", "created_month": "2026-10-01"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := setup.Create(&ChargeFeeRecord{ChargeOrderID: id, CalculationNo: "B-IV-" + rand.Text(), ResultJSON: []byte(`{"total_cents":400}`)}).Error; err != nil {
		t.Fatal(err)
	}
	gate := newLockGate()
	defer gate.pass()
	key := "invoice_lock_" + rand.Text()
	armOnLock(t, first, gate, key, "charge_order")
	defer first.Callback().Query().Remove(key)
	trying := make(chan struct{})
	var once sync.Once
	key2 := key + "_trying"
	// SQL 尚未生成时，按实际 FROM 模型表及 FOR 锁子句识别第二次订单锁请求。
	if err := second.Callback().Query().Before("gorm:query").Register(key2, func(q *gorm.DB) {
		if q.Statement.Table == "charge_order" {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				once.Do(func() { close(trying) })
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(key2)
	call := func(db *gorm.DB) chan error {
		ch := make(chan error, 1)
		go func() {
			_, err := (InvoiceService{DB: db}).Create(context.Background(), id, InvoiceInput{RequestID: uuid.NewString(), OrderNo: no, InvoiceType: "normal", Title: "并发开票"})
			ch <- err
		}()
		return ch
	}
	a := call(first)
	gate.await(t, "invoice order lock")
	b := call(second)
	select {
	case <-trying:
	case <-time.After(15 * time.Second):
		t.Fatal("second order lock not attempted")
	}
	gate.pass()
	select {
	case err := <-a:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("first application did not finish")
	}
	select {
	case err := <-b:
		if !errors.Is(err, ErrInvoiceDuplicate) {
			t.Fatalf("second application=%v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("second application did not finish")
	}
	var n int64
	if err := setup.Table("invoice_request").Where("biz_id=? AND biz_type='charge'", id).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("duplicate invoices=%d %v", n, err)
	}
}

// TestInvoiceApplicationReplaysOnlyIdenticalOwnerAndContent 验证真实费用回执决定开票额，重放不能换身份或内容。
// 申请及全部造数外层回滚，不涉及真实开票渠道。
func TestInvoiceApplicationReplaysOnlyIdenticalOwnerAndContent(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	seedRefundableOrder(t, tx)
	// 本用例独立指定400分权威费用，替换共享退款夹具中的默认回执。
	if err := tx.Table("charge_fee_receipt").Where("charge_order_id=?", refundOrderID).Delete(nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&ChargeFeeRecord{ChargeOrderID: refundOrderID, CalculationNo: "INV-" + rand.Text(), ResultJSON: []byte(`{"electric_cents":300,"service_cents":100,"total_cents":400}`)}).Error; err != nil {
		t.Fatal(err)
	}
	s := InvoiceService{DB: tx}
	in := InvoiceInput{RequestID: uuid.NewString(), OrderNo: "C-REFUND-1", InvoiceType: "normal", Title: "测试单位", TaxNo: "tax", Email: "test@example.com"}
	out, err := s.Create(context.Background(), refundUserID, in)
	if err != nil || out.TotalCents != 400 || out.ID == 0 {
		t.Fatalf("original fee not used: %+v %v", out, err)
	}
	prior, err := s.Create(context.Background(), refundUserID, in)
	if err != nil || prior != out {
		t.Fatalf("replay: %+v %v", prior, err)
	}
	changed := in
	changed.Title = "另一抬头"
	if _, err := s.Create(context.Background(), refundUserID, changed); !errors.Is(err, ErrInvoiceDuplicate) {
		t.Fatalf("changed content accepted: %v", err)
	}
	if _, err := s.Create(context.Background(), refundUserID+1, in); !errors.Is(err, ErrInvoiceOrderLookup) {
		t.Fatalf("foreign order accepted: %v", err)
	}
	// 不同订单复用请求号时不能返回其他申请，即使该订单也已验证。
	if _, err := ApplyInvoice(tx, InvoiceApplyInput{InvoiceNo: out.InvoiceNo, UserID: refundUserID + 1, OrderID: refundOrderID + 1, TotalCents: 400, InvoiceType: "normal", Title: in.Title, TaxNo: in.TaxNo, Email: in.Email}); !errors.Is(err, ErrInvoiceDuplicate) {
		t.Fatalf("foreign invoice identity returned: %v", err)
	}
	changed = in
	changed.RequestID = uuid.NewString()
	if _, err := s.Create(context.Background(), refundUserID, changed); !errors.Is(err, ErrInvoiceDuplicate) {
		t.Fatalf("second invoice accepted: %v", err)
	}
}

// TestInvoiceReviewAuditFailureRollsBackAndRevocationBlocksSecondSignature 验证审核与审计一起回滚，首位审核人撤权后不能完成第二次审核。
// 权限查询使用可控夹具，审核和审计使用真库，数据外层回滚。
func TestInvoiceReviewAuditFailureRollsBackAndRevocationBlocksSecondSignature(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	if err := tx.Table("invoice_request").Create(map[string]any{"id": id, "invoice_no": "IV-" + rand.Text(), "user_id": id, "biz_type": "charge", "biz_id": id, "total_cents": 400, "title": "测试单位"}).Error; err != nil {
		t.Fatal(err)
	}
	permitted := true
	s := InvoiceReviewService{DB: tx, ReviewRepo: InvoiceReviewRepo{LookupReviewer: func(context.Context, uint64) (ReviewerStatus, error) {
		return ReviewerStatus{Role: "customer_finance", Permitted: permitted}, nil
	}}}
	audit := ops.AuditContext{Actor: operations.Profile{ID: 31, Role: "customer_finance", Username: "invoice-review", Permissions: []string{"invoice.review"}}, RequestID: uuid.NewString()}
	in := InvoiceReviewInput{InvoiceURL: "https://example.com/invoice.pdf"}
	fault := true
	key := "invoice_audit_" + rand.Text()
	if err := db.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if fault && q.Statement.Table == "audit_log" {
			q.AddError(errors.New("audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove(key)
	if _, err := s.Review(context.Background(), audit, id, true, in); err == nil {
		t.Fatal("audit failure committed review")
	}
	var n int64
	if err := tx.Table("invoice_admin_review").Where("invoice_request_id=?", id).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("first signature survived: %d %v", n, err)
	}
	fault = false
	if status, err := s.Review(context.Background(), audit, id, true, in); err != nil || status != "awaiting_second" {
		t.Fatalf("first: %s %v", status, err)
	}
	audit.Actor.ID++
	permitted = false
	if _, err := s.Review(context.Background(), audit, id, true, in); !errors.Is(err, ErrInvoiceConflict) {
		t.Fatalf("revoked first signer accepted: %v", err)
	}
	permitted = true
	if status, err := s.Review(context.Background(), audit, id, true, in); err != nil || status != "approved" {
		t.Fatalf("second: %s %v", status, err)
	}
	var status string
	if err := tx.Table("invoice_request").Select("review_status").Where("id=?", id).Scan(&status).Error; err != nil || status != "issued" {
		t.Fatalf("not issued: %s %v", status, err)
	}
	if err := tx.Table("audit_log").Where("module='invoice' AND target_id=?", id).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("audit count %d %v", n, err)
	}
}
