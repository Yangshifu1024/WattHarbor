package identity

import (
	"context"
	"errors"
	"github.com/go-sql-driver/mysql"
	"wattharbor/internal/platform/phone"
)

var (
	ErrPhoneInput         = errors.New("phone input invalid")               // 号码或正式授权输入不符合约束
	ErrPhoneAuthorization = errors.New("phone authorization input invalid") // 正式路径须凭代码且不得提交明文
	ErrPhoneUnavailable   = errors.New("phone exchange unavailable")        // 未装配正式号码交换能力
	ErrPhoneExchange      = errors.New("phone exchange failed")             // 微信交换失败，不能相信客户端号码
)

// PhoneInput 保留手机号请求字段；正式路径仅接受 Code，开发路径使用 Phone。
type PhoneInput struct {
	Phone string `json:"phone"` // 开发输入的号码；正式绑定不得填写
	Code  string `json:"code"`  // 微信手机号授权凭证
}

// PhoneSource 是当前进程选择的号码来源，正式与开发实现由 cmd 分别装配。
type PhoneSource interface {
	// Resolve 处理 in，返回已验证或开发号码及授权、交换错误，不写用户。
	Resolve(ctx context.Context, in PhoneInput) (string, error)
}

// WeChatPhoneSource 只接受微信授权 code，通过微信接口取得手机号，不使用客户端明文。
type WeChatPhoneSource struct {
	Exchange func(context.Context, string) (string, error) /* cmd 注入的正式微信交换能力 */
}

// Resolve 核对 in 正式授权字段并交换，返回号码或授权、配置、渠道错误。
func (s WeChatPhoneSource) Resolve(ctx context.Context, in PhoneInput) (string, error) {
	if in.Code == "" || in.Phone != "" {
		return "", ErrPhoneAuthorization
	}
	if s.Exchange == nil {
		return "", ErrPhoneUnavailable
	}
	value, err := s.Exchange(ctx, in.Code)
	if err != nil {
		return "", errors.Join(ErrPhoneExchange, err)
	}
	return value, nil
}

// DevelopmentPhoneSource 只由开发模式 cmd 注入；明文输入不经过正式授权实现。
type DevelopmentPhoneSource struct{}

// Resolve 返回 ctx 下 in.Phone 开发输入或取消错误，不执行渠道或数据库调用。
func (DevelopmentPhoneSource) Resolve(ctx context.Context, in PhoneInput) (string, error) {
	return in.Phone, ctx.Err()
}

// PhoneRepository 提供所属号码事务，唯一键最终仲裁归属；用例不映射 user 表。
type PhoneRepository interface {
	// BindPhone 设置用户手机号，value 为 nil 时解绑；号码已被他人使用时返回归属冲突。
	BindPhone(ctx context.Context, userID uint64, value *string) error
}

// PhoneResult 保留绑定成功的掩码响应。
type PhoneResult struct {
	Bound       bool   `json:"bound"`        // 本请求已提交绑定
	PhoneMasked string `json:"phone_masked"` // 号码按通用掩码展示
}

// PhoneService 负责来源校验、号码校验及所属绑定事务；不接受来自 HTTP 的开发模式布尔值。
type PhoneService struct {
	Users  PhoneRepository // 所属用户数据能力
	Source PhoneSource     // cmd 选择的正式或开发来源
}

// Bind 校验号码来源并规范手机号，成功后返回掩码。
// 并发归属冲突返回 ErrPhoneTaken；失败不声称绑定成功，也不记录原号码或凭据。
func (s PhoneService) Bind(ctx context.Context, userID uint64, in PhoneInput) (PhoneResult, error) {
	if s.Source == nil {
		return PhoneResult{}, ErrPhoneUnavailable
	}
	value, err := s.Source.Resolve(ctx, in)
	if err != nil {
		return PhoneResult{}, err
	}
	value = phone.Normalize(value)
	if !phone.Valid(value) {
		return PhoneResult{}, ErrPhoneInput
	}
	if err := s.Users.BindPhone(ctx, userID, &value); err != nil {
		var duplicate *mysql.MySQLError
		if errors.Is(err, ErrPhoneTaken) || errors.As(err, &duplicate) && duplicate.Number == 1062 {
			return PhoneResult{}, ErrPhoneTaken
		}
		return PhoneResult{}, err
	}
	return PhoneResult{true, phone.Mask(value)}, nil
}

// Unbind 将 userID 的号码原子设为 NULL，返回数据库错误；不调用外部授权源。
func (s PhoneService) Unbind(ctx context.Context, userID uint64) error {
	return s.Users.BindPhone(ctx, userID, nil)
}
