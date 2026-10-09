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

// 真库测试通过 integration 标签显式运行，TEST_MYSQL_URL 缺失或连接失败直接终止。
// 默认 node tasks.mjs check 不访问数据库；真库入口使用 node scripts/db/test-integration.mjs。

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testDB 打开 central_db 真库连接，配置或连接失败即终止测试。
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

// txRollback 创建测试事务并在结束时回滚，隔离本测试数据。
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

// withStatus 覆盖测试规则状态。
func withStatus(s string) ruleOpt {
	return func(row map[string]any) { row["status"] = s }
}

// withDeletedAt 覆盖测试规则软删除时间。
func withDeletedAt(ts time.Time) ruleOpt {
	return func(row map[string]any) { row["deleted_at"] = ts }
}

// withPerUserLimit 覆盖单人份数，保留零和负数用于测试默认规则。
func withPerUserLimit(n int) ruleOpt {
	return func(row map[string]any) { row["per_user_limit"] = n }
}

// withCoupon 覆盖测试规则的券模板 ID。
func withCoupon(id uint64) ruleOpt {
	return func(row map[string]any) { row["coupon_id"] = id }
}

// withMaxGrants 覆盖总份数，保留零和负数供边界测试。
func withMaxGrants(n int) ruleOpt {
	return func(row map[string]any) { row["max_grants"] = n }
}

// withInviterCoupon 覆盖邀请人奖励券 ID。
func withInviterCoupon(id uint64) ruleOpt {
	return func(row map[string]any) { row["inviter_coupon_id"] = id }
}

// seedRule 用指定主键保存活动规则，使 ID 比对和发放排序测试保持稳定。
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

// ids 按输入顺序提取规则主键并格式化，空集合返回 []。
func ids(rules []activityRule) string { return fmt.Sprint(ruleIDs(rules)) }

// seedEstablishedUser 创建有已完成充电的用户；仅注册的用户不满足邀请人奖励资格。
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
