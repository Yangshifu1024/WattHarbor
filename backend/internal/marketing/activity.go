package marketing

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"context"
	"gorm.io/gorm"
	"time"
)

// ActivityInput 是活动的业务请求，不复用持久列模型；原时刻文本在用例内统一解析。
type ActivityInput struct {
	Name            string  `json:"name"`              // 非空活动名称，最多128字
	TriggerType     string  `json:"trigger_type"`      // first_recharge、invite_reward、threshold_redeem或holiday
	CouponID        uint64  `json:"coupon_id"`         // 原奖励券模板，须未删除且启用
	InviterCouponID *uint64 `json:"inviter_coupon_id"` // 原邀请人模板，邀请有奖必填，其他类型可空
	ThresholdCents  int64   `json:"threshold_cents"`   // 原门槛分值，满减为正，其他既定类型按原规则校验
	MaxGrants       int     `json:"max_grants"`        // 原活动来源总量，0不限
	PerUserLimit    int     `json:"per_user_limit"`    // 原单人配置，0沿用默认一份
	Status          string  `json:"status"`            // active或disabled；空值补active
	StartAt         string  `json:"start_at"`          // 原RFC3339开始时刻
	EndAt           string  `json:"end_at"`            // 原RFC3339结束时刻，晚于开始且不超过一年
}

// ActivityView 是活动只读模型，不携带ORM映射；原活动预算按既有券／来源计数。
type ActivityView struct {
	ID              uint64     `json:"id"`                // 原规则主键
	Name            string     `json:"name"`              // 原活动名称
	TriggerType     string     `json:"trigger_type"`      // 原固定触发类型
	CouponID        uint64     `json:"coupon_id"`         // 原奖励券主键
	CouponName      string     `json:"coupon_name"`       // 原关联模板名称
	InviterCouponID *uint64    `json:"inviter_coupon_id"` // 原可选邀请人奖励券主键
	ThresholdCents  int64      `json:"threshold_cents"`   // 原门槛，分
	MaxGrants       int        `json:"max_grants"`        // 原活动来源总量上限，0不限
	PerUserLimit    int        `json:"per_user_limit"`    // 原单人份数配置，0由活动引擎归一为1
	Status          string     `json:"status"`            // 原active或disabled
	StartAt         time.Time  `json:"start_at"`          // 原开始时刻
	EndAt           *time.Time `json:"end_at"`            // 原结束时刻，可空兼容历史记录
	CreatedAt       time.Time  `json:"created_at"`        // 原创建时刻
	GrantedCount    int64      `json:"granted_count"`     // 原券的活动／邀请来源累计数
}

// ActivityService 拥有活动配置准入与完整审计事务，不持有支付或结算实现。
type ActivityService struct {
	DB  *gorm.DB         // 所属中央连接或调用方事务，由进程管理
	Now func() time.Time // 可选受控时钟，空值使用当前UTC
}

// normalize 将 in 原请求在 now 下校验并转换为原UTC写入参数，返回输入或公开准入错误，无IO。
func normalize(in ActivityInput, now time.Time) (AdminActivityRuleInput, ActivityInput, error) {
	start, end, status, err := NormalizeActivity(ActivityRule(in), now)
	if err != nil {
		return AdminActivityRuleInput{}, in, &InputError{"活动规则无效：名称不能为空，门槛为非负分，窗口须为 RFC3339 且结束晚于开始，满减须设门槛，邀请有奖须设邀请人券"}
	}
	in.Status = status
	return AdminActivityRuleInput{Name: in.Name, TriggerType: in.TriggerType, CouponID: in.CouponID, InviterCouponID: in.InviterCouponID, ThresholdCents: in.ThresholdCents, MaxGrants: in.MaxGrants, PerUserLimit: in.PerUserLimit, Status: status, StartAt: start, EndAt: end}, in, nil
}

// now 返回 s 的受控UTC时刻，无IO。
func (s ActivityService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// List 在 ctx 下按 actor 原coupon.activity.read权限及 q 已校验分页读取规则，返回原页或禁止／存储错误。
// 名称、券投影与发放计数保持既有批量读取，不执行活动或资金写入。
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

// Create 在 ctx 下以 audit 原活动管理权限创建 in 规则，返回原主键或输入／存储错误。
// 奖励券当前读、活动插入与审计共用 tx；不存在／停用为准入错误，存储故障不能伪装成无券。
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

// Update 在 ctx 下以 audit 原活动管理权限更新 id 的 in 配置，返回输入／禁止／缺失／存储错误。
// 保留原触发类型；规则锁后按券主键顺序核对奖励，更新与审计同事务，失败全部回滚。
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
