package schedule

import (
	workerrepo "chargepilot/internal/worker/repo"
	"errors"
	"net/http"
	"strings"

	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 提供 worker 定时任务查询和手动触发接口。
type API struct {
	Scheduler    Scheduler // 已登记处理器及 worker 仓储连接，不在 HTTP 创建业务任务。
	ServiceToken string    // 内部服务身份，配置为空仍拒绝。
}

// Register 注册最近执行查询和手动触发接口，均先校验服务令牌。
func (a API) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/scheduled-tasks/:task_code/last-run", a.authorized(), a.lastRun)
	router.POST("/api/v1/internal/scheduled-tasks/:task_code/trigger", a.authorized(), a.trigger)
}

// authorized 校验服务令牌；失败时返回 401 并停止后续处理。
func (a API) authorized() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

// task 校验任务编码和已登记处理器，再读取任务。
// 输入无效、处理器或任务不存在、读取失败时分别返回 400、404、503，并返回 false。
func (a API) task(c *gin.Context) (Task, bool) {
	code := strings.TrimSpace(c.Param("task_code"))
	if code == "" || len(code) > 64 {
		httpapi.BadRequest(c, "task_code 无效")
		return Task{}, false
	}
	if a.Scheduler.Handlers[code] == nil {
		httpapi.Write(c, http.StatusNotFound, httpapi.CodeNotFound, "定时任务不存在", nil)
		return Task{}, false
	}
	task, err := workerrepo.FindTaskByCode(c.Request.Context(), a.Scheduler.DB, code)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusNotFound, httpapi.CodeNotFound, "定时任务不存在", nil)
		return Task{}, false
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "定时任务暂时无法读取", nil)
		return Task{}, false
	}
	return task, true
}

// lastRun 返回任务最近执行时刻、状态、耗时、错误和下次计划。没有执行记录时，执行相关字段返回 null。
func (a API) lastRun(c *gin.Context) {
	task, ok := a.task(c)
	if !ok {
		return
	}
	row, err := workerrepo.LastExecution(c.Request.Context(), a.Scheduler.DB, task.Code)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "执行记录暂时无法读取", nil)
		return
	}
	var duration any
	var status any
	var lastRun any
	var errorMessage any
	if err == nil {
		status, lastRun, errorMessage = row.Status, row.StartedAt.UTC(), row.ErrorMsg
		if row.FinishedAt != nil {
			duration = row.FinishedAt.Sub(row.StartedAt).Milliseconds()
		}
	}
	httpapi.OK(c, gin.H{"task_code": task.Code, "enabled": task.Enabled, "last_run_at": lastRun,
		"last_run_status": status, "last_run_duration_ms": duration, "error_message": errorMessage,
		"consecutive_fail_count": task.ConsecutiveFailCount, "next_run_at": task.NextRunAt})
}

// trigger 校验 trigger_reason 和 force 后执行任务；未领取租约时返回 409，执行失败返回 503。
func (a API) trigger(c *gin.Context) {
	task, ok := a.task(c)
	if !ok {
		return
	}
	var input struct {
		TriggerReason string `json:"trigger_reason"` // 手动原因，trim 后非空且字节长度最多 256。
		Force         bool   `json:"force"`          // 允许暂停任务执行，不绕过租约竞争。
	}
	if err := c.ShouldBindJSON(&input); err != nil || len(strings.TrimSpace(input.TriggerReason)) == 0 || len(input.TriggerReason) > 256 {
		httpapi.BadRequest(c, "trigger_reason 须为 1 至 256 字符")
		return
	}
	if !task.Enabled && !input.Force {
		httpapi.Write(c, http.StatusConflict, httpapi.CodeTaskConflict, "任务已暂停", nil)
		return
	}
	completed, err := a.Scheduler.execute(c.Request.Context(), task, input.Force, workerrepo.TaskTriggeredByAdminAPI, strings.TrimSpace(input.TriggerReason))
	if !completed && err == nil {
		httpapi.Write(c, http.StatusConflict, httpapi.CodeTaskConflict, "任务正在执行", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "任务执行失败: "+err.Error(), nil)
		return
	}
	httpapi.OK(c, gin.H{"task_code": task.Code, "status": "success"})
}
