package serviceclient

import "testing"

func TestVerifyServiceToken(t *testing.T) {
	const token = "internal-service-token"
	cases := []struct {
		name            string
		expected, given string
		want            bool
	}{
		{name: "匹配", expected: token, given: token, want: true},
		{name: "不匹配", expected: token, given: token + "x", want: false},
		{name: "大小写不同", expected: token, given: "Internal-Service-Token", want: false},
		{name: "缺少令牌", expected: token, given: "", want: false},
		// 未配置期望令牌意味着配置错误，必须拒绝而不是放行。
		{name: "期望令牌未配置", expected: "", given: "", want: false},
		{name: "期望令牌未配置但请求带了值", expected: "", given: token, want: false},
		// 长度不同也要能比较——摘要让两侧定长，subtle 不再拒绝。
		{name: "长度不同", expected: token, given: "a", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := VerifyServiceToken(c.expected, c.given); got != c.want {
				t.Fatalf("VerifyServiceToken(%q, %q) = %v, 期望 %v", c.expected, c.given, got, c.want)
			}
		})
	}
}
