package control

import (
	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SessionAPI 提供设备会话审计及遗留会话清理接口。
// 正常连接由所属进程关闭，进程退出后遗留的开放记录由运维清理。
type SessionAPI struct {
	// DB 装配方提供的 gateway GORM 连接句柄；此适配器不关闭连接池，具体事务由原方法管理。
	DB *gorm.DB
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
	// IdleThreshold 是候选会话的最小静默时间，仅按持久 last_active_at 判断。
	IdleThreshold time.Duration
	// MaxRows 限制单次清理的规模，避免积压过大时把一个长事务吊住；
	// 调用方重跑一次就行。
	MaxRows int
}

// Register 接收非 nil router，注册本类型原路由与处理器，无数据库或设备 IO，不启动 HTTP 监听。
func (a SessionAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/device-sessions", a.listIdle)
	router.POST("/api/v1/internal/device-sessions/cleanup-idle", a.cleanupIdle)
}

// authorized 接收 c，精确验证服务请求头；成功返回 true，失败写原 401 并返回 false，调用方据此退出，不读取数据库。
func (a SessionAPI) authorized(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}

// IdleSession 是向运维报告的一条被遗弃的连接。
type IdleSession struct {
	// SessionID 原设备连接会话身份，不是 START 或 STOP 命令会话。
	SessionID string `json:"session_id"`
	// DeviceID 原设备业务编号。
	DeviceID string `json:"device_id"`
	// Protocol 所属设备或连接的原协议名称。
	Protocol string `json:"protocol"`
	// RemoteAddr 原设备连接远端地址。
	RemoteAddr string `json:"remote_addr"`
	// StartedAt 原设备连接开始时刻。
	StartedAt time.Time `json:"started_at"`
	// LastActiveAt 原设备连接最后活动时刻。
	LastActiveAt time.Time `json:"last_active_at"`
}

// threshold 读取 IdleThreshold，正值原样返回，否则返回十五分钟；无 IO。
func (a SessionAPI) threshold() time.Duration {
	if a.IdleThreshold > 0 {
		return a.IdleThreshold
	}
	return 15 * time.Minute
}

// limit 读取 MaxRows，正值原样返回，否则返回五百；无 IO。
func (a SessionAPI) limit() int {
	if a.MaxRows > 0 {
		return a.MaxRows
	}
	return 500
}

// listIdle 只报告被遗弃的会话而不做任何改动，这样运维可以先看清一次清理
// 会关掉哪些，再决定要不要跑。
func (a SessionAPI) listIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var sessions []IdleSession
	if err := gatewayrepo.IdleSessionsQuery(c.Request.Context(), a.DB, a.threshold()).
		Select("session_id", "device_id", "protocol", "remote_addr", "started_at", "last_active_at").
		Order("last_active_at ASC").Limit(a.limit()).Scan(&sessions).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "设备会话查询失败", nil)
		return
	}
	if sessions == nil {
		sessions = []IdleSession{}
	}
	httpapi.OK(c, gin.H{
		"items":              sessions,
		"count":              len(sessions),
		"idle_threshold_sec": int(a.threshold().Seconds()),
	})
}

// cleanupIdle 接收 c，鉴权后在请求事务中按完整主键关闭超时且仍开放的连接记录。
// 成功写原 closed 数量；鉴权或 SQL 失败写原 401、503，不关闭实际 TCP 连接。
func (a SessionAPI) cleanupIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()

	// 在同一事务中读取并关闭候选会话；更新再次检查 ended_at IS NULL，避免覆盖并发正常关闭的记录。
	var closed int64
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []gatewayrepo.SessionKey
		if err := gatewayrepo.IdleSessionsQuery(ctx, tx, a.threshold()).
			Select("id", "created_month").Limit(a.limit()).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, key := range candidates {
			affected, err := gatewayrepo.CloseAbandonedSession(tx, key, now)
			if err != nil {
				return err
			}
			closed += affected
		}
		return nil
	})
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "设备会话清理失败", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"closed":             closed,
		"idle_threshold_sec": int(a.threshold().Seconds()),
	})
}
