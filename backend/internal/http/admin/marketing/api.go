package marketing

import (
	"errors"

	"chargepilot/internal/domain/operations"
	adminhttp "chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/marketing"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// API 仅持有营销业务服务与当前后台鉴权，不拥有数据库事务。
type API struct {
	Coupons    marketing.CouponService   // 模板及人工发放用例
	Activities marketing.ActivityService // 活动配置及列表用例
	Auth       authhttp.AdminAuth        // 实时权限及会话验证
}

// Register 将 a 券与活动路径注册到 r，无存储或网络 IO。
func (a API) Register(r *gin.Engine) { a.registerCoupons(r); a.registerActivityRules(r) }

// actor 从 c 后台上下文读取身份，返回已授权 Profile。
func actor(c *gin.Context) operations.Profile { return c.MustGet("admin_profile").(operations.Profile) }

// audit 从 c 鉴权与请求适配取得审计值，返回安全上下文，不发起事务。
func audit(c *gin.Context) ops.AuditContext {
	return ops.AuditContext{Actor: actor(c), ClientIP: c.ClientIP(), RequestID: httpapi.RequestID(c)}
}

// failure 将 err 输入／权限／存储结果映射到 c，业务故障不伪装成鉴权失败。
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
