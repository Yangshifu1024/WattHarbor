//go:build integration

package marketing

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
)

// 活动发券的端到端行为：真表、真唯一键、真事务。
//
// 前面几组测试验的是"选哪条规则"，这里验的是"发出去之后留下什么"——
// 事务原子性、唯一键冲突与幂等重放。这三样单测一个都覆盖不到：
// 它们只在真库的约束和真事务的回滚行为下才存在。

// seedCoupon 接收测试句柄 t、调用方事务 tx、固定券主键 id、小时有效期 validHours 与可选结束时间 endAt。
// 在 tx 插入五百分抵扣券；endAt 非 nil 覆写默认结束时间，nil 保持默认。无返回值，插入失败由 t.Fatal 终止，提交或回滚由调用方负责。
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

// grantCount 接收测试句柄 t 与调用方句柄 tx，委托 grantCountFor 统计当前可见的全部发券记录并返回数量；不限制用户，查询错误由 t.Fatal 终止，不修改数据。
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

// TestApplyActivityRulesGrantsOnceAndIsIdempotentOnReplay 钉住同一事件重放的行为。
//
// source_event_id 上有唯一键，重放会撞唯一约束。撞了之后会怎样，
// 决定了「用户重试支付」是幂等地返回原结果，还是把整个业务事务拖失败。
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
		// 唯一键冲突会冒泡成错误，整笔业务事务随之失败。
		// 支付回调与计费都在同一个事务里调用这里，
		// 意味着上游重试会被当成真实故障。
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

// TestApplyActivityRulesRespectsPerUserLimit 钉住单人份数上限。
//
// per_user_limit=1 时同一用户第二次参与该活动不应再拿到券。
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

// TestApplyActivityRulesRespectsBudget 钉住活动总发放上限。
//
// max_grants=1 时第二个不同用户参与，该活动预算耗尽，不应再发券。
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

// TestApplyActivityRulesSkipsDeletedCoupon 钉住券被删除后规则不再发券。
//
// 删券不会自动删规则。若发券路径不校验券本身，
// 会生成一张永远核销不了的记录，白白消耗掉活动预算。
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

// TestApplyActivityRulesInviteRewardSkipsOnlyInviter 钉住邀请人达上限时的部分发放。
//
// 邀请人拿不到自己的奖励，不应牵连被邀请人：两者的额度是各自独立的。
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

// TestApplyActivityRulesRejectsSelfInvite 钉住自邀在真库路径上确实被拦下。
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

// TestApplyActivityRulesReplayWithPerUserLimitAboveOne 钉住单人上限大于 1 时的重放。
//
// 默认单人上限是 1，重放会被"该用户已领过"这条挡在插入之前，
// 于是唯一键根本不会被碰到。把上限配成 2 或更多时这条就不成立了——
// 重放会一路走到插入，撞上 coupon_grant.source_event_id 的唯一约束。
// 撞约束的错误会一路冒泡回 ApplyActivityRules，把调用方整个事务拖失败。
// 支付回调与计费都在自己的事务里调用这里，失败会连带回滚入账。
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
