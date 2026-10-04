package marketing

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// registerCoupons 挂载优惠券模板的查询、新建、编辑、统计与人工发放五类路由，
// 人工发放单独要求 coupon.grant 权限，因为它能直接影响用户资产。
//
// 模板与发放的读写都在 marketing.AdminCouponStore / GrantCouponInTx；
// 本文件只做入参校验与审计编排。
func (a API) registerCoupons(r *gin.Engine) {
	r.GET("/api/v1/admin/coupons", a.Auth.Require("coupon.read"), a.coupons)
	r.POST("/api/v1/admin/coupons", a.Auth.Require("coupon.create"), a.createCoupon)
	r.PUT("/api/v1/admin/coupons/:id", a.Auth.Require("coupon.update"), a.updateCoupon)
	r.GET("/api/v1/admin/coupons/:id/stats", a.Auth.Require("coupon.read"), a.couponStats)
	r.POST("/api/v1/admin/coupons/:id/grants", a.Auth.Require("coupon.grant"), a.grantCoupon)
}

// coupons 返回全部未删除的券模板，按 ID 倒序，一次性返回不分页。
func (a API) coupons(c *gin.Context) {
	rows, err := (marketing.AdminCouponStore{DB: a.Store.UserDB}).ListCoupons(c.Request.Context())
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createCoupon 新建券模板。状态强制置为 active，不接受调用方指定。
func (a API) createCoupon(c *gin.Context) {
	var in marketing.AdminCouponRow
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if in.ID != 0 || !operations.ValidText(in.Name, 128) || in.MinChargeCents < 0 ||
		in.ValidHours == 0 || in.ValidHours > 87600 || in.PerUserQuota == 0 ||
		(in.EndAt != nil && in.StartAt != nil && !in.EndAt.After(*in.StartAt)) {
		httpapi.BadRequest(c, "优惠券名称、有效期或额度无效")
		return
	}
	if !validDiscount(in) {
		httpapi.BadRequest(c, "优惠参数无效：满减分值须大于 0，折扣为 0–100 之间，免费时长为 1–1440 分钟")
		return
	}
	in.Status = "active"
	var auditPending []chargehttp.AuditEntry
	err := chargehttp.AuditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		if err := (marketing.AdminCouponStore{DB: tx}).CreateCoupon(c.Request.Context(), &in); err != nil {
			return err
		}
		auditPending = []chargehttp.AuditEntry{{Action: "create", Target: "coupon", ID: in.ID, Before: nil, After: in, RequestID: ""}}
		return nil
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, in)
}

// validDiscount 校验优惠三选一的互斥约束：满减用分值、折扣用百分比、免时长用分钟数，
// 另外两种字段必须为空，避免一份券同时带多种优惠口径。
func validDiscount(in marketing.AdminCouponRow) bool {
	switch in.DiscountType {
	case "amount":
		return in.DiscountValueCents != nil && *in.DiscountValueCents > 0 &&
			in.DiscountPercent == nil && in.FreeMinutes == nil
	case "percentage":
		return in.DiscountPercent != nil && in.DiscountPercent.GreaterThan(decimal.Zero) &&
			in.DiscountPercent.LessThan(decimal.NewFromInt(100)) &&
			in.DiscountPercent.Equal(in.DiscountPercent.Round(2)) &&
			in.DiscountValueCents == nil && in.FreeMinutes == nil
	case "time_free":
		return in.FreeMinutes != nil && *in.FreeMinutes > 0 && *in.FreeMinutes <= 1440 &&
			in.DiscountPercent == nil && in.DiscountValueCents == nil
	}
	return false
}

// updateCoupon 只能改名称和启停状态；额度与优惠力度一旦发出去就不能再改，
// 否则已发出的券无法解释。
func (a API) updateCoupon(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !operations.ValidText(in.Name, 128) || (in.Status != "active" && in.Status != "disabled") {
		httpapi.BadRequest(c, "名称或状态无效")
		return
	}
	var before marketing.AdminCouponRow
	var auditPending []chargehttp.AuditEntry
	err := chargehttp.AuditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var err error
		before, err = (marketing.AdminCouponStore{DB: tx}).UpdateCoupon(c.Request.Context(), id, in.Name, in.Status)
		if err != nil {
			return err
		}
		auditPending = []chargehttp.AuditEntry{{Action: "update", Target: "coupon", ID: id, Before: before, After: in, RequestID: ""}}
		return nil
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, in)
}

// couponStats 统计该券模板已发放、未使用、已使用、已过期四种发放记录的数量及使用率。
func (a API) couponStats(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	stats, err := (marketing.AdminCouponStore{DB: a.Store.UserDB}).Stats(c.Request.Context(), id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, stats)
}

// grantCoupon 人工发券。资格判定、到期计算与幂等回执都在 GrantCouponInTx，
// 这里只校验入参并把发放与审计放进同一事务。
func (a API) grantCoupon(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID string            `json:"request_id"`
		UserID    httpapi.DecimalID `json:"user_id"`
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	userID := uint64(in.UserID)
	if !admin.RequestIDPattern.MatchString(in.RequestID) || userID == 0 {
		httpapi.BadRequest(c, "请求编号或用户编号无效")
		return
	}
	var grantID uint64
	var auditPending []chargehttp.AuditEntry
	err := chargehttp.AuditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var err error
		grantID, err = marketing.GrantCouponInTx(tx, id, userID, in.RequestID)
		if errors.Is(err, marketing.ErrCouponGrant) {
			// 业务拒绝，不是故障：映射到统一的 409，否则会被当成 503 让运营反复重试。
			return admin.ErrConflict
		}
		if err != nil {
			return err
		}
		auditPending = []chargehttp.AuditEntry{{Action: "grant", Target: "coupon", ID: id, Before: nil, After: in, RequestID: in.RequestID}}
		return nil
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"coupon_grant_id": grantID, "request_id": in.RequestID})
}
