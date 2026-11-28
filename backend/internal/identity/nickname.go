package identity

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrNicknameInvalid 表示昵称为空、超过数据库 varchar(64) 容量或含控制字符。
var ErrNicknameInvalid = errors.New("昵称须为 1 至 64 个字符且不能包含控制字符或换行")

// NicknameRequest 仅接受用户主动填写的昵称；用户身份由访问令牌确定。
type NicknameRequest struct {
	Nickname string `json:"nickname"` // 用户选择或输入的显示名称。
}

// NicknameReply 返回已持久化的昵称，供客户端立即更新本人资料。
type NicknameReply struct {
	Nickname string `json:"nickname"` // 去除首尾空格后的显示名称。
}

// UpdateNickname 为刚通过会话鉴权的 userID 设置昵称。同值可重复提交，写入时再次核验启用状态。
func (s SessionService) UpdateNickname(ctx context.Context, userID uint64, in NicknameRequest) (NicknameReply, error) {
	nickname := strings.TrimSpace(in.Nickname)
	if nickname == "" || !utf8.ValidString(in.Nickname) || utf8.RuneCountInString(nickname) > 64 || strings.ContainsFunc(in.Nickname, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
		return NicknameReply{}, ErrNicknameInvalid
	}
	if err := s.Users.SetNickname(ctx, userID, nickname); err != nil {
		return NicknameReply{}, err
	}
	return NicknameReply{Nickname: nickname}, nil
}
