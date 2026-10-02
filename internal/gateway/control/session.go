package control

import (
	gatewaystore "github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
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
	DB           *gorm.DB
	ServiceToken string
	// IdleThreshold 是候选会话的最小静默时间；所属进程仍活跃时不回收连接。
	IdleThreshold time.Duration
	// MaxRows 限制单次清理的规模，避免积压过大时把一个长事务吊住；
	// 调用方重跑一次就行。
	MaxRows int
}

func (a SessionAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/device-sessions", a.listIdle)
	router.POST("/api/v1/internal/device-sessions/cleanup-idle", a.cleanupIdle)
}

func (a SessionAPI) authorized(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}

// IdleSession 是向运维报告的一条被遗弃的连接。
type IdleSession struct {
	SessionID    string    `json:"session_id"`
	DeviceID     string    `json:"device_id"`
	Protocol     string    `json:"protocol"`
	RemoteAddr   string    `json:"remote_addr"`
	StartedAt    time.Time `json:"started_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

func (a SessionAPI) threshold() time.Duration {
	if a.IdleThreshold > 0 {
		return a.IdleThreshold
	}
	return 15 * time.Minute
}

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
	if err := gatewaystore.IdleSessionsQuery(c.Request.Context(), a.DB, a.threshold()).
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

// CleanupIdle 关闭遗留会话并补齐断连终态字段。
func (a SessionAPI) cleanupIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()

	// 在同一事务中读取并关闭候选会话；更新再次检查 ended_at IS NULL，避免覆盖并发正常关闭的记录。
	var closed int64
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []gatewaystore.SessionKey
		if err := gatewaystore.IdleSessionsQuery(ctx, tx, a.threshold()).
			Select("id", "created_month").Limit(a.limit()).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, key := range candidates {
			affected, err := gatewaystore.CloseAbandonedSession(tx, key, now)
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
