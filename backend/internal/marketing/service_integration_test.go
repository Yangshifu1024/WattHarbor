//go:build integration

package marketing

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"chargepilot/internal/testfixture"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"testing"
	"time"
)

// marketingAudit 返回测试原操作人；不替换真实审计持久化。
func marketingAudit() ops.AuditContext {
	return ops.AuditContext{Actor: operations.Profile{ID: 991, Username: "marketing-proof", Permissions: []string{"coupon.create", "coupon.update", "coupon.grant", "coupon.activity.manage"}}}
}

// insertServiceCoupon 在 db 预置测试专属当前有效模板，返回原主键；调用者管理回滚或清理。
func insertServiceCoupon(t *testing.T, db *gorm.DB, tag string, quota uint32) uint64 {
	t.Helper()
	amount := int64(500)
	row := AdminCouponRow{Name: tag, DiscountType: "amount", DiscountValueCents: &amount, ValidHours: 24, PerUserQuota: 1, TotalQuota: quota, Status: "active"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

// TestMarketingAuditFailureRollsBackOwnedChanges 使用独立验证连接确认审计故障时模板／活动／发放均未提交。
func TestMarketingAuditFailureRollsBackOwnedChanges(t *testing.T) {
	db := testConn(t).orm
	verify := testConn(t).orm
	tag := "marketing-audit-" + uuid.NewString()
	ctx := context.Background()
	audit := marketingAudit()
	coupon := insertServiceCoupon(t, db, tag, 3)
	uid := testfixture.NewID()
	if err := db.Table("user").Create(map[string]any{"id": uid, "openid": tag, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupSeed(t, verify, nil, []uint64{coupon})
		if err := verify.Table("user").Where("id=?", uid).Delete(nil).Error; err != nil {
			t.Error(err)
		}
		if err := verify.Table("audit_log").Where("actor_name=? AND (after_json LIKE ? OR before_json LIKE ?)", audit.Actor.Username, "%"+tag+"%", "%"+tag+"%").Delete(nil).Error; err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	activity := ActivityInput{Name: tag, TriggerType: TriggerHoliday, CouponID: coupon, StartAt: now.Add(-time.Hour).Format(time.RFC3339), EndAt: now.Add(time.Hour).Format(time.RFC3339), Status: "active", MaxGrants: 3}
	id, err := (ActivityService{DB: db}).Create(ctx, audit, activity)
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("marketing audit unavailable")
	if err := db.Callback().Create().Before("gorm:create").Register("test:marketing-audit", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove("test:marketing-audit")
	amount := int64(500)
	created, err := (CouponService{DB: db}).Create(ctx, audit, Coupon{Name: tag + "-rolled", DiscountType: "amount", DiscountValueCents: &amount, ValidHours: 24, PerUserQuota: 1})
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	var n int64
	if err := verify.Table("coupon").Where("id=?", created.ID).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("coupon committed %d %v", n, err)
	}
	createdID, err := (ActivityService{DB: db}).Create(ctx, audit, activity)
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	if err := verify.Table("coupon_activity_rule").Where("id=?", createdID).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("activity committed %d %v", n, err)
	}
	changed := activity
	changed.Name = tag + "-changed"
	if err := (ActivityService{DB: db}).Update(ctx, audit, id, changed); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	var name string
	if err := verify.Table("coupon_activity_rule").Where("id=?", id).Pluck("name", &name).Error; err != nil || name != tag {
		t.Fatalf("activity update committed %s %v", name, err)
	}
	_, err = (CouponService{DB: db}).Grant(ctx, audit, coupon, GrantInput{UserID: uid, RequestID: uuid.NewString()})
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	if err := verify.Table("coupon_grant").Where("coupon_id=?", coupon).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("grant committed %d %v", n, err)
	}
}

// TestManualGrantCurrentBudgetAndReplay 建立旧RR快照再由另一连接提交发放，确认当前计数防止总量与单人超发且重放不换号。
func TestManualGrantCurrentBudgetAndReplay(t *testing.T) {
	setup := testConn(t).orm
	tag := "marketing-manual-" + uuid.NewString()
	coupon := insertServiceCoupon(t, setup, tag, 1)
	uid := testfixture.NewID()
	for offset := uint64(0); offset < 2; offset++ {
		if err := setup.Table("user").Create(map[string]any{"id": uid + offset, "openid": fmt.Sprint(tag, offset), "status": "active"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupSeed(t, setup, nil, []uint64{coupon})
		if err := setup.Table("user").Where("id IN ?", []uint64{uid, uid + 1}).Delete(nil).Error; err != nil {
			t.Error(err)
		}
	})
	tx2 := testConn(t).orm.Begin()
	defer tx2.Rollback()
	var n int64
	if err := tx2.Table("coupon_grant").Where("coupon_id=?", coupon).Count(&n).Error; err != nil || n != 0 {
		t.Fatal(n, err)
	}
	request := uuid.NewString()
	var original uint64
	if err := testConn(t).orm.Transaction(func(tx *gorm.DB) error {
		var err error
		original, err = (CouponService{}).GrantInTx(tx, coupon, uid, request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := (CouponService{}).GrantInTx(tx2, coupon, uid+1, uuid.NewString())
	if !errors.Is(err, ErrCouponGrant) || got != 0 {
		t.Fatalf("stale total quota %d %v", got, err)
	}
	got, err = (CouponService{}).GrantInTx(tx2, coupon, uid, request)
	if err != nil || got != original {
		t.Fatalf("stale replay %d vs %d %v", got, original, err)
	}
	_, err = (CouponService{}).GrantInTx(tx2, coupon, uid+1, request)
	if !errors.Is(err, ErrCouponGrant) {
		t.Fatalf("replay content changed %v", err)
	}
	if err := tx2.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := setup.Table("coupon_grant").Where("coupon_id=?", coupon).Count(&n).Error; err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

// TestActivitySharedTemplateSerializesDifferentRules 使用两连接和锁闸门验证不同触发共用模板／来源预算只发一份。
func TestActivitySharedTemplateSerializesDifferentRules(t *testing.T) {
	setup := testConn(t).orm
	tag := "marketing-shared-" + uuid.NewString()
	coupon := insertServiceCoupon(t, setup, tag, 0)
	ids := []uint64{testfixture.NewID(), testfixture.NewID() + 1}
	now := time.Now().UTC()
	for i, trigger := range []string{TriggerFirstRecharge, TriggerHoliday} {
		if err := setup.Table("coupon_activity_rule").Create(map[string]any{"id": ids[i], "name": tag, "trigger_type": trigger, "coupon_id": coupon, "max_grants": 1, "per_user_limit": 2, "status": "active", "start_at": now.Add(-time.Hour), "end_at": now.Add(time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { cleanupSeed(t, setup, ids, []uint64{coupon}) })
	p1, p2 := testConn(t), testConn(t)
	gate := newLockGate()
	// 暂停在模板读取返回后；此时必须持有共用模板锁，而非只持有各自的规则锁。
	if err := p1.orm.Callback().Query().After("gorm:query").Register("test:shared-template", func(db *gorm.DB) {
		if db.Statement.Table == "coupon" {
			gate.once.Do(func() { close(gate.locked); <-gate.release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer gate.pass()
	tx1, tx2 := p1.orm.Begin(), p2.orm.Begin()
	defer tx1.Rollback()
	defer tx2.Rollback()
	// outcome 将一笔发券调用的结果与错误传回并发测试控制方；外层事务提交或回滚仍由测试负责。
	type outcome struct {
		// rows 当前发券调用返回的发放结果，供并发控制方比对，不查询或更新记录。
		rows []ActivityResult
		// err 当前调用返回的执行错误；nil 表示该调用成功，不替代持久状态核验。
		err error
	}
	a, b := make(chan outcome, 1), make(chan outcome, 1)
	go func() {
		r, e := (ActivityService{}).ApplyInTx(tx1, ActivityEvent{TriggerType: TriggerFirstRecharge, UserID: ids[0], EventKey: tag + "1", Now: now})
		a <- outcome{r, e}
	}()
	gate.await(t, "shared first")
	go func() {
		r, e := (ActivityService{}).ApplyInTx(tx2, ActivityEvent{TriggerType: TriggerHoliday, UserID: ids[1], EventKey: tag + "2", Now: now})
		b <- outcome{r, e}
	}()
	select {
	case got := <-b:
		t.Fatalf("second bypassed template serialization %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	gate.pass()
	first := <-a
	if first.err != nil || len(first.rows) != 1 {
		t.Fatal(first)
	}
	if err := tx1.Commit().Error; err != nil {
		t.Fatal(err)
	}
	second := <-b
	if second.err != nil || len(second.rows) != 0 {
		t.Fatalf("shared budget overspend %+v", second)
	}
}

// TestCouponRedemptionFrozenMoneyReplayCancelAndRollback 验证真实券锁、冻结金额一致、原支付重放、跨支付拒绝、取消及失败回滚。
func TestCouponRedemptionFrozenMoneyReplayCancelAndRollback(t *testing.T) {
	tx := txRollback(t, testDB(t))
	tag := uuid.NewString()
	coupon := insertServiceCoupon(t, tx, tag, 0)
	uid := testfixture.NewID()
	if err := tx.Table("coupon").Where("id=?", coupon).Updates(map[string]any{"discount_type": "percentage", "discount_value_cents": nil, "discount_percent": 75}).Error; err != nil {
		t.Fatal(err)
	}
	var grant uint64
	if err := tx.Table("coupon_grant").Create(map[string]any{"coupon_id": coupon, "user_id": uid, "grant_source": "manual", "status": "unused", "expired_at": time.Now().Add(time.Hour), "source_event_id": tag}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&grant).Error; err != nil {
		t.Fatal(err)
	}
	ref := RedeemRef{UserID: uid, CouponGrantID: grant, TotalCents: 1001, DiscountCents: 250, PaymentOrder: uid}
	bad := ref
	bad.DiscountCents = 249
	if err := (CouponService{}).RedeemInTx(tx, bad, tag); !errors.Is(err, ErrCouponGrant) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (CouponService{}).RedeemInTx(tx.WithContext(ctx), ref, tag); !errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation lost %v", err)
	}
	rollback := errors.New("after redemption failure")
	if err := tx.Transaction(func(nested *gorm.DB) error {
		if err := (CouponService{}).RedeemInTx(nested, ref, tag); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var n int64
	var status string
	if err := tx.Table("coupon_redemption").Where("biz_id=?", uid).Count(&n).Error; err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err := tx.Table("coupon_grant").Where("id=?", grant).Pluck("status", &status).Error; err != nil || status != "unused" {
		t.Fatal(status, err)
	}
	if err := (CouponService{}).RedeemInTx(tx, ref, tag); err != nil {
		t.Fatal(err)
	}
	if err := (CouponService{}).RedeemInTx(tx, ref, tag); err != nil {
		t.Fatalf("same replay %v", err)
	}
	bad = ref
	bad.PaymentOrder++
	if err := (CouponService{}).RedeemInTx(tx, bad, tag); !errors.Is(err, ErrCouponExhausted) {
		t.Fatal(err)
	}
	var cents int64
	if err := tx.Table("coupon_redemption").Where("biz_id=?", uid).Pluck("discount_cents", &cents).Error; err != nil || cents != 250 {
		t.Fatalf("double discounted %d %v", cents, err)
	}
	if err := tx.Table("coupon_redemption").Where("biz_id=?", uid).Count(&n).Error; err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

// TestMarketingQueriesKeepUserExpiryAndNulls 验证真实用户隔离、展示到期状态、筛选分页和原null字段，不写回过期状态。
func TestMarketingQueriesKeepUserExpiryAndNulls(t *testing.T) {
	tx := txRollback(t, testDB(t))
	coupon := insertServiceCoupon(t, tx, uuid.NewString(), 0)
	uid := testfixture.NewID()
	now := time.Now().UTC()
	for i, row := range []struct {
		// user 该测试发券记录的用户主键，用于核对查询不会返回另一用户的券。
		user uint64
		// expires 该测试发券记录的绝对到期时刻，用于区分可用与过期状态。
		expires time.Time
	}{{uid, now.Add(time.Hour)}, {uid, now.Add(-time.Hour)}, {uid + 1, now.Add(time.Hour)}} {
		if err := tx.Table("coupon_grant").Create(map[string]any{"coupon_id": coupon, "user_id": row.user, "grant_source": "manual", "status": "unused", "expired_at": row.expires, "source_event_id": uuid.NewString()}).Error; err != nil {
			t.Fatal(i, err)
		}
	}
	s := CouponQueryService{DB: tx, Now: func() time.Time { return now }}
	page, err := s.Mine(context.Background(), uid, persist.PageQuery{Page: 1, PageSize: 1, Status: "expired"}, false)
	if err != nil || page.Total != 1 || page.Count != 1 || len(page.Items) != 1 || page.Items[0].Usable || page.Items[0].DiscountPercent != nil || page.Items[0].DiscountValueCents == nil {
		t.Fatalf("expired/null %+v %v", page, err)
	}
	page, err = s.Mine(context.Background(), uid, persist.PageQuery{Page: 2, PageSize: 1}, true)
	if err != nil || page.Total != 1 || len(page.Items) != 0 {
		t.Fatalf("filtered empty %+v %v", page, err)
	}
	var n int64
	if err := tx.Table("coupon_grant").Where("coupon_id=? AND status='unused'", coupon).Count(&n).Error; err != nil || n != 3 {
		t.Fatal(n, err)
	}
}

// TestActivityInviterKeyAndOriginalReceipt 验证两份邀请奖励键符合真实CHAR(36)约束，更新规则后重放沿用原模板而不新增权益。
func TestActivityInviterKeyAndOriginalReceipt(t *testing.T) {
	tx := txRollback(t, testDB(t))
	seedCoupon(t, tx, 4101, 24, nil)
	seedCoupon(t, tx, 4102, 24, nil)
	seedCoupon(t, tx, 4103, 24, nil)
	seedRule(t, tx, 4101, TriggerInviteReward, hours(-1), hours(1), 0, withCoupon(4101), withInviterCoupon(4102), withPerUserLimit(2))
	seedEstablishedUser(t, tx, 74101)
	event := ActivityEvent{TriggerType: TriggerInviteReward, UserID: 74102, InviterID: 74101, EventKey: "invite-original", Now: fixedNow}
	first, err := (ActivityService{}).ApplyInTx(tx, event)
	if err != nil || len(first) != 1 || !first[0].InviterGot {
		t.Fatalf("real inviter insert %+v %v", first, err)
	}
	if err := tx.Table("coupon_activity_rule").Where("id=4101").Updates(map[string]any{"coupon_id": 4103, "inviter_coupon_id": 4103}).Error; err != nil {
		t.Fatal(err)
	}
	replay, err := (ActivityService{}).ApplyInTx(tx, event)
	if err != nil || len(replay) != 1 || !replay[0].Already || replay[0].CouponID != 4101 || !replay[0].InviterGot {
		t.Fatalf("original receipt %+v %v", replay, err)
	}
	var n int64
	if err := tx.Table("coupon_grant").Where("coupon_id IN ?", []uint64{4101, 4102, 4103}).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("reissued %d %v", n, err)
	}
}

// TestActivityStorageFailureRollsBackPartialGrant 验证原请求凭据写入故障不伪装不适用，调用方事务回滚已插权益。
func TestActivityStorageFailureRollsBackPartialGrant(t *testing.T) {
	tx := txRollback(t, testDB(t))
	seedCoupon(t, tx, 4201, 24, nil)
	seedRule(t, tx, 4201, TriggerHoliday, hours(-1), hours(1), 0, withCoupon(4201))
	fault := errors.New("activity receipt unavailable")
	if err := tx.Callback().Create().Before("gorm:create").Register("test:activity-receipt", func(db *gorm.DB) {
		if db.Statement.Table == "coupon_grant_request" {
			db.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer tx.Callback().Create().Remove("test:activity-receipt")
	err := tx.Transaction(func(nested *gorm.DB) error {
		_, err := (ActivityService{}).ApplyInTx(nested, ActivityEvent{TriggerType: TriggerHoliday, UserID: 74201, EventKey: "fault-original", Now: fixedNow})
		return err
	})
	if !errors.Is(err, fault) || errors.Is(err, ErrActivityNotApplicable) {
		t.Fatal(err)
	}
	var n int64
	if err := tx.Table("coupon_grant").Where("coupon_id=4201").Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("orphan grant %d %v", n, err)
	}
}

// TestActivityUsesLockedConfiguration 用另一独立连接在候选读取后修改配置，确认发放使用锁后模板且撤销资格立即生效。
func TestActivityUsesLockedConfiguration(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			writer := testConn(t).orm
			reader := testConn(t).orm
			tag := uuid.NewString()
			a := insertServiceCoupon(t, writer, tag+"a", 0)
			b := insertServiceCoupon(t, writer, tag+"b", 0)
			id := testfixture.NewID()
			now := time.Now().UTC()
			if err := writer.Table("coupon_activity_rule").Create(map[string]any{"id": id, "name": tag, "trigger_type": TriggerHoliday, "coupon_id": a, "status": "active", "start_at": now.Add(-time.Hour), "end_at": now.Add(time.Hour)}).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cleanupSeed(t, writer, []uint64{id}, []uint64{a, b}) })
			called := false
			if err := reader.Callback().Query().After("gorm:query").Register("test:current-rule", func(tx *gorm.DB) {
				if called || tx.Statement.Table != "coupon_activity_rule" {
					return
				}
				called = true
				changes := map[string]any{"coupon_id": b}
				if disabled {
					changes["status"] = "disabled"
				}
				if err := writer.Table("coupon_activity_rule").Where("id=?", id).Updates(changes).Error; err != nil {
					tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			tx := reader.Begin()
			defer tx.Rollback()
			rows, err := (ActivityService{}).ApplyInTx(tx, ActivityEvent{TriggerType: TriggerHoliday, UserID: id, EventKey: tag, Now: now})
			if err != nil || !called {
				t.Fatal(rows, err, called)
			}
			if disabled {
				if len(rows) != 0 {
					t.Fatalf("revoked rule granted %+v", rows)
				}
			} else if len(rows) != 1 || rows[0].CouponID != b {
				t.Fatalf("stale configuration %+v", rows)
			}
		})
	}
}
