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

// API 适配原 worker 定时任务查询与手动触发端点，执行编排委托 Scheduler。
type API struct {
	Scheduler    Scheduler // 已登记处理器及 worker 仓储连接，不在 HTTP 创建业务任务。
	ServiceToken string    // 内部服务身份，配置为空仍拒绝。
}

// Register 在 router 挂载原 GET last-run 与 POST trigger 路径，保留先鉴权后读取，无启动期 IO。
func (a API) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/scheduled-tasks/:task_code/last-run", a.authorized(), a.lastRun)
	router.POST("/api/v1/internal/scheduled-tasks/:task_code/trigger", a.authorized(), a.trigger)
}

// authorized 返回服务令牌中间件，身份失败写唯一401并 Abort，成功继续后续处理器；不访问业务库。
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

// task 校验 c 的 task_code 和处理器登记后读取原任务，返回记录和可继续标志。
// 无效、未登记、缺行或读取失败写原400／404／503并返回 false，不领取租约或执行处理器。
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

// lastRun 在 c 下读取原任务及最近执行日志，输出原时间、状态、错误、失败数及下次计划字段。
// 未执行过保留 null 形状；读取失败映射原503，不执行任务或修改记录。
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

// trigger 校验 c 的任务与原 trigger_reason／force 后调用 Scheduler.execute，输出原成功或错误信封。
// 原未领取或暂停映射409，执行失败503，输入失败400；实际日志、租约及处理器副作用由调度用例拥有。
func (a API) trigger(c *gin.Context) {
	task, ok := a.task(c)
	if !ok {
		return
	}
	var input struct {
		TriggerReason string `json:"trigger_reason"` // 原手动原因，trim 后非空且原字节长度最多256。
		Force         bool   `json:"force"`          // 允许原暂停任务执行，不绕过租约竞争。
	}
	if err := c.ShouldBindJSON(&input); err != nil || len(strings.TrimSpace(input.TriggerReason)) == 0 || len(input.TriggerReason) > 256 {
		httpapi.BadRequest(c, "trigger_reason 须为 1 至 256 字符")
		return
	}
	if !task.Enabled && !input.Force {
		httpapi.Write(c, http.StatusConflict, httpapi.CodeTaskConflict, "任务已暂停", nil)
		return
	}
	completed, err := a.Scheduler.execute(c.Request.Context(), task, input.Force, "admin_api", strings.TrimSpace(input.TriggerReason))
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
