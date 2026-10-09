//go:build integration

package card

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/testfixture"
	"context"
	"gorm.io/gorm"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFirstCardBindingUsesUniqueClaimAndCurrentLockedIdentity 用两条独立连接控制首次绑定写入等待。
// 第一请求已持有唯一键写锁，第二请求已尝试同号 claim 后放行；同用户恢复同卡且只有一次审计。
func TestFirstCardBindingUsesUniqueClaimAndCurrentLockedIdentity(t *testing.T) {
	first := cardConn(t)
	second := cardConn(t)
	id := testfixture.NewID()
	seedCardUser(t, first, id, 1000)
	cardNo := strconv.FormatUint(id%4000000000+1, 10)
	t.Cleanup(func() {
		var row OnlineCard
		first.Where("card_no=?", cardNo).Find(&row)
		for _, entry := range []struct {
			table, where string
			value        any
		}{{"online_card_audit", "card_id=?", row.ID}, {"online_card", "card_no=?", cardNo}, {"wallet_account", "user_id=?", id}, {"user", "id=?", id}} {
			if err := first.Table(entry.table).Where(entry.where, entry.value).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := first.Table(entry.table).Where(entry.where, entry.value).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("cleanup %s=%d %v", entry.table, n, err)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	locked, attempt, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var lockOnce, attemptOnce sync.Once
	const firstGate = "first_card_claim_gate"
	const secondGate = "second_card_claim_gate"
	if err := first.Callback().Raw().After("gorm:raw").Register(firstGate, func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "INSERT IGNORE INTO online_card") && tx.Error == nil {
			lockOnce.Do(func() {
				close(locked)
				select {
				case <-release:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Raw().Remove(firstGate)
	if err := second.Callback().Raw().Before("gorm:raw").Register(secondGate, func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "INSERT IGNORE INTO online_card") {
			attemptOnce.Do(func() { close(attempt) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Raw().Remove(secondGate)
	actor := operations.Profile{ID: id, Permissions: []string{"online_card.manage"}}
	in := CardBindInput{CardNo: cardNo, UserID: id, Reason: "verified first binding"}
	type result struct {
		id  uint64 /* 卡主键 */
		err error  /* 用例结果 */
	}
	done1, done2 := make(chan result, 1), make(chan result, 1)
	go func() { v, err := (CardAdminService{DB: first}).Bind(ctx, actor, in); done1 <- result{v, err} }()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("first unique claim did not reach gate")
	}
	go func() { v, err := (CardAdminService{DB: second}).Bind(ctx, actor, in); done2 <- result{v, err} }()
	select {
	case <-attempt:
	case <-ctx.Done():
		t.Fatal("second unique claim did not reach gate")
	}
	close(release)
	var one, two result
	select {
	case one = <-done1:
	case <-ctx.Done():
		t.Fatal("first bind did not finish")
	}
	select {
	case two = <-done2:
	case <-ctx.Done():
		t.Fatal("second bind did not finish")
	}
	if one.err != nil || two.err != nil || one.id == 0 || one.id != two.id {
		t.Fatalf("same first binding: one=%d/%v two=%d/%v", one.id, one.err, two.id, two.err)
	}
	var cards, audits int64
	if err := first.Table("online_card").Where("card_no=?", cardNo).Count(&cards).Error; err != nil {
		t.Fatal(err)
	}
	if err := first.Table("online_card_audit").Where("card_id=?", one.id).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if cards != 1 || audits != 1 {
		t.Fatalf("cards=%d audits=%d", cards, audits)
	}
}
