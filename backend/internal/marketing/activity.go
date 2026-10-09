package marketing

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"context"
	"gorm.io/gorm"
	"time"
)

// ActivityInput 是活动请求参数，时间文本在用例中统一解析。
type ActivityInput struct {
	Name            string  `json:"name"`              // 非空活动名称，最多128字
	TriggerType     string  `json:"trigger_type"`      // first_recharge、invite_reward、threshold_redeem 或 holiday
	CouponID        uint64  `json:"coupon_id"`         // 奖励券模板，须未删除且启用
	InviterCouponID *uint64 `json:"inviter_coupon_id"` // 邀请人模板，邀请有奖必填，其他类型可空
	ThresholdCents  int64   `json:"threshold_cents"`   // 门槛金额，满减为正，其他既定类型按既定规则校验
	MaxGrants       int     `json:"max_grants"`        // 活动来源总量，0 表示不限量
	PerUserLimit    int     `json:"per_user_limit"`    // 单人配置，0沿用默认一份
	Status          string  `json:"status"`            // active 或 disabled；空值补 active
	StartAt         string  `json:"start_at"`          // RFC3339 开始时刻
	EndAt           string  `json:"end_at"`            // RFC3339 结束时刻，晚于开始且不超过一年
}

// ActivityView 是活动查询结果，预算按优惠券和发放来源统计。
type ActivityView struct {
	ID              uint64     `json:"id"`                // 既定规则主键
	Name            string     `json:"name"`              // 活动名称
	TriggerType     string     `json:"trigger_type"`      // 固定触发类型
	CouponID        uint64     `json:"coupon_id"`         // 奖励券主键
	CouponName      string     `json:"coupon_name"`       // 关联模板名称
	InviterCouponID *uint64    `json:"inviter_coupon_id"` // 可选邀请人奖励券主键
	ThresholdCents  int64      `json:"threshold_cents"`   // 门槛，分
	MaxGrants       int        `json:"max_grants"`        // 活动来源总量上限，0 表示不限量
	PerUserLimit    int        `json:"per_user_limit"`    // 单人份数配置，0由活动引擎归一为1
	Status          string     `json:"status"`            // active 或 disabled
	StartAt         time.Time  `json:"start_at"`          // 开始时刻
	EndAt           *time.Time `json:"end_at"`            // 结束时刻，可空兼容历史记录
	CreatedAt       time.Time  `json:"created_at"`        // 创建时刻
	GrantedCount    int64      `json:"granted_count"`     // 券的活动／邀请来源累计数
}

// ActivityService 负责活动配置校验和同事务审计。
type ActivityService struct {
	DB  *gorm.DB         // 所属中央连接或调用方事务，由进程管理
	Now func() time.Time // 可选受控时钟，空值使用当前 UTC
}

// normalize 校验活动请求并将时间转换为 UTC 写入参数。
func normalize(in ActivityInput, now time.Time) (AdminActivityRuleInput, ActivityInput, error) {
	start, end, status, err := NormalizeActivity(ActivityRule(in), now)
	if err != nil {
		return AdminActivityRuleInput{}, in, &InputError{"活动规则无效：名称不能为空，门槛为非负分，窗口须为 RFC3339 且结束晚于开始，满减须设门槛，邀请有奖须设邀请人券"}
	}
	in.Status = status
	return AdminActivityRuleInput{Name: in.Name, TriggerType: in.TriggerType, CouponID: in.CouponID, InviterCouponID: in.InviterCouponID, ThresholdCents: in.ThresholdCents, MaxGrants: in.MaxGrants, PerUserLimit: in.PerUserLimit, Status: status, StartAt: start, EndAt: end}, in, nil
}

// now 返回服务使用的 UTC 时间，测试可替换时钟。
func (s ActivityService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// List 核对 coupon.activity.read 权限后分页查询规则，批量补充券名称和发放数量。
func (s ActivityService) List(ctx context.Context, actor operations.Profile, q persist.PageQuery) (persist.Page[ActivityView], error) {
	out := persist.Page[ActivityView]{Items: []ActivityView{}, Page: q.Page, PageSize: q.PageSize}
	if !actor.HasPermission("coupon.activity.read") {
		return out, operations.ErrForbidden
	}
	if q.Page < 1 || q.Page > 100000 || q.PageSize < 1 || q.PageSize > 100 || (q.Status != "" && q.Status != "active" && q.Status != "disabled") {
		return out, &InputError{"分页或活动状态无效"}
	}
	rows, total, err := (AdminActivityRepo{DB: s.DB}).ListActivityRules(ctx, q.Status, q.Keyword, q.Page, q.PageSize)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		out.Items = append(out.Items, ActivityView(row))
	}
	out.Total = total
	return out, nil
}

// Create 核对活动管理权限和奖励券状态后保存规则及审计。
// 奖励券用当前读；不存在或停用时拒绝，数据库故障不能当作没有券。
func (s ActivityService) Create(ctx context.Context, audit ops.AuditContext, in ActivityInput) (uint64, error) {
	if !audit.Actor.HasPermission("coupon.activity.manage") {
		return 0, operations.ErrForbidden
	}
	input, normalized, err := normalize(in, s.now())
	if err != nil {
		return 0, err
	}
	var id uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		id, err = (AdminActivityRepo{DB: tx}).CreateActivityRule(ctx, input)
		if err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "create", Target: "coupon_activity", ID: id, After: normalized})
	})
	return id, err
}

// Update 核对管理权限后更新活动，触发类型保持不变。
// 先锁规则，再按券主键顺序检查奖励；更新和审计同事务，失败整体回滚。
func (s ActivityService) Update(ctx context.Context, audit ops.AuditContext, id uint64, in ActivityInput) error {
	if !audit.Actor.HasPermission("coupon.activity.manage") {
		return operations.ErrForbidden
	}
	if id == 0 {
		return &InputError{"活动规则无效"}
	}
	input, normalized, err := normalize(in, s.now())
	if err != nil {
		return &InputError{"活动规则无效"}
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before, err := (AdminActivityRepo{DB: tx}).UpdateActivityRule(ctx, id, input)
		if err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "update", Target: "coupon_activity", ID: id, Before: ActivityView(before), After: normalized})
	})
}
