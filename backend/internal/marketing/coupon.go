// Package marketing 管理优惠券模板、发放、报价、核销和活动规则。
package marketing

import (
	marketingdomain "chargepilot/internal/domain/marketing"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/identity"
	"chargepilot/internal/ops"
	"context"
	"errors"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"time"
)

// InputError 表示营销请求内容不合法；数据库故障不能包装成输入错误。
type InputError struct {
	Message string // 公开校验提示
}

// Error 返回公开输入提示。
func (e *InputError) Error() string { return e.Message }

// Coupon 是券模板参数和查询结果，金额、折扣、免费时长三种优惠互斥。
// DiscountType 与 Status 对应 coupon 表的 discount_type 与 status 列；
// 与活动的 TriggerType、ActivityStatus 不同表，不得互换。
type Coupon struct {
	ID                 uint64           `json:"id"`                   // 券主键，创建时必须为0
	Name               string           `json:"name"`                 // 非空名称，最多128字
	DiscountType       DiscountType     `json:"discount_type"`        // amount、percentage 或 time_free
	DiscountValueCents *int64           `json:"discount_value_cents"` // 满减分值，其他优惠类型为空
	DiscountPercent    *decimal.Decimal `json:"discount_percent"`     // 应付比例百分数，80 表示八折；最多两位小数，严格在 0 与 100 之间
	FreeMinutes        *uint32          `json:"free_minutes"`         // 免费分钟，1–1440，其他优惠类型为空
	MinChargeCents     int64            `json:"min_charge_cents"`     // 使用门槛，非负分
	ValidHours         uint32           `json:"valid_hours"`          // 领取后有效小时，1–87600
	TotalQuota         uint32           `json:"total_quota"`          // 模板总量，0 表示不限量
	PerUserQuota       uint32           `json:"per_user_quota"`       // 人工发放单人上限，必须大于0
	Status             CouponStatus     `json:"status"`               // active 或 disabled；创建强制 active
	StartAt            *time.Time       `json:"start_at"`             // 可选开始时刻
	EndAt              *time.Time       `json:"end_at"`               // 可选结束时刻；同时设置开始时间时，须晚于开始
}

// CouponUpdate 只允许修改名称和状态，不改变优惠和发放上限。
type CouponUpdate struct {
	Name   string       `json:"name"`   // 模板名称，非空最多128字
	Status CouponStatus `json:"status"` // active 或 disabled
}

// GrantInput 保存人工发放的用户和请求号。
type GrantInput struct {
	RequestID string `json:"request_id"`     // UUID 请求号，重试沿用同一号
	UserID    uint64 `json:"user_id,string"` // 目标用户主键，审计保持十进制文本
}

// GrantResult 返回发放主键和首次请求号，重放不生成新编号。
type GrantResult struct {
	CouponGrantID uint64 `json:"coupon_grant_id"` // 发放主键
	RequestID     string `json:"request_id"`      // 客户端幂等号
}

// CouponService 负责模板校验、人工发放和同事务审计，使用全局券管理权限。
type CouponService struct {
	DB *gorm.DB // 所属中央连接或调用方事务，由进程管理
}

// RedeemRef 保存支付时确定的原价和优惠；核销时按原价校验优惠是否一致。
type RedeemRef struct {
	UserID        uint64 // 付款用户
	CouponGrantID uint64 // 发放记录主键
	TotalCents    int64  // 优惠前费用，分
	DiscountCents int64  // 冻结优惠，分
	PaymentOrder  uint64 // 支付主键
}

// Quote 计算本人未用、未到期券对原价的优惠，单位分，不核销券。
func (s CouponService) Quote(ctx context.Context, userID, grantID uint64, totalCents int64) (int64, error) {
	grant, err := loadGrant(ctx, s.DB, userID, grantID, false)
	if err != nil {
		return 0, err
	}
	if grant.Status != marketingdomain.GrantUnused || !grant.ExpiresAt.After(time.Now().UTC()) {
		return 0, ErrCouponExhausted
	}
	return discountFor(grant, totalCents)
}

// RedeemInTx 在资金事务内锁定并核销券，同一券和支付记录重放沿用首次结果。
// 优惠须等于支付时保存的优惠，不能按实付额再算；核销随资金一起提交或回滚。
func (s CouponService) RedeemInTx(tx *gorm.DB, ref RedeemRef, reference string) error {
	if tx == nil || ref.UserID == 0 || ref.CouponGrantID == 0 || ref.PaymentOrder == 0 || reference == "" || ref.DiscountCents < 0 || ref.DiscountCents > ref.TotalCents {
		return ErrCouponGrant
	}
	grant, err := loadGrant(tx.Statement.Context, tx, ref.UserID, ref.CouponGrantID, true)
	if err != nil {
		return err
	}
	if grant.Status == marketingdomain.GrantUsed {
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
	if grant.Status != marketingdomain.GrantUnused || !grant.ExpiresAt.After(time.Now().UTC()) {
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

// validDiscount 检查金额、折扣和免费时长三选一，且各项满足数值限制。
// 取值判定按 coupon.discount_type 的三值词表（status.go 的 DiscountType）。
func validDiscount(in Coupon) bool {
	switch in.DiscountType {
	case DiscountAmount:
		return in.DiscountValueCents != nil && *in.DiscountValueCents > 0 && in.DiscountPercent == nil && in.FreeMinutes == nil
	case DiscountPercentage:
		return in.DiscountPercent != nil && in.DiscountPercent.GreaterThan(decimal.Zero) && in.DiscountPercent.LessThan(decimal.NewFromInt(100)) && in.DiscountPercent.Equal(in.DiscountPercent.Round(2)) && in.DiscountValueCents == nil && in.FreeMinutes == nil
	case DiscountTimeFree:
		return in.FreeMinutes != nil && *in.FreeMinutes > 0 && *in.FreeMinutes <= 1440 && in.DiscountPercent == nil && in.DiscountValueCents == nil
	}
	return false
}

// List 核对 coupon.read 权限后倒序读取未删除模板。
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

// Create 核对 coupon.create 权限和内容后创建启用模板，并同事务保存审计。
// 客户端主键须为 0；创建时将状态设为 active，忽略提交的状态。
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
	in.Status = CouponActive
	row := AdminCouponRow(in)
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := (AdminCouponRepo{DB: tx}).CreateCoupon(ctx, &row); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "create", Target: "coupon", ID: row.ID, After: Coupon(row)})
	})
	return Coupon(row), err
}

// Update 核对 coupon.update 权限后锁定模板，只更新允许字段，并同事务保存审计。
func (s CouponService) Update(ctx context.Context, audit ops.AuditContext, id uint64, in CouponUpdate) (CouponUpdate, error) {
	if !audit.Actor.HasPermission("coupon.update") {
		return in, operations.ErrForbidden
	}
	if id == 0 || !operations.ValidText(in.Name, 128) || !in.Status.Known() {
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

// Stats 核对 coupon.read 权限后读取发放统计；未用但已到期计为 expired。
func (s CouponService) Stats(ctx context.Context, actor operations.Profile, id uint64) (AdminCouponStats, error) {
	if !actor.HasPermission("coupon.read") {
		return AdminCouponStats{}, operations.ErrForbidden
	}
	return (AdminCouponRepo{DB: s.DB}).Stats(ctx, id)
}

// Grant 核对 coupon.grant 权限后向指定用户发券。
// 用户状态、发放上限和请求记录在同一事务检查，发放与审计一起提交；重试沿用请求号。
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

// GrantInTx 先检查首次请求重放，再按用户→模板→发放行加锁，复查请求和额度后发券。
// 同请求且用户、模板一致时返回首次发放 ID；新券和请求记录保存到调用方事务，审计由调用方追加。
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
