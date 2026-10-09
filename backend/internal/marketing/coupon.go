package marketing

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/identity"
	"chargepilot/internal/ops"
	"context"
	"errors"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"time"
)

// InputError 表示营销业务输入不满足既定准入；存储故障不能包装成输入错误。
type InputError struct {
	Message string // 原公开准入文案
}

// Error 返回 e 的公开文案，无IO。
func (e *InputError) Error() string { return e.Message }

// Coupon 是券模板业务输入与读模型，不包含ORM映射；优惠三种口径互斥。
type Coupon struct {
	ID                 uint64           `json:"id"`                   // 原券主键，创建时必须为0
	Name               string           `json:"name"`                 // 非空名称，最多128字
	DiscountType       string           `json:"discount_type"`        // amount、percentage或time_free
	DiscountValueCents *int64           `json:"discount_value_cents"` // 满减分值，其他口径为空
	DiscountPercent    *decimal.Decimal `json:"discount_percent"`     // 原两位小数百分比，严格在0与100之间
	FreeMinutes        *uint32          `json:"free_minutes"`         // 原免费分钟，1–1440，其他口径为空
	MinChargeCents     int64            `json:"min_charge_cents"`     // 原使用门槛，非负分
	ValidHours         uint32           `json:"valid_hours"`          // 原领取后有效小时，1–87600
	TotalQuota         uint32           `json:"total_quota"`          // 原模板总量，0不限
	PerUserQuota       uint32           `json:"per_user_quota"`       // 原人工发放单人上限，必须大于0
	Status             string           `json:"status"`               // active或disabled；创建强制active
	StartAt            *time.Time       `json:"start_at"`             // 原可选开始时刻
	EndAt              *time.Time       `json:"end_at"`               // 原可选结束时刻，须晚于开始
}

// CouponUpdate 是模板已发放后仍允许修改的两个字段，不改变优惠口径及预算。
type CouponUpdate struct {
	Name   string `json:"name"`   // 原模板名称，非空最多128字
	Status string `json:"status"` // active或disabled
}

// GrantInput 保留人工发放的原用户及请求身份，不携带当前设备或付款状态。
type GrantInput struct {
	RequestID string `json:"request_id"`     // 原UUID形状幂等号
	UserID    uint64 `json:"user_id,string"` // 原目标用户主键，审计保持十进制文本
}

// GrantResult 返回原发放主键与请求号，不替换重放身份。
type GrantResult struct {
	CouponGrantID uint64 `json:"coupon_grant_id"` // 原发放主键
	RequestID     string `json:"request_id"`      // 原客户端幂等号
}

// CouponService 拥有模板准入、人工发放及审计事务；权限沿用原全局券管理能力。
type CouponService struct {
	DB *gorm.DB // 所属中央连接或调用方事务，由进程管理
}

// RedeemRef 是调用方冻结支付事实；优惠原价和实付优惠不得在到账时重新解释。
type RedeemRef struct {
	UserID        uint64 // 原付款用户
	CouponGrantID uint64 // 原权益主键
	TotalCents    int64  // 原优惠前费用，分
	DiscountCents int64  // 原冻结优惠，分
	PaymentOrder  uint64 // 原支付主键
}

// Quote 在 ctx 下只读计算 userID 的 grantID 对 totalCents 原价的优惠，返回整分或无券／过期／门槛／存储错误。
// 不核销、不取得事务锁；当前套餐仍不接受组合券。
func (s CouponService) Quote(ctx context.Context, userID, grantID uint64, totalCents int64) (int64, error) {
	grant, err := loadGrant(ctx, s.DB, userID, grantID, false)
	if err != nil {
		return 0, err
	}
	if grant.Status != "unused" || !grant.ExpiresAt.After(time.Now().UTC()) {
		return 0, ErrCouponExhausted
	}
	return discountFor(grant, totalCents)
}

// RedeemInTx 在 tx 原请求context下按 ref／reference 锁定并核销原权益，返回冲突／过期／门槛／存储错误。
// 同一权益及支付凭据重放沿用首次结果；核销优惠须等于支付冻结优惠，不从实付额再算一次。
// 核销与调用方资金事务同提交／回滚，不启动事务或后台任务，不更换原号。
func (s CouponService) RedeemInTx(tx *gorm.DB, ref RedeemRef, reference string) error {
	if tx == nil || ref.UserID == 0 || ref.CouponGrantID == 0 || ref.PaymentOrder == 0 || reference == "" || ref.DiscountCents < 0 || ref.DiscountCents > ref.TotalCents {
		return ErrCouponGrant
	}
	grant, err := loadGrant(tx.Statement.Context, tx, ref.UserID, ref.CouponGrantID, true)
	if err != nil {
		return err
	}
	if grant.Status == "used" {
		if grant.UsedPaymentOrderID == nil || *grant.UsedPaymentOrderID != ref.PaymentOrder {
			return ErrCouponExhausted
		}
		receipt, err := findRedemption(tx, ref.PaymentOrder)
		if err != nil {
			return err
		}
		if receipt.CouponID != grant.CouponID || receipt.UserID != ref.UserID || receipt.Reference != reference || receipt.DiscountCents != ref.DiscountCents {
			return ErrCouponGrant
		}
		return nil
	}
	if grant.Status != "unused" || !grant.ExpiresAt.After(time.Now().UTC()) {
		return ErrCouponExhausted
	}
	discount, err := discountFor(grant, ref.TotalCents)
	if err != nil {
		return err
	}
	if discount != ref.DiscountCents {
		return ErrCouponGrant
	}
	return saveRedemption(tx, grant, ref, reference)
}

// validDiscount 核对 in 的三选一优惠字段与原金额／分钟／小数约束，返回事实，无IO。
func validDiscount(in Coupon) bool {
	switch in.DiscountType {
	case "amount":
		return in.DiscountValueCents != nil && *in.DiscountValueCents > 0 && in.DiscountPercent == nil && in.FreeMinutes == nil
	case "percentage":
		return in.DiscountPercent != nil && in.DiscountPercent.GreaterThan(decimal.Zero) && in.DiscountPercent.LessThan(decimal.NewFromInt(100)) && in.DiscountPercent.Equal(in.DiscountPercent.Round(2)) && in.DiscountValueCents == nil && in.FreeMinutes == nil
	case "time_free":
		return in.FreeMinutes != nil && *in.FreeMinutes > 0 && *in.FreeMinutes <= 1440 && in.DiscountPercent == nil && in.DiscountValueCents == nil
	}
	return false
}

// List 在 ctx 下核对 actor 原coupon.read权限，返回未删除模板的原倒序数组或禁止／存储错误，无写入。
func (s CouponService) List(ctx context.Context, actor operations.Profile) ([]Coupon, error) {
	if !actor.HasPermission("coupon.read") {
		return nil, operations.ErrForbidden
	}
	rows, err := (AdminCouponRepo{DB: s.DB}).ListCoupons(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Coupon, 0, len(rows))
	for _, row := range rows {
		out = append(out, Coupon(row))
	}
	return out, nil
}

// Create 在 ctx 下以 audit 原coupon.create权限校验 in 并创建启用模板，返回原主键模型或输入／存储错误。
// 模板与审计共用一个事务；审计失败全部回滚，不接受调用方主键或创建时停用状态。
func (s CouponService) Create(ctx context.Context, audit ops.AuditContext, in Coupon) (Coupon, error) {
	if !audit.Actor.HasPermission("coupon.create") {
		return Coupon{}, operations.ErrForbidden
	}
	if in.ID != 0 || !operations.ValidText(in.Name, 128) || in.MinChargeCents < 0 || in.ValidHours == 0 || in.ValidHours > 87600 || in.PerUserQuota == 0 || (in.EndAt != nil && in.StartAt != nil && !in.EndAt.After(*in.StartAt)) {
		return Coupon{}, &InputError{"优惠券名称、有效期或额度无效"}
	}
	if !validDiscount(in) {
		return Coupon{}, &InputError{"优惠参数无效：满减分值须大于 0，折扣为 0–100 之间，免费时长为 1–1440 分钟"}
	}
	in.Status = "active"
	row := AdminCouponRow(in)
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := (AdminCouponRepo{DB: tx}).CreateCoupon(ctx, &row); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "create", Target: "coupon", ID: row.ID, After: Coupon(row)})
	})
	return Coupon(row), err
}

// Update 在 ctx 下以 audit 原coupon.update权限将 id 模板改为 in，返回原允许字段或输入／缺失／存储错误。
// 原行锁、修改与审计处于同一事务；额度及优惠字段不接受修改，不在失败后提交模板。
func (s CouponService) Update(ctx context.Context, audit ops.AuditContext, id uint64, in CouponUpdate) (CouponUpdate, error) {
	if !audit.Actor.HasPermission("coupon.update") {
		return in, operations.ErrForbidden
	}
	if id == 0 || !operations.ValidText(in.Name, 128) || (in.Status != "active" && in.Status != "disabled") {
		return in, &InputError{"名称或状态无效"}
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before, err := (AdminCouponRepo{DB: tx}).UpdateCoupon(ctx, id, in.Name, in.Status)
		if err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "update", Target: "coupon", ID: id, Before: Coupon(before), After: in})
	})
	return in, err
}

// Stats 在 ctx 下以 actor 原coupon.read权限读取 id 原模板发放统计，返回原计数或禁止／缺失／存储错误。
// 未用但已到期仍统计为expired，无资金或发放写入。
func (s CouponService) Stats(ctx context.Context, actor operations.Profile, id uint64) (AdminCouponStats, error) {
	if !actor.HasPermission("coupon.read") {
		return AdminCouponStats{}, operations.ErrForbidden
	}
	return (AdminCouponRepo{DB: s.DB}).Stats(ctx, id)
}

// Grant 在 ctx 下以 audit 原coupon.grant权限给 id 模板的 in.UserID 发券，返回原凭据或输入／冲突／存储错误。
// 模板预算、原请求内容、用户准入与发放由同事务所属能力仲裁，审计失败整体回滚，不换请求号重试。
func (s CouponService) Grant(ctx context.Context, audit ops.AuditContext, id uint64, in GrantInput) (GrantResult, error) {
	out := GrantResult{RequestID: in.RequestID}
	if !audit.Actor.HasPermission("coupon.grant") {
		return out, operations.ErrForbidden
	}
	if id == 0 || in.UserID == 0 || !operations.ValidRequestID(in.RequestID) {
		return out, &InputError{"请求编号或用户编号无效"}
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		grantID, err := s.GrantInTx(tx, id, in.UserID, in.RequestID)
		if errors.Is(err, ErrCouponGrant) {
			return operations.ErrConflict
		}
		if err != nil {
			return err
		}
		out.CouponGrantID = grantID
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "grant", Target: "coupon", ID: id, After: in, RequestID: in.RequestID})
	})
	return out, err
}

// GrantInTx 在 tx 原context下按 couponID／userID／requestID 仲裁人工发放，返回原发放号或准入／冲突／存储错误。
// 首次原凭据优先重放，用户→模板→发放行取得当前锁后复核请求，按原有效期及预算发券。
// 不自行提交；发放、原请求与调用方审计共用完整事务，唯一键冲突不留下额外权益。
func (s CouponService) GrantInTx(tx *gorm.DB, couponID, userID uint64, requestID string) (uint64, error) {
	if tx == nil || couponID == 0 || userID == 0 || !operations.ValidRequestID(requestID) {
		return 0, ErrCouponGrant
	}
	receipt, err := findGrantReceipt(tx, requestID, false)
	if err == nil {
		return ReplayGrant(receipt, requestID, couponID, userID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	status, err := identity.LockStatusInTx(tx, userID)
	if err != nil {
		return 0, err
	}
	coupon, usage, err := manualGrantState(tx, couponID, userID)
	if err != nil {
		return 0, err
	}
	receipt, err = findGrantReceipt(tx, requestID, true)
	if err == nil {
		return ReplayGrant(receipt, requestID, couponID, userID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if status != "active" {
		return 0, ErrCouponGrant
	}
	now := time.Now().UTC()
	template := CouponTemplate{TemplateID: coupon.ID, Status: coupon.Status, TotalQuota: coupon.TotalQuota, PerUserQuota: coupon.PerUserQuota, ValidHours: coupon.ValidHours, StartAt: coupon.StartAt, EndAt: coupon.EndAt}
	if err := CheckGrantable(now, template, usage); err != nil {
		return 0, ErrCouponGrant
	}
	id, err := saveManualGrant(tx, activityGrant{CouponID: couponID, UserID: userID, EventID: requestID, ExpiresAt: GrantExpiry(now, template)})
	if isDuplicateKey(err) {
		return 0, ErrCouponGrant
	}
	return id, err
}
