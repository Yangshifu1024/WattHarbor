// Package marketing 提供优惠券模板、发放和活动规则管理接口。
package marketing

import (
	"errors"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/domain/operations"
	adminhttp "wattharbor/internal/http/admin"
	authhttp "wattharbor/internal/http/auth"
	"wattharbor/internal/marketing"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
)

// API 提供优惠券和活动规则接口，依赖营销服务和后台鉴权。
type API struct {
	Coupons    marketing.CouponService   // 模板及人工发放用例
	Activities marketing.ActivityService // 活动配置及列表用例
	Auth       authhttp.AdminAuth        // 实时权限及会话验证
}

// Register 注册优惠券和活动规则接口。
func (a API) Register(r *gin.Engine) { a.registerCoupons(r); a.registerActivityRules(r) }

// actor 取得请求中已鉴权的管理员资料。
func actor(c *gin.Context) operations.Profile { return c.MustGet("admin_profile").(operations.Profile) }

// audit 取得已鉴权的管理员、客户端 IP 和请求编号。
func audit(c *gin.Context) ops.AuditContext {
	return ops.AuditContext{Actor: actor(c), ClientIP: c.ClientIP(), RequestID: httpapi.RequestID(c)}
}

// failure 将参数、权限和存储错误转换为接口响应。
func failure(c *gin.Context, err error) {
	var input *marketing.InputError
	if errors.As(err, &input) {
		httpapi.BadRequest(c, input.Message)
		return
	}
	if errors.Is(err, operations.ErrForbidden) {
		httpapi.Write(c, 403, 1003, "无权执行此操作", nil)
		return
	}
	adminhttp.AdminResourceFailure(c, err)
}
