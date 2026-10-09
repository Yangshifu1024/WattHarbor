package internaljob

import (
	"errors"
	"net/http"
	"time"

	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// OpsAPI 查询配置流的 Redis 长度；服务令牌通过后才允许读取，不提供重放或死信操作。
type OpsAPI struct {
	// Stream 执行带请求 context 的 XLen，不写入流或消费组。
	Stream redis.UniversalClient
	// Streams 为按原顺序读取的流名；空列表返回空 pending 对象。
	Streams []string
	// ServiceToken 为精确校验的期望令牌，空配置拒绝所有请求。
	ServiceToken string
}

// Register 在 router 注册原 GET 路由和鉴权中间件，无 Redis IO；router 必须非 nil。
func (a OpsAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/worker/ops/streams", a.authorized(), a.streams)
}

// authorized 返回服务鉴权处理器：输入请求头，失败写唯一 401 并终止处理链，成功继续后续处理器。
func (a OpsAPI) authorized() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

// streams 使用 c 的请求 context 依次读取 XLen，输出 pending 长度与 UTC checked_at。
// 长度不区分已消费与未消费；redis.Nil 跳过该流，其余错误（包括取消）仅写原 503，停止后续读取。
func (a OpsAPI) streams(c *gin.Context) {
	pending := map[string]int64{}
	for _, name := range a.Streams {
		length, err := a.Stream.XLen(c.Request.Context(), name).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "stream 状态暂时不可读", nil)
			return
		}
		pending[name] = length
	}
	httpapi.OK(c, gin.H{"pending": pending, "checked_at": time.Now().UTC()})
}
