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
	// DB 装配方提供的 gateway GORM 连接句柄；此适配器不关闭连接池，完整读写和事务由 repo 管理。
	DB *gorm.DB
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
	// IdleThreshold 是候选会话的最小静默时间，仅按持久 last_active_at 判断。
	IdleThreshold time.Duration
	// MaxRows 限制单次列表和清理的候选条数，非正值采用五百条默认值。
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

// IdleSession 是向运维报告的静默设备连接会话记录，不代表实际 TCP 已断开。
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

// listIdle 接收 c 中的服务令牌，返回阈值筛选的设备连接会话、条数和阈值秒数，空列表保持 []。
// 鉴权失败返回 401，读取失败返回原 503；仅转换持久投影，不修改会话记录或实际 TCP 连接。
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

// cleanupIdle 接收 c 中的服务令牌，捕获当前 UTC 作为关闭时刻，交由 repo 完整事务关闭静默且仍开放的连接记录。
// 成功返回 closed 和阈值秒数；鉴权失败返回 401，事务失败返回原 503 且不输出中间计数，不关闭实际 TCP。
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
