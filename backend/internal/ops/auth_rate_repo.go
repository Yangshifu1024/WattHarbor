package ops

import (
	"context"
	"github.com/redis/go-redis/v9"
)

// authRateScript 原子增加计数，仅首次设置六十秒窗口，不因后续请求延寿。
var authRateScript = redis.NewScript(`local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`)

// AuthRateRepo 保存后台认证来源的计数，不定义允许次数的业务规则。
type AuthRateRepo struct {
	Redis *redis.Client /* 进程拥有的认证缓存 */
}

// CountLogin 统计 IP 登录次数，保持首次一分钟窗口。
func (r AuthRateRepo) CountLogin(ctx context.Context, ip string) (int, error) {
	return authRateScript.Run(ctx, r.Redis, []string{"admin:login-rate:" + ip}).Int()
}
