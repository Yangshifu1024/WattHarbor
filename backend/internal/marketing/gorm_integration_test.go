//go:build integration

package marketing

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"chargepilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// 真库集成测试的开关。
//
// 这里刻意不做「连不上就 t.Skip」：跳过的测试和通过的测试在
// `go test` 的输出里长得一样，CI 一旦没配数据库就会一路绿灯，
// 而护栏最常见的失效正是「靠放行通过」。要么有库真跑，要么直接失败。
//
// 起库：docker compose -f compose.test.yaml up -d mysql
//       docker compose -f compose.dev.yaml run --rm migrate
// 跑测试：TEST_MYSQL_URL=mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/central_db \
//         go test -tags=integration ./internal/marketing/
//
// build tag 同样是刻意的：`node tasks.mjs check` 不依赖 Docker，日常单测不碰真库；
// 真库测试由 CI 的 integration job 显式带 tag 执行；评审需核对 tag 与命令。

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testDB 返回连到 central_db 的 GORM 句柄。
//
// 环境变量缺失、URL 不合法、连不上或 ping 失败，一律 t.Fatal——
// 不给"这次没测成"留出口。
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv(testMySQLURLEnv)
	if raw == "" {
		t.Fatalf("%s 未设置。真库集成测试不提供跳过路径：没有库就是没测，不是测过了。", testMySQLURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sqlDB, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatalf("连接 %s 失败：%v", raw, err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	orm, err := dbconn.WrapGORM(sqlDB)
	if err != nil {
		t.Fatalf("包装 GORM 失败：%v", err)
	}
	return orm
}

// txRollback 开一个事务并在测试结束时回滚。
//
// 集成测试要写真表，提交就会污染独立测试库。用事务把改动圈在测试内，
// 测试之间不会互相污染，跑多少次都是同一个初始状态。
func txRollback(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("开启事务失败：%v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return tx
}

// ruleOpt 是 seedRule 的可选覆写。
type ruleOpt func(map[string]any)

// withStatus 接收规则状态 s，返回在调用时覆写种子行 status 的 ruleOpt；不校验状态或访问数据库，只修改传入的非 nil 行映射。
func withStatus(s string) ruleOpt {
	return func(row map[string]any) { row["status"] = s }
}

// withDeletedAt 接收软删除时间 ts，返回在调用时覆写种子行 deleted_at 的 ruleOpt；不改变时区或检查零值，只修改传入的非 nil 行映射。
func withDeletedAt(ts time.Time) ruleOpt {
	return func(row map[string]any) { row["deleted_at"] = ts }
}

// withPerUserLimit 接收单用户份数 n，返回覆写种子行 per_user_limit 的 ruleOpt；零值或负数原样保留以测试业务默认规则，不访问数据库。
func withPerUserLimit(n int) ruleOpt {
	return func(row map[string]any) { row["per_user_limit"] = n }
}

// withCoupon 接收券主键 id，返回覆写种子行 coupon_id 的 ruleOpt；不查询券存在性，仅修改传入的非 nil 行映射。
func withCoupon(id uint64) ruleOpt {
	return func(row map[string]any) { row["coupon_id"] = id }
}

// withMaxGrants 接收活动总份数 n，返回覆写种子行 max_grants 的 ruleOpt；不提前转换零值或负数，仅修改种子映射，不访问数据库。
func withMaxGrants(n int) ruleOpt {
	return func(row map[string]any) { row["max_grants"] = n }
}

// withInviterCoupon 接收邀请人奖励券主键 id，返回覆写种子行 inviter_coupon_id 的 ruleOpt；不查询奖励资格或券，仅修改种子映射。
func withInviterCoupon(id uint64) ruleOpt {
	return func(row map[string]any) { row["inviter_coupon_id"] = id }
}

// seedRule 按给定主键插入一条活动规则。
//
// 主键由测试指定而不是自增，这样断言可以直接比对 id，
// 也让"按 id 升序发放"这条能够稳定验证，不受自增值影响。
func seedRule(t *testing.T, tx *gorm.DB, id uint64, trigger string, start, end time.Time, threshold int64, opts ...ruleOpt) {
	t.Helper()
	row := map[string]any{
		"id":              id,
		"name":            "rule-" + trigger,
		"trigger_type":    trigger,
		"coupon_id":       900000 + id,
		"threshold_cents": threshold,
		"max_grants":      0,
		"per_user_limit":  1,
		"status":          "active",
		"start_at":        start,
		"end_at":          end,
	}
	for _, opt := range opts {
		opt(row)
	}
	if err := tx.Table("coupon_activity_rule").Create(row).Error; err != nil {
		t.Fatalf("插入活动规则 %d 失败：%v", id, err)
	}
}

// ruleIDs 把查询结果收成主键列表，便于直接比对。
func ruleIDs(rules []activityRule) []uint64 {
	out := make([]uint64, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.ID)
	}
	return out
}

// ids 接收规则切片 rules，按原顺序提取主键并用 fmt.Sprint 返回比对字符串；空切片返回 []，不排序、修改输入或访问数据库。
func ids(rules []activityRule) string { return fmt.Sprint(ruleIDs(rules)) }

// seedEstablishedUser 造一个"已完成充电"的账号。
// 邀请有奖要求邀请人必须是 established 用户（有过已完成充电或已结算充值），
// 只有注册的空账号不能拿邀请奖励，否则空账号互邀就能消耗活动预算。
func seedEstablishedUser(t *testing.T, tx *gorm.DB, userID uint64) {
	t.Helper()
	if err := tx.Table("charge_order").Create(map[string]any{
		"id": 700000 + userID, "order_no": "O-" + fmt.Sprint(userID), "user_id": userID,
		"device_id": "D1", "port_no": 1, "status": "completed",
		"payment_status": "paid", "created_month": "2026-03-01", "started_at": hours(-48), "ended_at": hours(-47),
	}).Error; err != nil {
		t.Fatalf("预置已完成充电订单失败：%v", err)
	}
}
