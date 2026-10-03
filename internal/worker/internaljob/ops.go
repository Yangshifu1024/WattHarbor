package internaljob

import (
	"errors"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// OpsAPI 提供流积压查询接口，要求服务令牌。
//
// 死信查询与重放曾在这里（连同 internal/worker/outbox/dlq.go 整个子系统），
// 已删除：那套代码实现了但从未启用——ConsumeBatch 全仓无调用方，
// 死信队列从不写入，重放处理器恒定返回「没有注册处理器」。留着它只会
// 让运维看到一个永远为空的死信查询接口，误以为有死信要处理。
type OpsAPI struct {
	// Stream 是 Redis Stream 客户端。积压量直接读 XLen，不需要死信表。
	Stream       redis.UniversalClient
	Streams      []string
	ServiceToken string
}

func (a OpsAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/worker/ops/streams", a.authorized(), a.streams)
}

func (a OpsAPI) authorized() gin.HandlerFunc {
	return func(c *gin.Context) {
		if a.ServiceToken == "" || a.ServiceToken != c.GetHeader("X-Service-Token") {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			return
		}
		c.Next()
	}
}

// streams 报告各业务流的积压条数。XLen 返回的是流长度，不区分已消费与未消费，
// 排障时用它判断"事件有没有在进来"足够了；要精确未消费数得 XReadGroup，
// 而消费组此前从未建立。
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
