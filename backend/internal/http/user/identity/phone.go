package identity

import (
	"errors"

	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/identity"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// PhoneAPI 只提取用户及请求，正式／开发来源与号码事务由 identity 拥有。
type PhoneAPI struct {
	Auth    authhttp.SessionAuthenticator // 原请求身份能力
	Service identity.PhoneService         // cmd 装配来源及所属号码数据能力
}

// Register 在 r 注册原用户绑定／解绑端点，不打开资源。
func (a PhoneAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/user/phone/bind", a.bind)
	r.POST("/api/v1/user/phone/unbind", a.unbind)
}

// bind 从 c 读取当前身份与原号码字段，调用用例并保持原错误状态及成功掩码。
func (a PhoneAPI) bind(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in identity.PhoneInput
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "手机号请求无效")
		return
	}
	out, err := a.Service.Bind(c.Request.Context(), userID, in)
	switch {
	case errors.Is(err, identity.ErrPhoneAuthorization):
		httpapi.BadRequest(c, "请使用微信手机号授权凭证")
	case errors.Is(err, identity.ErrPhoneUnavailable):
		httpapi.Write(c, 503, 5003, "手机号授权暂不可用", nil)
	case errors.Is(err, identity.ErrPhoneExchange):
		httpapi.Write(c, 400, 1004, "手机号授权失败，请重新授权", nil)
	case errors.Is(err, identity.ErrPhoneInput):
		httpapi.BadRequest(c, "请输入有效的中国大陆手机号")
	case errors.Is(err, identity.ErrPhoneTaken):
		httpapi.Write(c, 409, 2009, "该手机号已绑定其他账号", nil)
	case err != nil:
		httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
	default:
		httpapi.OK(c, out)
	}
}

// unbind 从 c 提取用户并调用所属解绑事务，返回原标志或存储错误，不交换号码。
func (a PhoneAPI) unbind(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	if a.Service.Unbind(c.Request.Context(), userID) != nil {
		httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
		return
	}
	httpapi.OK(c, gin.H{"bound": false, "unbound": true})
}
