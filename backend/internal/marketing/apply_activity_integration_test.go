//go:build integration

package marketing

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
)

// 真库发券测试验证保存结果、唯一键重放和事务回滚。

// seedCoupon 在调用方事务中创建抵扣 500 分的券模板，可覆盖默认结束时间。
func seedCoupon(t *testing.T, tx *gorm.DB, id uint64, validHours int, endAt *time.Time) {
	t.Helper()
	row := map[string]any{
		"id": id, "name": "券-" + fmt.Sprint(id),
		"discount_type": "amount", "discount_value_cents": 500,
		"min_charge_cents": 0, "valid_hours": validHours,
		"total_quota": 0, "per_user_quota": 0, "status": "active",
		"start_at": hours(-24), "end_at": hours(24), "free_minutes": 0,
	}
	if endAt != nil {
		row["end_at"] = *endAt
	}
	if err := tx.Table("coupon").Create(row).Error; err != nil {
		t.Fatalf("插入券 %d 失败：%v", id, err)
	}
}

// grantCount 统计当前连接可见的全部发放记录。
func grantCount(t *testing.T, tx *gorm.DB) int64 { return grantCountFor(t, tx, 0) }

// grantCountFor 统计某个用户拿到的券数；userID=0 表示不限用户全表统计。
func grantCountFor(t *testing.T, tx *gorm.DB, userID uint64) int64 {
	t.Helper()
	q := tx.Table("coupon_grant")
	if userID != 0 {
		q = q.Where("user_id = ?", userID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("统计发放记录失败：%v", err)
	}
	return n
}

// TestApplyActivityRulesGrantsOnceAndIsIdempotentOnReplay 验证同一事件重放不新增券，且不导致调用方事务失败。
func TestApplyActivityRulesGrantsOnceAndIsIdempotentOnReplay(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedCoupon(t, tx, 1001, 24, nil)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0, withCoupon(1001))

	event := ActivityEvent{TriggerType: TriggerFirstRecharge, UserID: 9001, EventKey: "recharge:R-1", Now: fixedNow}

	first, err := (ActivityService{}).ApplyInTx(tx, event)
	if err != nil {
		t.Fatalf("首次发放失败：%v", err)
	}
	if len(first) != 1 || first[0].CouponID != 1001 {
		t.Fatalf("首次结果 = %+v，期望发放 1001", first)
	}
	if n := grantCount(t, tx); n != 1 {
		t.Fatalf("发放记录 %d 条，期望 1 条", n)
	}

	// 重放：同一个事件再来一次。
	replay, replayErr := (ActivityService{}).ApplyInTx(tx, event)
	t.Logf("重放结果=%+v 错误=%v", replay, replayErr)

	if replayErr != nil {
		// 重放若返回唯一键错误，会使同事务中的支付或计费回滚。
		t.Errorf("重放同一事件返回错误 %v；这会让上游事务失败，幂等应返回原结果", replayErr)
	}
	if n := grantCount(t, tx); n != 1 {
		t.Errorf("重放后发放记录 %d 条，期望仍为 1 条（幂等重放不得重复发券）", n)
	}
	for _, r := range replay {
		if !r.Already {
			t.Errorf("重放结果 %+v 未标记 Already，调用方无法区分首次与重放", r)
		}
	}
}

// TestApplyActivityRulesRespectsPerUserLimit 验证单人限一份时第二次参与不再发券。
func TestApplyActivityRulesRespectsPerUserLimit(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedCoupon(t, tx, 1002, 24, nil)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0, withCoupon(1002), withPerUserLimit(1))

	first := ActivityEvent{TriggerType: TriggerFirstRecharge, UserID: 9002, EventKey: "recharge:R-A", Now: fixedNow}
	if got, err := (ActivityService{}).ApplyInTx(tx, first); err != nil || len(got) != 1 {
		t.Fatalf("首次参与应发放：%+v %v", got, err)
	}
	second := ActivityEvent{TriggerType: TriggerFirstRecharge, UserID: 9002, EventKey: "recharge:R-B", Now: fixedNow}
	got, err := (ActivityService{}).ApplyInTx(tx, second)
	if err != nil {
		t.Fatalf("第二次参与不应报错：%v", err)
	}
	if len(got) != 0 {
		t.Errorf("单人上限 1 份，第二次参与却拿到 %+v", got)
	}
	if n := grantCount(t, tx); n != 1 {
		t.Errorf("发放记录 %d 条，期望 1 条", n)
	}
}

// TestApplyActivityRulesRespectsBudget 验证总量限一份时第二个用户参与不再发券。
func TestApplyActivityRulesRespectsBudget(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedCoupon(t, tx, 1003, 24, nil)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0, withCoupon(1003), withMaxGrants(1))

	one := ActivityEvent{TriggerType: TriggerFirstRecharge, UserID: 9003, EventKey: "recharge:R-1", Now: fixedNow}
	if got, err := (ActivityService{}).ApplyInTx(tx, one); err != nil || len(got) != 1 {
		t.Fatalf("首个用户应发放：%+v %v", got, err)
	}
	two := ActivityEvent{TriggerType: TriggerFirstRecharge, UserID: 9004, EventKey: "recharge:R-2", Now: fixedNow}
	got, err := (ActivityService{}).ApplyInTx(tx, two)
	if err != nil {
		t.Fatalf("预算耗尽属于正常跳过，不该报错：%v", err)
	}
	if len(got) != 0 {
		t.Errorf("预算上限 1 份，第二个用户却拿到 %+v", got)
	}
	if n := grantCount(t, tx); n != 1 {
		t.Errorf("发放记录 %d 条，期望 1 条", n)
	}
}

// TestApplyActivityRulesSkipsDeletedCoupon 验证券模板删除后，即使活动规则还在也不发券。
func TestApplyActivityRulesSkipsDeletedCoupon(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedCoupon(t, tx, 1004, 24, nil)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0, withCoupon(1004))

	if err := tx.Table("coupon").Where("id = ?", 1004).
		Update("deleted_at", fixedNow).Error; err != nil {
		t.Fatalf("软删券失败：%v", err)
	}

	got, err := (ActivityService{}).ApplyInTx(tx, ActivityEvent{
		TriggerType: TriggerFirstRecharge, UserID: 9005, EventKey: "recharge:R-1", Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("券已删除属于正常跳过，不该报错：%v", err)
	}
	if len(got) != 0 {
		t.Errorf("券已删除却仍发放 %+v，会产生无法核销的记录", got)
	}
	if n := grantCount(t, tx); n != 0 {
		t.Errorf("发放记录 %d 条，期望 0 条", n)
	}
}

// TestApplyActivityRulesInviteRewardSkipsOnlyInviter 验证邀请人额度用尽时，被邀请人仍可领取自己的奖励。
func TestApplyActivityRulesInviteRewardSkipsOnlyInviter(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedCoupon(t, tx, 1005, 24, nil) // 被邀请人奖励券
	seedCoupon(t, tx, 1006, 24, nil) // 邀请人奖励券
	seedRule(t, tx, 1, TriggerInviteReward, hours(-1), hours(1), 0, withCoupon(1005), withInviterCoupon(1006))

	seedEstablishedUser(t, tx, 9500)

	// 邀请人已用过自己的那一张。
	if err := tx.Table("coupon_grant").Create(map[string]any{
		"coupon_id": 1006, "user_id": 9500, "grant_source": "invite_reward",
		"status": "unused", "expired_at": hours(24), "source_event_id": "seed-inviter-prize",
	}).Error; err != nil {
		t.Fatalf("预置邀请人已领记录失败：%v", err)
	}

	got, err := (ActivityService{}).ApplyInTx(tx, ActivityEvent{
		TriggerType: TriggerInviteReward, UserID: 9501, InviterID: 9500,
		EventKey: "invite:I-1", Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("邀请有奖不应报错：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("结果 = %+v，期望被邀请人仍拿到奖励", got)
	}
	if got[0].InviterGot {
		t.Error("邀请人已达单人上限，不该再拿到奖励")
	}
	// 分别统计：被邀请人应新增一张，邀请人的总数应停在预置的那一张。
	if n := grantCountFor(t, tx, 9501); n != 1 {
		t.Errorf("被邀请人拿到 %d 张，期望 1 张", n)
	}
	if n := grantCountFor(t, tx, 9500); n != 1 {
		t.Errorf("邀请人共 %d 张，期望仍为预置的 1 张", n)
	}
}

// TestApplyActivityRulesRejectsSelfInvite 验证自邀在真库路径上确实被拦下。
func TestApplyActivityRulesRejectsSelfInvite(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedCoupon(t, tx, 1007, 24, nil)
	seedCoupon(t, tx, 1008, 24, nil)
	seedRule(t, tx, 1, TriggerInviteReward, hours(-1), hours(1), 0, withCoupon(1007), withInviterCoupon(1008))

	seedEstablishedUser(t, tx, 9600)
	got, err := (ActivityService{}).ApplyInTx(tx, ActivityEvent{
		TriggerType: TriggerInviteReward, UserID: 9600, InviterID: 9600,
		EventKey: "invite:I-2", Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("自邀属于正常跳过，不该报错：%v", err)
	}
	if len(got) != 0 {
		t.Errorf("自邀却发放了 %+v", got)
	}
	if n := grantCount(t, tx); n != 0 {
		t.Errorf("自邀产生了 %d 条发放记录，期望 0 条", n)
	}
}

// TestApplyActivityRulesReplayWithPerUserLimitAboveOne 验证单人限额大于一时，事件重放仍不重复发券。
// 不能只靠单人限额挡住重放；唯一键命中须复用原结果，不能连带回滚支付或计费。
func TestApplyActivityRulesReplayWithPerUserLimitAboveOne(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedCoupon(t, tx, 1009, 24, nil)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0,
		withCoupon(1009), withPerUserLimit(3))

	event := ActivityEvent{TriggerType: TriggerFirstRecharge, UserID: 9010, EventKey: "recharge:R-REPLAY", Now: fixedNow}
	if got, err := (ActivityService{}).ApplyInTx(tx, event); err != nil || len(got) != 1 {
		t.Fatalf("首次发放应成功：%+v %v", got, err)
	}

	replay, replayErr := (ActivityService{}).ApplyInTx(tx, event)
	t.Logf("单人上限 3 时重放：结果=%+v 错误=%v", replay, replayErr)

	if replayErr != nil {
		t.Errorf("重放同一事件返回错误 %v；这会连带回滚调用方整个事务（支付回调、计费）", replayErr)
	}
	if n := grantCountFor(t, tx, 9010); n != 1 {
		t.Errorf("重放后该用户共 %d 张券，期望仍为 1 张", n)
	}
	if len(replay) == 1 && !replay[0].Already {
		t.Errorf("重放结果 %+v 未标记 Already", replay[0])
	}
}
