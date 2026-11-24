//go:build contract

package serialization

import (
	"encoding/json"
	"testing"

	"wattharbor/internal/card"
	"wattharbor/internal/contracts/central"
	carddomain "wattharbor/internal/domain/card"
	identitydomain "wattharbor/internal/domain/identity"
	marketingdomain "wattharbor/internal/domain/marketing"
	operationsdomain "wattharbor/internal/domain/operations"
	walletdomain "wattharbor/internal/domain/wallet"
	"wattharbor/internal/export"
	"wattharbor/internal/identity"
	"wattharbor/internal/marketing"
	"wattharbor/internal/ops"
	"wattharbor/internal/order"
	"wattharbor/internal/pricing"
)

// 本文件覆盖 A 组枚举（营销、卡、后台运营、身份、反馈、计费、导出）的对外 JSON 形状。
// 全部用例的期望串都由临时探针跑 json.Marshal 实际输出取得，不是按结构体推断。
//
// 本轮改造最核心的风险是「同名不同义」：多个词表共用同一批取值串却描述不同业务含义。
// 每个 Test*SameName* 用例把互相冲突的两张表放在同一个测试里，让合并类型的行为立即失败。

// TestMarketingGrantStatus 锁定 domain/marketing.GrantStatus（coupon_grant.status）的 3 个取值。
// 载体是真实的 marketing.UserCoupon（C 端本人券列表），同时覆盖两个可空优惠列的互斥形状。
func TestMarketingGrantStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketing.UserCoupon
		Want  string
	}{
		{"unused", marketing.UserCoupon{ExpiredAt: ts, ExpiresAt: ts, Status: marketingdomain.GrantUnused, DiscountType: marketing.DiscountAmount}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"amount","discount_value_cents":null,"discount_percent":null,"min_charge_cents":0,"status":"unused","expired_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:04:05Z","usable":false}`},
		{"used", marketing.UserCoupon{ExpiredAt: ts, ExpiresAt: ts, Status: marketingdomain.GrantUsed, DiscountType: marketing.DiscountAmount}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"amount","discount_value_cents":null,"discount_percent":null,"min_charge_cents":0,"status":"used","expired_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:04:05Z","usable":false}`},
		{"expired_由内存派生不落库", marketing.UserCoupon{ExpiredAt: ts, ExpiresAt: ts, Status: marketingdomain.GrantExpired, DiscountType: marketing.DiscountAmount}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"amount","discount_value_cents":null,"discount_percent":null,"min_charge_cents":0,"status":"expired","expired_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:04:05Z","usable":false}`},
		// 顺带锁住 discount_value_cents / discount_percent 两个可空列的取整形状：
		// 一个是 *int64 的数字，一个是 *float64 的 80（不是 "80"，也没有 ,string 标签）。
		{"amount_带减免分值", marketing.UserCoupon{ExpiredAt: ts, ExpiresAt: ts, DiscountType: marketing.DiscountAmount, DiscountValueCents: ptr(int64(500))}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"amount","discount_value_cents":500,"discount_percent":null,"min_charge_cents":0,"status":"","expired_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:04:05Z","usable":false}`},
		{"percentage_带折扣比例", marketing.UserCoupon{ExpiredAt: ts, ExpiresAt: ts, DiscountType: marketing.DiscountPercentage, DiscountPercent: ptr(80.0)}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"percentage","discount_value_cents":null,"discount_percent":80,"min_charge_cents":0,"status":"","expired_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:04:05Z","usable":false}`},
	})
}

// TestMarketingGrantSource 锁定 domain/marketing.GrantSource（coupon_grant.grant_source）的 5 个取值。
//
// 载体说明：grant_source 在当前代码里只作为写入参数和 SQL 绑定值出现，没有任何
// 带 json 标签的对外响应字段承载它（marketing.AdminActivityRuleRow、UserCoupon、
// AvailableCoupon 都不输出该列）。因此这里按「不得为测试另造同形结构体」的规则，
// 直接断言这些**真实的枚举类型**本身的序列化字节——它们就是这个值在 JSON 里的全部形状。
func TestMarketingGrantSource(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketingdomain.GrantSource
		Want  string
	}{
		{"register", marketingdomain.GrantSourceRegister, `"register"`},
		{"activity", marketingdomain.GrantSourceActivity, `"activity"`},
		{"invite", marketingdomain.GrantSourceInvite, `"invite"`},
		{"manual", marketingdomain.GrantSourceManual, `"manual"`},
		{"invite_reward", marketingdomain.GrantSourceInviteReward, `"invite_reward"`},
	})
}

// TestMarketingCouponStatus 锁定 marketing.CouponStatus（coupon.status）的 2 个取值，
// 载体是真实的 marketing.AdminCouponRow（后台券模板列表行）。
func TestMarketingCouponStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketing.AdminCouponRow
		Want  string
	}{
		{"active", marketing.AdminCouponRow{Status: marketing.CouponActive}, `{"id":0,"name":"","discount_type":"","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"active","start_at":null,"end_at":null}`},
		{"disabled", marketing.AdminCouponRow{Status: marketing.CouponDisabled}, `{"id":0,"name":"","discount_type":"","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"disabled","start_at":null,"end_at":null}`},
	})
}

// TestMarketingActivityStatus 锁定 marketing.ActivityStatus（coupon_activity_rule.status）的 2 个取值，
// 载体是真实的 marketing.AdminActivityRuleRow（后台活动规则列表行）。
func TestMarketingActivityStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketing.AdminActivityRuleRow
		Want  string
	}{
		{"active", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, Status: marketing.ActivityActive}, `{"id":0,"name":"","trigger_type":"","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"active","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
		{"disabled", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, Status: marketing.ActivityDisabled}, `{"id":0,"name":"","trigger_type":"","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"disabled","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
	})
}

// TestMarketingDiscountType 锁定 marketing.DiscountType（coupon.discount_type）的 3 个取值。
// 载体有两个：AdminCouponRow（后台列表，含三个互斥优惠列）和 AvailableCoupon（C 端可领券）。
func TestMarketingDiscountType(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketing.AdminCouponRow
		Want  string
	}{
		{"amount", marketing.AdminCouponRow{DiscountType: marketing.DiscountAmount}, `{"id":0,"name":"","discount_type":"amount","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"","start_at":null,"end_at":null}`},
		{"percentage", marketing.AdminCouponRow{DiscountType: marketing.DiscountPercentage}, `{"id":0,"name":"","discount_type":"percentage","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"","start_at":null,"end_at":null}`},
		{"time_free", marketing.AdminCouponRow{DiscountType: marketing.DiscountTimeFree}, `{"id":0,"name":"","discount_type":"time_free","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"","start_at":null,"end_at":null}`},
	})

	assertEach(t, []struct {
		Name  string
		Value marketing.AvailableCoupon
		Want  string
	}{
		{"amount_用户端", marketing.AvailableCoupon{ExpiresAt: ts, DiscountType: marketing.DiscountAmount}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"amount","min_charge_cents":0,"expires_at":"2026-01-02T03:04:05Z"}`},
		{"percentage_用户端", marketing.AvailableCoupon{ExpiresAt: ts, DiscountType: marketing.DiscountPercentage}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"percentage","min_charge_cents":0,"expires_at":"2026-01-02T03:04:05Z"}`},
		{"time_free_用户端", marketing.AvailableCoupon{ExpiresAt: ts, DiscountType: marketing.DiscountTimeFree}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"time_free","min_charge_cents":0,"expires_at":"2026-01-02T03:04:05Z"}`},
	})
}

// TestMarketingTriggerType 锁定 marketing.TriggerType（coupon_activity_rule.trigger_type）的 4 个取值。
// 载体有两个：AdminActivityRuleRow（后台列表）与 ActivityInput（后台写入请求）。
func TestMarketingTriggerType(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketing.AdminActivityRuleRow
		Want  string
	}{
		{"first_recharge", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, TriggerType: marketing.TriggerFirstRecharge}, `{"id":0,"name":"","trigger_type":"first_recharge","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
		{"invite_reward", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, TriggerType: marketing.TriggerInviteReward}, `{"id":0,"name":"","trigger_type":"invite_reward","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
		{"threshold_redeem", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, TriggerType: marketing.TriggerThresholdRedeem}, `{"id":0,"name":"","trigger_type":"threshold_redeem","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
		{"holiday", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, TriggerType: marketing.TriggerHoliday}, `{"id":0,"name":"","trigger_type":"holiday","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
	})

	assertEach(t, []struct {
		Name  string
		Value marketing.ActivityInput
		Want  string
	}{
		{"first_recharge_写入请求", marketing.ActivityInput{TriggerType: marketing.TriggerFirstRecharge}, `{"name":"","trigger_type":"first_recharge","coupon_id":0,"inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"","end_at":""}`},
		{"invite_reward_写入请求", marketing.ActivityInput{TriggerType: marketing.TriggerInviteReward, InviterCouponID: ptr(uint64(9))}, `{"name":"","trigger_type":"invite_reward","coupon_id":0,"inviter_coupon_id":9,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"","end_at":""}`},
		{"threshold_redeem_写入请求", marketing.ActivityInput{TriggerType: marketing.TriggerThresholdRedeem}, `{"name":"","trigger_type":"threshold_redeem","coupon_id":0,"inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"","end_at":""}`},
		{"holiday_写入请求", marketing.ActivityInput{TriggerType: marketing.TriggerHoliday}, `{"name":"","trigger_type":"holiday","coupon_id":0,"inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"","end_at":""}`},
	})
}

// TestMarketingSameNameCouponAndActivityStatus 锁死「CouponStatus 与 ActivityStatus 取值串完全相同」。
//
// 两个词表都是 active/disabled，分属 coupon 与 coupon_activity_rule 两张表，语义不同、
// 不得合并。把两张表的行放进同一个测试，任何一方改名（例如给活动加 published）
// 或两个类型被合并成同一个，都会立刻失败。
func TestMarketingSameNameCouponAndActivityStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketing.AdminCouponRow
		Want  string
	}{
		{"券模板_active", marketing.AdminCouponRow{Status: marketing.CouponActive}, `{"id":0,"name":"","discount_type":"","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"active","start_at":null,"end_at":null}`},
		{"券模板_disabled", marketing.AdminCouponRow{Status: marketing.CouponDisabled}, `{"id":0,"name":"","discount_type":"","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"disabled","start_at":null,"end_at":null}`},
	})

	assertEach(t, []struct {
		Name  string
		Value marketing.AdminActivityRuleRow
		Want  string
	}{
		{"活动规则_active", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, Status: marketing.ActivityActive}, `{"id":0,"name":"","trigger_type":"","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"active","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
		{"活动规则_disabled", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, Status: marketing.ActivityDisabled}, `{"id":0,"name":"","trigger_type":"","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"disabled","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
	})

	// CouponUpdate 是券模板状态唯一的写入载体，同样是 active/disabled。
	assertEach(t, []struct {
		Name  string
		Value marketing.CouponUpdate
		Want  string
	}{
		{"券模板写入_active", marketing.CouponUpdate{Status: marketing.CouponActive}, `{"name":"","status":"active"}`},
		{"券模板写入_disabled", marketing.CouponUpdate{Status: marketing.CouponDisabled}, `{"name":"","status":"disabled"}`},
	})
}

// TestMarketingGrantStatusVsCouponAndActivityStatus 锁死三张表的 status 词表互不重叠：
// coupon_grant.status 是 unused/used/expired，coupon.status 与 coupon_activity_rule.status
// 是 active/disabled。三个词表没有任何一个交集，合并任一两个都会产生错误判定。
func TestMarketingGrantStatusVsCouponAndActivityStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketing.UserCoupon
		Want  string
	}{
		{"发放记录_unused_券模板无此值", marketing.UserCoupon{ExpiredAt: ts, ExpiresAt: ts, Status: marketingdomain.GrantUnused}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"","discount_value_cents":null,"discount_percent":null,"min_charge_cents":0,"status":"unused","expired_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:04:05Z","usable":false}`},
		{"发放记录_used", marketing.UserCoupon{ExpiredAt: ts, ExpiresAt: ts, Status: marketingdomain.GrantUsed}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"","discount_value_cents":null,"discount_percent":null,"min_charge_cents":0,"status":"used","expired_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:04:05Z","usable":false}`},
		{"发放记录_expired", marketing.UserCoupon{ExpiredAt: ts, ExpiresAt: ts, Status: marketingdomain.GrantExpired}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"","discount_value_cents":null,"discount_percent":null,"min_charge_cents":0,"status":"expired","expired_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:04:05Z","usable":false}`},
	})

	assertEach(t, []struct {
		Name  string
		Value marketing.AdminCouponRow
		Want  string
	}{
		{"券模板_active_发放记录无此值", marketing.AdminCouponRow{Status: marketing.CouponActive}, `{"id":0,"name":"","discount_type":"","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"active","start_at":null,"end_at":null}`},
		{"券模板_disabled_发放记录无此值", marketing.AdminCouponRow{Status: marketing.CouponDisabled}, `{"id":0,"name":"","discount_type":"","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"disabled","start_at":null,"end_at":null}`},
	})

	assertEach(t, []struct {
		Name  string
		Value marketing.AdminActivityRuleRow
		Want  string
	}{
		{"活动规则_active_发放记录无此值", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, Status: marketing.ActivityActive}, `{"id":0,"name":"","trigger_type":"","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"active","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
		{"活动规则_disabled_发放记录无此值", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, Status: marketing.ActivityDisabled}, `{"id":0,"name":"","trigger_type":"","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"disabled","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
	})
}

// TestMarketingSameNameTriggerAndGrantSourceInviteReward 锁死 invite_reward 这一取值串
// 同时出现在两个不同含义的类型里：
//   - marketing.TriggerType 的 invite_reward：coupon_activity_rule.trigger_type，描述「因何触发活动」；
//   - marketingdomain.GrantSource 的 invite_reward：coupon_grant.grant_source，描述「券因何发放」。
//
// 两者序列化后是同一个字符串，合并成单一常量会让「活动触发类型」与「发放来源」互相赋值。
func TestMarketingSameNameTriggerAndGrantSourceInviteReward(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value marketing.AdminActivityRuleRow
		Want  string
	}{
		{"触发类型_invite_reward", marketing.AdminActivityRuleRow{StartAt: ts, CreatedAt: ts, TriggerType: marketing.TriggerInviteReward}, `{"id":0,"name":"","trigger_type":"invite_reward","coupon_id":0,"coupon_name":"","inviter_coupon_id":null,"threshold_cents":0,"max_grants":0,"per_user_limit":0,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null,"created_at":"2026-01-02T03:04:05Z","granted_count":0}`},
	})

	assertEach(t, []struct {
		Name  string
		Value marketingdomain.GrantSource
		Want  string
	}{
		{"发放来源_invite_reward", marketingdomain.GrantSourceInviteReward, `"invite_reward"`},
		{"发放来源_invite_与_invite_reward_是两个值", marketingdomain.GrantSourceInvite, `"invite"`},
	})
}

// TestCardStatus 锁定 card.CardStatus（online_card.status）的 4 个取值，
// 载体是真实的 card.OnlineCard（用户与后台共用的在线卡响应）。
func TestCardStatus(t *testing.T) {
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
}

// TestCardOperationKindAndStatus 锁定 domain/card.CardOperationKind（2 值）与
// CardOperationStatus（3 值），载体是服务间契约 contracts/central.CardOperationReply
// （gateway 刷卡后 central 返回的操作回执）。
//
// 这里的 kind 与 status 与 protocol.CommandKind 的 "start"、money 的各种状态都会撞词，
// 放在**服务间契约**上断言，是对外字节的最强形态。
func TestCardOperationKindAndStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value central.CardOperationReply
		Want  string
	}{
		{"kind_start", central.CardOperationReply{Kind: carddomain.CardKindStart}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"start","price_cents":0,"minutes":0,"status":""}`},
		{"kind_extend", central.CardOperationReply{Kind: carddomain.CardKindExtend}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"extend","price_cents":0,"minutes":0,"status":""}`},
		{"status_confirming", central.CardOperationReply{Status: carddomain.CardStatusConfirming}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"confirming"}`},
		{"status_confirmed", central.CardOperationReply{Status: carddomain.CardStatusConfirmed}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"confirmed"}`},
		{"status_failed", central.CardOperationReply{Status: carddomain.CardStatusFailed}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"failed"}`},
	})
}

// TestCardSameNameEntityStatusVsOperationStatus 锁死两张卡状态表的词表完全不相交：
// online_card.status 是 active/lost/disabled/unbound（描述「一张实体卡当前可用性」），
// card_operation.status 是 confirming/confirmed/failed（描述「一次刷卡操作的执行结论」）。
//
// 注意命名陷阱：domain/card 里的常量叫 CardStatusConfirming/Confirmed/Failed，
// 与 card.CardStatus 同名前缀却完全不同的含义。这条用例把两个真实载体并排断言。
func TestCardSameNameEntityStatusVsOperationStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value card.OnlineCard
		Want  string
	}{
		{"实体卡_active", card.OnlineCard{Status: card.CardActive}, `{"id":0,"card_no":"","user_id":"0","status":"active"}`},
		{"实体卡_lost", card.OnlineCard{Status: card.CardLost}, `{"id":0,"card_no":"","user_id":"0","status":"lost"}`},
		{"实体卡_disabled", card.OnlineCard{Status: card.CardDisabled}, `{"id":0,"card_no":"","user_id":"0","status":"disabled"}`},
		{"实体卡_unbound", card.OnlineCard{Status: card.CardUnbound}, `{"id":0,"card_no":"","user_id":"0","status":"unbound"}`},
	})

	assertEach(t, []struct {
		Name  string
		Value central.CardOperationReply
		Want  string
	}{
		{"刷卡操作_confirming", central.CardOperationReply{Status: carddomain.CardStatusConfirming}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"confirming"}`},
		{"刷卡操作_confirmed", central.CardOperationReply{Status: carddomain.CardStatusConfirmed}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"confirmed"}`},
		{"刷卡操作_failed", central.CardOperationReply{Status: carddomain.CardStatusFailed}, `{"operation_id":"","device_id":"","event_id":"","charge_order_id":0,"card_id":0,"kind":"","price_cents":0,"minutes":0,"status":"failed"}`},
	})
}

// TestOpsAnnouncementStatusAndScope 锁定 ops.AnnouncementStatus（3 值）与
// ops.AnnouncementScope（3 值），载体是真实的 ops.AnnouncementRow（后台公告列表行）。
//
// 这条同时锁住 target_ids 的两种形状：nil 的 json.RawMessage 序列化为 null，
// 原始 JSON 数组原样透传（不被再编码、不加引号）。
func TestOpsAnnouncementStatusAndScope(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value ops.AnnouncementRow
		Want  string
	}{
		{"status_draft", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeGlobal, Status: ops.AnnouncementDraft}, `{"id":0,"title":"","content":"","scope":"global","target_ids":null,"status":"draft","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"status_published", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeStation, TargetIDs: json.RawMessage(`[1,2]`), Status: ops.AnnouncementPublished}, `{"id":0,"title":"","content":"","scope":"station","target_ids":[1,2],"status":"published","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"status_expired_当前无写入路径", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeCity, Status: ops.AnnouncementExpired}, `{"id":0,"title":"","content":"","scope":"city","target_ids":null,"status":"expired","start_at":"2026-01-02T03:04:05Z","end_at":null}`},

		{"scope_global", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeGlobal}, `{"id":0,"title":"","content":"","scope":"global","target_ids":null,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"scope_station", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeStation, TargetIDs: json.RawMessage(`[7]`)}, `{"id":0,"title":"","content":"","scope":"station","target_ids":[7],"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"scope_city", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeCity, TargetIDs: json.RawMessage(`["320100"]`)}, `{"id":0,"title":"","content":"","scope":"city","target_ids":["320100"],"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null}`},

		// 公告写入请求：scope 与 status 同样对外暴露，起止时刻是 RFC3339 字符串而非 time.Time。
		{"公告写入_scope_station_带目标", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeStation, TargetIDs: json.RawMessage(`["7"]`)}, `{"id":0,"title":"","content":"","scope":"station","target_ids":["7"],"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"公告写入_scope_city_带目标", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeCity, TargetIDs: json.RawMessage(`["320100"]`)}, `{"id":0,"title":"","content":"","scope":"city","target_ids":["320100"],"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
	})
}

// TestOpsAnnouncementInputScope 锁定公告写入请求 ops.AnnouncementInput 的 scope 三值，
// 并锁住两个易错形状：TargetIDs 是字符串数组（不是数字），
// StartAt 是 time.Time 而不是 ActivityInput 里那种 RFC3339 字符串。
func TestOpsAnnouncementInputScope(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value ops.AnnouncementInput
		Want  string
	}{
		{"写入_global_无目标", ops.AnnouncementInput{Scope: ops.AnnouncementScopeGlobal, StartAt: ts}, `{"title":"","content":"","scope":"global","target_ids":null,"start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"写入_station_带站点目标", ops.AnnouncementInput{Scope: ops.AnnouncementScopeStation, TargetIDs: []string{"7"}, StartAt: ts}, `{"title":"","content":"","scope":"station","target_ids":["7"],"start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"写入_city_带城市目标", ops.AnnouncementInput{Scope: ops.AnnouncementScopeCity, TargetIDs: []string{"320100"}, StartAt: ts, EndAt: ptr(ts)}, `{"title":"","content":"","scope":"city","target_ids":["320100"],"start_at":"2026-01-02T03:04:05Z","end_at":"2026-01-02T03:04:05Z"}`},
	})
}

// TestOpsAnnouncementScopeVsDataScopeType 锁死两个都叫「范围类型」的词表：
// ops.AnnouncementScope 是 global/station/city（公告面向谁），
// operations.ScopeType 是 station/vendor（后台账号的数据范围维度）。
// 两者只在 "station" 上撞词，其余取值互不相同。
func TestOpsAnnouncementScopeVsDataScopeType(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value ops.AnnouncementRow
		Want  string
	}{
		{"公告范围_global", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeGlobal}, `{"id":0,"title":"","content":"","scope":"global","target_ids":null,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"公告范围_station", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeStation}, `{"id":0,"title":"","content":"","scope":"station","target_ids":null,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
		{"公告范围_city", ops.AnnouncementRow{StartAt: ts, Scope: ops.AnnouncementScopeCity}, `{"id":0,"title":"","content":"","scope":"city","target_ids":null,"status":"","start_at":"2026-01-02T03:04:05Z","end_at":null}`},
	})

	// DataScope 是真实结构体但**没有 json 标签**（字段直接用 Go 名输出），
	// 因此输出的是 AdminUserID/ScopeType 这样的键名。effectiveScope 保存导出范围时
	// 刻意把 ScopeType/ScopeID 写成零值，导出文件里不得出现具体范围类型。
	assertEach(t, []struct {
		Name  string
		Value operationsdomain.DataScope
		Want  string
	}{
		{"数据范围_station", operationsdomain.DataScope{AdminUserID: 1, ScopeType: operationsdomain.ScopeStation, ScopeID: 7, StationIDs: []uint64{7}}, `{"AdminUserID":1,"ScopeType":"station","ScopeID":7,"StationIDs":[7],"VendorIDs":null,"Unrestricted":false}`},
		{"数据范围_vendor", operationsdomain.DataScope{AdminUserID: 1, ScopeType: operationsdomain.ScopeVendor, ScopeID: 8, VendorIDs: []uint64{8}}, `{"AdminUserID":1,"ScopeType":"vendor","ScopeID":8,"StationIDs":null,"VendorIDs":[8],"Unrestricted":false}`},
		{"数据范围_不受限_不携带类型", operationsdomain.DataScope{AdminUserID: 1, Unrestricted: true}, `{"AdminUserID":1,"ScopeType":"","ScopeID":0,"StationIDs":null,"VendorIDs":null,"Unrestricted":true}`},
	})
}

// TestOpsAdminUserStatus 锁定 ops.AdminUserStatus（admin_user.status）的 3 个取值。
// 载体是两个真实的对外/审计结构体：ops.AdminUserRow（后台账号列表行）与
// ops.AdminUserSnapshot（账号变更审计快照）。
func TestOpsAdminUserStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value ops.AdminUserRow
		Want  string
	}{
		{"active", ops.AdminUserRow{Status: ops.AdminUserActive}, `{"id":0,"username":"","display_name":null,"role_id":null,"role_code":null,"role_name":null,"phone":null,"email":null,"status":"active","mfa_enabled":false,"last_login_at":null,"locked_until":null,"failed_login_count":0}`},
		{"disabled", ops.AdminUserRow{Status: ops.AdminUserDisabled}, `{"id":0,"username":"","display_name":null,"role_id":null,"role_code":null,"role_name":null,"phone":null,"email":null,"status":"disabled","mfa_enabled":false,"last_login_at":null,"locked_until":null,"failed_login_count":0}`},
		{"locked", ops.AdminUserRow{Status: ops.AdminUserLocked}, `{"id":0,"username":"","display_name":null,"role_id":null,"role_code":null,"role_name":null,"phone":null,"email":null,"status":"locked","mfa_enabled":false,"last_login_at":null,"locked_until":null,"failed_login_count":0}`},
	})

	assertEach(t, []struct {
		Name  string
		Value ops.AdminUserSnapshot
		Want  string
	}{
		{"审计快照_active", ops.AdminUserSnapshot{Status: ops.AdminUserActive}, `{"id":0,"username":"","display_name":null,"role_id":null,"phone":null,"email":null,"status":"active","mfa_enabled":false,"last_login_at":null,"locked_until":null,"failed_login_count":0,"auth_version":0,"deleted_at":null,"deleted_by":null}`},
		{"审计快照_disabled", ops.AdminUserSnapshot{Status: ops.AdminUserDisabled}, `{"id":0,"username":"","display_name":null,"role_id":null,"phone":null,"email":null,"status":"disabled","mfa_enabled":false,"last_login_at":null,"locked_until":null,"failed_login_count":0,"auth_version":0,"deleted_at":null,"deleted_by":null}`},
		{"审计快照_locked", ops.AdminUserSnapshot{Status: ops.AdminUserLocked}, `{"id":0,"username":"","display_name":null,"role_id":null,"phone":null,"email":null,"status":"locked","mfa_enabled":false,"last_login_at":null,"locked_until":null,"failed_login_count":0,"auth_version":0,"deleted_at":null,"deleted_by":null}`},
	})
}

// TestOpsOutboxStatus 锁定 ops.OutboxStatus（三张 outbox 表共用的 3 值词表）。
//
// 载体是真实的 ops.EventOutboxRecord，但它**没有 json 标签**——字段按 Go 名输出，
// Status 是 *OutboxStatus 指针。nil 序列化为 null（留给数据库 default 填 pending），
// 非 nil 输出裸字符串。这一条把「指针 + 无标签」的形状锁死，防止有人给该模型补标签。
func TestOpsOutboxStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value ops.EventOutboxRecord
		Want  string
	}{
		{"nil_留给数据库_default", ops.EventOutboxRecord{}, `{"ID":0,"EventID":"","Stream":"","EnvelopeJSON":null,"Status":null,"RetryCount":0,"LastError":{"String":"","Valid":false},"ScheduledAt":null,"PublishedAt":null}`},
		{"pending", ops.EventOutboxRecord{Status: ptr(ops.OutboxPending)}, `{"ID":0,"EventID":"","Stream":"","EnvelopeJSON":null,"Status":"pending","RetryCount":0,"LastError":{"String":"","Valid":false},"ScheduledAt":null,"PublishedAt":null}`},
		{"published", ops.EventOutboxRecord{Status: ptr(ops.OutboxPublished)}, `{"ID":0,"EventID":"","Stream":"","EnvelopeJSON":null,"Status":"published","RetryCount":0,"LastError":{"String":"","Valid":false},"ScheduledAt":null,"PublishedAt":null}`},
		{"failed", ops.EventOutboxRecord{Status: ptr(ops.OutboxFailed)}, `{"ID":0,"EventID":"","Stream":"","EnvelopeJSON":null,"Status":"failed","RetryCount":0,"LastError":{"String":"","Valid":false},"ScheduledAt":null,"PublishedAt":null}`},
	})
}

// TestIdentityUserStatusAndGender 锁定 domain/identity.UserStatus（2 值）与
// Gender（3 值），载体是真实的 identity.ChargeUserRow（后台充电用户列表行）。
//
// 这条同时锁住 id / inviter_id 的 `,string` 形状：都是字符串而不是数字。
func TestIdentityUserStatusAndGender(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value identity.ChargeUserRow
		Want  string
	}{
		{"status_active", identity.ChargeUserRow{FirstSeen: ts, CreatedAt: ts, Status: identitydomain.UserActive}, `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"","status":"active","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null}`},
		{"status_frozen", identity.ChargeUserRow{FirstSeen: ts, CreatedAt: ts, Status: identitydomain.UserFrozen}, `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"","status":"frozen","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null}`},

		{"gender_unknown_数据库默认值", identity.ChargeUserRow{FirstSeen: ts, CreatedAt: ts, Gender: identitydomain.GenderUnknown}, `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"unknown","status":"","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null}`},
		{"gender_male", identity.ChargeUserRow{FirstSeen: ts, CreatedAt: ts, Gender: identitydomain.GenderMale}, `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"male","status":"","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null}`},
		{"gender_female", identity.ChargeUserRow{FirstSeen: ts, CreatedAt: ts, Gender: identitydomain.GenderFemale}, `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"female","status":"","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null}`},
	})
}

// TestIdentitySameNameUserStatusVsWalletAccountStatus 锁死两张表的 status 取值串
// **逐字相同**（active/frozen）却描述不同对象：
//   - identitydomain.UserStatus：user.status，账号本身；
//   - walletdomain.AccountStatus：wallet_account.status，钱包账户。
//
// 恢复钱包时同一行会同时判定两者，两者不得互相赋值。载体用真实的
// identity.ChargeUserDetail——它在同一条响应里同时输出 status 与 wallet_status。
func TestIdentitySameNameUserStatusVsWalletAccountStatus(t *testing.T) {
	detail := func(s identitydomain.UserStatus, w walletdomain.AccountStatus) identity.ChargeUserDetail {
		return identity.ChargeUserDetail{
			ChargeUserRow: identity.ChargeUserRow{FirstSeen: ts, CreatedAt: ts, Status: s},
			WalletStatus:  w,
		}
	}

	assertEach(t, []struct {
		Name  string
		Value identity.ChargeUserDetail
		Want  string
	}{
		{"账号active_钱包active", detail(identitydomain.UserActive, walletdomain.AccountActive), `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"","status":"active","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null,"wallet_status":"active","recent_orders":null,"coupon_granted":0,"coupon_unused":0,"fault_reports":0}`},
		{"账号active_钱包frozen_两列可不同", detail(identitydomain.UserActive, walletdomain.AccountFrozen), `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"","status":"active","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null,"wallet_status":"frozen","recent_orders":null,"coupon_granted":0,"coupon_unused":0,"fault_reports":0}`},
		{"账号frozen_钱包active_两列可不同", detail(identitydomain.UserFrozen, walletdomain.AccountActive), `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"","status":"frozen","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null,"wallet_status":"active","recent_orders":null,"coupon_granted":0,"coupon_unused":0,"fault_reports":0}`},
		{"账号frozen_钱包frozen", detail(identitydomain.UserFrozen, walletdomain.AccountFrozen), `{"id":"0","openid":"","union_id":null,"nickname":null,"avatar_url":null,"gender":"","status":"frozen","first_seen_at":"2026-01-02T03:04:05Z","last_login_at":null,"inviter_id":null,"created_at":"2026-01-02T03:04:05Z","phone":"","phone_bound":false,"balance_cents":0,"frozen_cents":0,"order_count":0,"total_cents":0,"last_order_at":null,"wallet_status":"frozen","recent_orders":null,"coupon_granted":0,"coupon_unused":0,"fault_reports":0}`},
	})
}

// TestOrderFeedbackCategory 锁定 order.FeedbackCategory（feedback.category）的 3 个取值，
// 载体是真实的 order.FeedbackInput（C 端提交反馈的请求体）。
//
// 这条同时锁住 images 的两种形状：nil 输出 null（写库保留 JSON null），空切片输出 []。
func TestOrderFeedbackCategory(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value order.FeedbackInput
		Want  string
	}{
		{"rating", order.FeedbackInput{Rating: 5, Category: order.FeedbackCategoryRating}, `{"rating":5,"category":"rating","content":null,"images":null}`},
		{"complaint", order.FeedbackInput{Rating: 1, Category: order.FeedbackCategoryComplaint}, `{"rating":1,"category":"complaint","content":null,"images":null}`},
		{"suggestion", order.FeedbackInput{Rating: 3, Category: order.FeedbackCategorySuggestion}, `{"rating":3,"category":"suggestion","content":null,"images":null}`},
		{"rating_空附件输出空数组", order.FeedbackInput{Rating: 4, Category: order.FeedbackCategoryRating, Content: ptr("很好"), Images: []string{}}, `{"rating":4,"category":"rating","content":"很好","images":[]}`},
		{"complaint_带附件", order.FeedbackInput{Rating: 2, Category: order.FeedbackCategoryComplaint, Images: []string{"https://example.com/a.png"}}, `{"rating":2,"category":"complaint","content":null,"images":["https://example.com/a.png"]}`},
	})
}

// TestOrderFeedbackStatus 锁定 order.FeedbackStatus（feedback.status）的 3 个取值。
//
// 载体说明：feedback.status 在读取路径上是 map[string]any 的数据库行（List 返回
// `[]map[string]any`），没有带该字段的结构体；写路径也只有 map[string]any。
// 因此按「不为测试另造同形结构体」的规则，直接断言这个**真实枚举类型**的序列化字节。
func TestOrderFeedbackStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value order.FeedbackStatus
		Want  string
	}{
		{"pending", order.FeedbackPending, `"pending"`},
		{"processed", order.FeedbackProcessed, `"processed"`},
		{"closed", order.FeedbackClosed, `"closed"`},
	})
}

// TestPricingTemplateStatus 锁定 pricing.PricingTemplateStatus（pricing_template.status）
// 的 2 个取值，载体是真实的 pricing.SchemeView（后台计费模板列表）与
// pricing.ChangeSchemeInput（启停/删除请求体）。
func TestPricingTemplateStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value pricing.SchemeView
		Want  string
	}{
		{"active", pricing.SchemeView{Status: pricing.PricingTemplateActive}, `{"id":0,"scheme":null,"version":0,"status":"active"}`},
		{"disabled", pricing.SchemeView{Status: pricing.PricingTemplateDisabled}, `{"id":0,"scheme":null,"version":0,"status":"disabled"}`},
	})

	assertEach(t, []struct {
		Name  string
		Value pricing.ChangeSchemeInput
		Want  string
	}{
		{"写入_active", pricing.ChangeSchemeInput{Status: pricing.PricingTemplateActive}, `{"expected_version":0,"status":"active"}`},
		{"写入_disabled", pricing.ChangeSchemeInput{Status: pricing.PricingTemplateDisabled}, `{"expected_version":0,"status":"disabled"}`},
	})
}

// TestPricingSameNameRuleStatusVsTemplateStatus 锁死 pricing_rule.status 与
// pricing_template.status 词表相同（active/disabled）但分属不同表的两个独立列，
// 状态各自变化、不得互相赋值。pricing.PricingTemplateRow 只有 gorm 标签没有 json 标签，
// 因此规则状态用真实的枚举类型本身断言。
func TestPricingSameNameRuleStatusVsTemplateStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value pricing.SchemeView
		Want  string
	}{
		{"模板_active", pricing.SchemeView{Status: pricing.PricingTemplateActive}, `{"id":0,"scheme":null,"version":0,"status":"active"}`},
		{"模板_disabled", pricing.SchemeView{Status: pricing.PricingTemplateDisabled}, `{"id":0,"scheme":null,"version":0,"status":"disabled"}`},
	})

	assertEach(t, []struct {
		Name  string
		Value pricing.PricingRuleStatus
		Want  string
	}{
		{"规则_active", pricing.PricingRuleActive, `"active"`},
		{"规则_disabled", pricing.PricingRuleDisabled, `"disabled"`},
	})

	// PricingTemplateRow 没有 json 标签，字段按 Go 名输出；
	// 发布路径只写 PricingRuleActive，停用旧版本写 PricingRuleDisabled。
	assertJSON(t, pricing.PricingTemplateRow{Status: pricing.PricingTemplateActive}, `{"ID":0,"Name":"","Remark":"","SpecJSON":null,"DisplayJSON":null,"Status":"active","Version":0}`)
}

// TestExportStatus 锁定 export.ExportStatus（export_task.status）的 5 个取值，
// 载体是真实的 export.ExportTaskRow（后台导出任务列表行）。
//
// 这条同时锁住「服务端文件路径不外泄」：file_path 是 json:"-"，不参与输出。
func TestExportStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value export.ExportTaskRow
		Want  string
	}{
		{"pending", export.ExportTaskRow{Status: export.ExportStatusPending}, `{"id":0,"task_no":"","resource":"","file_format":"","status":"pending","requested_by":0,"filter_json":null,"row_count":0,"error_msg":null,"expires_at":null,"created_at":"","completed_at":null}`},
		{"running", export.ExportTaskRow{Status: export.ExportStatusRunning}, `{"id":0,"task_no":"","resource":"","file_format":"","status":"running","requested_by":0,"filter_json":null,"row_count":0,"error_msg":null,"expires_at":null,"created_at":"","completed_at":null}`},
		{"completed", export.ExportTaskRow{Status: export.ExportStatusCompleted}, `{"id":0,"task_no":"","resource":"","file_format":"","status":"completed","requested_by":0,"filter_json":null,"row_count":0,"error_msg":null,"expires_at":null,"created_at":"","completed_at":null}`},
		{"failed", export.ExportTaskRow{Status: export.ExportStatusFailed, ErrorMsg: ptr("生成失败")}, `{"id":0,"task_no":"","resource":"","file_format":"","status":"failed","requested_by":0,"filter_json":null,"row_count":0,"error_msg":"生成失败","expires_at":null,"created_at":"","completed_at":null}`},
		{"expired", export.ExportTaskRow{Status: export.ExportStatusExpired}, `{"id":0,"task_no":"","resource":"","file_format":"","status":"expired","requested_by":0,"filter_json":null,"row_count":0,"error_msg":null,"expires_at":null,"created_at":"","completed_at":null}`},
	})
}

// TestExportFileFormat 锁定 export.ExportFileFormat（export_task.file_format）的 2 个合法取值。
//
// 额外锁住 LegacyExportFormatPDF：它是同一个 Go 类型的取值，但不在数据库 ENUM 约束内，
// 序列化后仍是 "pdf"。把它单列一条，是为了让「历史遗留格式不计入 Known()」这件事
// 在对外字节上也可见——将来若有人把它塞进合法集合，这条会与 TestExportStatus 的
// csv/xlsx 形状对照着看。
func TestExportFileFormat(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value export.ExportTaskRow
		Want  string
	}{
		{"csv", export.ExportTaskRow{FileFormat: export.ExportFormatCSV}, `{"id":0,"task_no":"","resource":"","file_format":"csv","status":"","requested_by":0,"filter_json":null,"row_count":0,"error_msg":null,"expires_at":null,"created_at":"","completed_at":null}`},
		{"xlsx", export.ExportTaskRow{FileFormat: export.ExportFormatXLSX}, `{"id":0,"task_no":"","resource":"","file_format":"xlsx","status":"","requested_by":0,"filter_json":null,"row_count":0,"error_msg":null,"expires_at":null,"created_at":"","completed_at":null}`},
		{"pdf_历史遗留_不在_ENUM_内", export.ExportTaskRow{FileFormat: export.LegacyExportFormatPDF}, `{"id":0,"task_no":"","resource":"","file_format":"pdf","status":"","requested_by":0,"filter_json":null,"row_count":0,"error_msg":null,"expires_at":null,"created_at":"","completed_at":null}`},
	})
}

// TestOpsAndExportPendingVsActiveStatuses 把「pending」这一在多个词表里都出现的取值放在一起，
// 说明它在不同表代表不同含义：event_outbox 的 pending 是待投递，
// export_task 的 pending 是已受理待生成，feedback 的 pending 是待处理。
// 三者不得因为字面相同就共用一个类型。
func TestOpsAndExportPendingVsActiveStatuses(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value ops.EventOutboxRecord
		Want  string
	}{
		{"outbox_pending_待投递", ops.EventOutboxRecord{Status: ptr(ops.OutboxPending)}, `{"ID":0,"EventID":"","Stream":"","EnvelopeJSON":null,"Status":"pending","RetryCount":0,"LastError":{"String":"","Valid":false},"ScheduledAt":null,"PublishedAt":null}`},
	})

	assertEach(t, []struct {
		Name  string
		Value export.ExportTaskRow
		Want  string
	}{
		{"export_pending_待生成", export.ExportTaskRow{Status: export.ExportStatusPending}, `{"id":0,"task_no":"","resource":"","file_format":"","status":"pending","requested_by":0,"filter_json":null,"row_count":0,"error_msg":null,"expires_at":null,"created_at":"","completed_at":null}`},
	})

	assertEach(t, []struct {
		Name  string
		Value order.FeedbackStatus
		Want  string
	}{
		{"feedback_pending_待处理", order.FeedbackPending, `"pending"`},
	})

	// failed 同样跨三张表：outbox 的 failed 待重试、export 的 failed 已记录失败因、
	// feedback 没有 failed 取值。
	assertEach(t, []struct {
		Name  string
		Value order.FeedbackStatus
		Want  string
	}{
		{"feedback_没有_failed", order.FeedbackStatus("failed"), `"failed"`},
	})
}
