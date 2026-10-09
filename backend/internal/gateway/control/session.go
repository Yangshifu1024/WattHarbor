package control

import (
	gatewayrepo "chargepilot/internal/gateway/repo"
	"net/http"
	"time"

	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SessionAPI 提供设备会话审计及遗留会话清理接口。
// 正常连接由进程关闭，进程退出后遗留的开放记录由运维清理。
type SessionAPI struct {
	// DB 装配方提供的 gateway GORM 连接句柄；此适配器不关闭连接池，完整读写和事务由 repo 管理。
	DB *gorm.DB
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
	// IdleThreshold 是候选会话的最小静默时间，仅按持久 last_active_at 判断。
	IdleThreshold time.Duration
	// MaxRows 限制单次列表和清理的候选条数，非正值采用五百条默认值。
	MaxRows int
}

// Register 在 router 注册路由；调用方须提供非 nil router。
func (a SessionAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/device-sessions", a.listIdle)
	router.POST("/api/v1/internal/device-sessions/cleanup-idle", a.cleanupIdle)
}

// authorized 精确验证服务请求头；成功返回 true，失败写 401 并返回 false，调用方据此退出。
func (a SessionAPI) authorized(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}

// IdleSession 是向运维报告的静默设备连接会话记录，不代表实际 TCP 已断开。
type IdleSession struct {
	// SessionID 设备连接会话标识，不是 START 或 STOP 命令会话。
	SessionID string `json:"session_id"`
	// DeviceID 设备业务编号。
	DeviceID string `json:"device_id"`
	// Protocol 设备或连接的协议名称。
	Protocol string `json:"protocol"`
	// RemoteAddr 设备连接远端地址。
	RemoteAddr string `json:"remote_addr"`
	// StartedAt 设备连接开始时刻。
	StartedAt time.Time `json:"started_at"`
	// LastActiveAt 设备连接最后活动时刻。
	LastActiveAt time.Time `json:"last_active_at"`
}

// threshold 读取 IdleThreshold，正值原样返回，否则返回十五分钟。
func (a SessionAPI) threshold() time.Duration {
	if a.IdleThreshold > 0 {
		return a.IdleThreshold
	}
	return 15 * time.Minute
}

// limit 读取 MaxRows，正值原样返回，否则返回五百。
func (a SessionAPI) limit() int {
	if a.MaxRows > 0 {
		return a.MaxRows
	}
	return 500
}

// listIdle 接收 c 中的服务令牌，返回阈值筛选的设备连接会话、条数和阈值秒数，空列表保持 []。
// 鉴权失败返回 401，读取失败返回 503；仅转换数据库记录，不修改会话记录或实际 TCP 连接。
func (a SessionAPI) listIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	rows, err := gatewayrepo.ListIdleSessions(
		c.Request.Context(),
		a.DB,
		a.threshold(),
		a.limit(),
	)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "设备会话查询失败", nil)
		return
	}
	sessions := make([]IdleSession, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, IdleSession{
			SessionID:    row.SessionID,
			DeviceID:     row.DeviceID,
			Protocol:     row.Protocol,
			RemoteAddr:   row.RemoteAddr,
			StartedAt:    row.StartedAt,
			LastActiveAt: row.LastActiveAt,
		})
	}
	httpapi.OK(c, gin.H{
		"items":              sessions,
		"count":              len(sessions),
		"idle_threshold_sec": int(a.threshold().Seconds()),
	})
}

// cleanupIdle 鉴权后在同一事务内关闭静默会话记录，返回关闭数和阈值秒数。
// 事务失败不输出中间计数；此操作不关闭实际 TCP 连接。
func (a SessionAPI) cleanupIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()

	closed, err := gatewayrepo.CloseIdleSessions(
		ctx,
		a.DB,
		a.threshold(),
		a.limit(),
		now,
	)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "设备会话清理失败", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"closed":             closed,
		"idle_threshold_sec": int(a.threshold().Seconds()),
	})
}
