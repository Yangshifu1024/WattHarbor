package identity

import (
	"context"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/phone"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
)

// PhoneAPI 提供 C 端手机号绑定与解绑接口。user 表归 identity 家族，
// 手机号列的写入经 UserStore 落到持久化层，接口层不持有数据库句柄；
// 正式绑定必须使用微信授权凭证，开发模式的明文号码仅供联调。
type PhoneAPI struct {
	Auth             SessionAuthenticator
	Users            UserStore
	DevelopmentPhone bool
	PhoneExchange    func(context.Context, string) (string, error)
}

func (a PhoneAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/user/phone/bind", a.bind)
	r.POST("/api/v1/user/phone/unbind", a.unbind)
}

func (a PhoneAPI) bind(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "手机号请求无效")
		return
	}
	if !a.DevelopmentPhone {
		if in.Code == "" || in.Phone != "" {
			httpapi.BadRequest(c, "请使用微信手机号授权凭证")
			return
		}
		if a.PhoneExchange == nil {
			httpapi.Write(c, 503, 5003, "手机号授权暂不可用", nil)
			return
		}
		exchanged, err := a.PhoneExchange(c.Request.Context(), in.Code)
		if err != nil {
			httpapi.Write(c, 400, 1004, "手机号授权失败，请重新授权", nil)
			return
		}
		in.Phone = exchanged
	}
	in.Phone = phone.Normalize(in.Phone)
	if !phone.Valid(in.Phone) {
		httpapi.BadRequest(c, "请输入有效的中国大陆手机号")
		return
	}
	err := a.Users.BindPhone(c.Request.Context(), userID, &in.Phone)
	var duplicate *mysql.MySQLError
	switch {
	case errors.Is(err, ErrPhoneTaken) || errors.As(err, &duplicate) && duplicate.Number == 1062:
		httpapi.Write(c, 409, 2009, "该手机号已绑定其他账号", nil)
	case err != nil:
		httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
	default:
		httpapi.OK(c, gin.H{"bound": true, "phone_masked": phone.Mask(in.Phone)})
	}
}

func (a PhoneAPI) unbind(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	if err := a.Users.BindPhone(c.Request.Context(), userID, nil); err != nil {
		httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
		return
	}
	httpapi.OK(c, gin.H{"bound": false, "unbound": true})
}
