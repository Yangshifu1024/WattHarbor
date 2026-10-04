package marketing

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"time"
)

// 本文件提供优惠券活动规则的运营接口；发放逻辑由 charge 包实现。
// 金额单位为分，活动时间使用绝对起止时间，是否生效由状态及时间窗口共同判断。

// registerActivityRules 挂载券活动的后台路由：列表（只读）、新建、修改，均为管理后台侧，
// 活动引擎本身在 charge 服务里，这里只负责运营的查看与开关。
func (a API) registerActivityRules(r *gin.Engine) {
	r.GET("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.read"), a.listActivityRules)
	r.POST("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.manage"), a.createActivityRule)
	r.PUT("/api/v1/admin/coupon-activities/:id", a.Auth.Require("coupon.activity.manage"), a.updateActivityRule)
}

// listActivityRules 分页返回券活动列表，支持按状态（active/disabled）和活动名关键词过滤，
// 并把每条活动券的实际发放份数补进 GrantedCount，让运营直接看到活动成本。
func (a API) listActivityRules(c *gin.Context) {
	page, ok := chargehttp.ReadAdminPage(c, "active disabled")
	if !ok {
		return
	}
	rows, total, err := (marketing.AdminActivityStore{DB: a.DB}).
		ListActivityRules(c.Request.Context(), page.Status, page.Keyword, page.Page, page.PageSize)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, chargehttp.AdminPage[marketing.AdminActivityRuleRow]{
		Items: rows, Total: total, Page: page.Page, PageSize: page.PageSize})
}

// activityRuleInput 是创建和更新活动的请求体。
// StartAt 与 EndAt 由 validate 按 RFC3339 统一解析并返回参数错误。
type activityRuleInput struct {
	Name            string  `json:"name"`              // 活动名称，必填，最多 128 字
	TriggerType     string  `json:"trigger_type"`      // 触发类型，取值见 activityTriggers
	CouponID        uint64  `json:"coupon_id"`         // 活动发放的券 ID，必填且必须存在且为 active
	InviterCouponID *uint64 `json:"inviter_coupon_id"` // 邀请人奖励券 ID；邀请有奖类型必填且不得为 0，其余类型可为 nil
	ThresholdCents  int64   `json:"threshold_cents"`   // 触发门槛（分）；满减类型须 > 0，其余类型须为 0
	MaxGrants       int     `json:"max_grants"`        // 活动总发放上限，0 表示不限
	PerUserLimit    int     `json:"per_user_limit"`    // 单用户可领份数，0 表示不限
	Status          string  `json:"status"`            // active 或 disabled；留空时默认置为 active
	StartAt         string  `json:"start_at"`          // 活动开始时间，RFC3339 字符串
	EndAt           string  `json:"end_at"`            // 活动结束时间，RFC3339 字符串，须晚于开始时间且不超过一年后
}

// errActivityInput 是券活动各类校验失败的统一哨兵错误，只在事务内传播，由外层翻译成给运营看的中文提示。
var errActivityInput = marketing.ErrActivityRule

// createActivityRule 新建一个券活动。校验通过且所引用的券都可用后写入 coupon_activity_rule。
func (a API) createActivityRule(c *gin.Context) {
	var in activityRuleInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	start, end, status, err := marketing.NormalizeActivity(in.asActivity(), time.Now().UTC())
	if err == nil {
		in.Status = status
	}
	if err != nil {
		httpapi.BadRequest(c, "活动规则无效：名称不能为空，门槛为非负分，窗口须为 RFC3339 且结束晚于开始，满减须设门槛，邀请有奖须设邀请人券")
		return
	}
	// 创建前校验奖励券存在且可用，避免保存无法发券的规则。
	if err := a.couponUsable(c, in.CouponID); err != nil {
		httpapi.BadRequest(c, "活动券不存在或已停用")
		return
	}
	if in.InviterCouponID != nil && *in.InviterCouponID != 0 {
		if err := a.couponUsable(c, *in.InviterCouponID); err != nil {
			httpapi.BadRequest(c, "邀请人券不存在或已停用")
			return
		}
	}
	input := marketing.AdminActivityRuleInput{Name: in.Name, TriggerType: in.TriggerType,
		CouponID: in.CouponID, InviterCouponID: in.InviterCouponID, ThresholdCents: in.ThresholdCents,
		MaxGrants: in.MaxGrants, PerUserLimit: in.PerUserLimit, Status: in.Status,
		StartAt: start, EndAt: end}
	store := marketing.AdminActivityStore{DB: a.DB}
	var id uint64
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		id, err = store.CreateActivityRule(c.Request.Context(), input)
		if err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, c.MustGet("admin_profile").(operations.Profile), "create", "coupon_activity", id, nil, in, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// updateActivityRule 更新活动配置但保持 trigger_type 不变，确保历史发放的触发语义一致。
// 更新前读取旧值用于审计；规则不存在时返回 404。
func (a API) updateActivityRule(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in activityRuleInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	start, end, status, err := marketing.NormalizeActivity(in.asActivity(), time.Now().UTC())
	if err == nil {
		in.Status = status
	}
	if err != nil {
		httpapi.BadRequest(c, "活动规则无效")
		return
	}
	if err := a.couponUsable(c, in.CouponID); err != nil {
		httpapi.BadRequest(c, "活动券不存在或已停用")
		return
	}
	input := marketing.AdminActivityRuleInput{Name: in.Name, TriggerType: in.TriggerType,
		CouponID: in.CouponID, InviterCouponID: in.InviterCouponID, ThresholdCents: in.ThresholdCents,
		MaxGrants: in.MaxGrants, PerUserLimit: in.PerUserLimit, Status: in.Status,
		StartAt: start, EndAt: end}
	store := marketing.AdminActivityStore{DB: a.DB}
	var before marketing.AdminActivityRuleRow
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		before, err = store.UpdateActivityRule(c.Request.Context(), id, input)
		if err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, c.MustGet("admin_profile").(operations.Profile), "update", "coupon_activity", id, before, in, c.ClientIP(), httpapi.RequestID(c))
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusNotFound, 1004, "活动规则不存在", nil)
		return
	}
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// couponUsable 校验券存在、状态为 active 且未软删除；不满足时返回 errActivityInput。
func (a API) couponUsable(c *gin.Context, couponID uint64) error {
	usable, err := marketing.CouponUsable(c.Request.Context(), a.DB, couponID)
	if err != nil {
		return err
	}
	if !usable {
		return errActivityInput
	}
	return nil
}

// asActivity 把请求体转成领域层的活动参数。
// 两层的字段名一致，但分层是硬要求：校验规则不在接口层再实现一份。
func (in *activityRuleInput) asActivity() marketing.ActivityRule {
	return marketing.ActivityRule{
		Name: in.Name, TriggerType: in.TriggerType, CouponID: in.CouponID,
		InviterCouponID: in.InviterCouponID, ThresholdCents: in.ThresholdCents,
		MaxGrants: in.MaxGrants, PerUserLimit: in.PerUserLimit,
		Status: in.Status, StartAt: in.StartAt, EndAt: in.EndAt,
	}
}
