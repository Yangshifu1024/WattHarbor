package export

import (
	"errors"
	"net/http"

	"chargepilot/internal/export"
	adminhttp "chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// ExportTask 只持有导出服务及请求鉴权，不读取数据库路径或执行文件／状态事务。
type ExportTask struct {
	Service export.ExportService /* 任务、生成和下载完整用例 */
	Auth    authhttp.AdminAuth   /* HTTP 鉴权 */
}

// Register 在 r 挂载五条导出路由，保留 finance.read／export.create 权限及公开 URL。
func (t ExportTask) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/exports", t.Auth.Require("finance.read"), t.listTasks)
	r.GET("/api/v1/admin/exports/:id", t.Auth.Require("finance.read"), t.detailTask)
	r.POST("/api/v1/admin/exports", t.Auth.Require("export.create"), t.createTask)
	r.GET("/api/v1/admin/exports/:id/download", t.Auth.Require("export.create"), t.downloadTask)
	r.GET("/api/v1/admin/exports/resources", t.Auth.Require("export.create"), t.listResources)
}

// failure 将 err 映射参数 400、权限 403、状态 409／到期 410 及公共存储错误，不暴露 IO 细节。
func failure(c *gin.Context, err error) {
	var input *export.InputError
	var denied *export.ForbiddenError
	var state *export.DownloadError
	switch {
	case errors.As(err, &input):
		httpapi.BadRequest(c, input.Message)
	case errors.As(err, &denied):
		httpapi.Write(c, 403, 1003, denied.Message, nil)
	case errors.As(err, &state):
		if state.Expired {
			httpapi.Write(c, http.StatusGone, httpapi.CodeResourceExpired, state.Message, nil)
		} else {
			httpapi.Write(c, 409, 2009, state.Message, nil)
		}
	default:
		adminhttp.AdminManagementFailure(c, err)
	}
}

// detailTask 从请求提取 id 及身份，返回任务状态或错误，不返回服务端路径。
func (t ExportTask) detailTask(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	row, err := t.Service.Detail(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor, id)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// listTasks 从请求提取分页和身份，返回全局任务分页或错误，不构造 SQL。
func (t ExportTask) listTasks(c *gin.Context) {
	page, ok := adminhttp.ReadAdminPage(c, "pending running completed failed expired")
	if !ok {
		return
	}
	out, err := t.Service.List(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor, page)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// listResources 将当前账号身份传给资源权限检查，输出清单、格式和上限，不决定业务范围。
func (t ExportTask) listResources(c *gin.Context) {
	out, err := t.Service.Resources(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// createTask 从请求严格读取申请及审计信息，返回同步回执或错误，不组合事务／文件。
func (t ExportTask) createTask(c *gin.Context) {
	var in export.CreateInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	// 不支持格式仍先于身份 IO 拒绝；其它业务验证及默认值由服务执行。
	audit := adminhttp.AdminAuditContext(c)
	audit.RequestID = c.GetHeader("X-Request-ID") // 创建审计使用请求头，不补造关联号
	out, err := t.Service.Create(c.Request.Context(), audit, in)
	if err != nil {
		failure(c, err)
		return
	}
	if out.Message != "" {
		httpapi.OK(c, gin.H{"task_no": out.TaskNo, "status": out.Status, "message": out.Message})
		return
	}
	httpapi.OK(c, gin.H{"task_no": out.TaskNo, "status": out.Status, "row_count": out.RowCount, "expires_at": out.ExpiresAt})
}

// downloadTask 从请求提取 id／身份，流式输出用例授权的句柄；请求结束关闭文件，不再使用数据库路径。
func (t ExportTask) downloadTask(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	out, err := t.Service.GetDownload(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor, id)
	if err != nil {
		failure(c, err)
		return
	}
	defer out.File.Close()
	c.Header("Content-Type", out.MIME)
	c.Header("Content-Disposition", "attachment; filename=\""+out.Name+"\"")
	http.ServeContent(c.Writer, c.Request, out.Name, out.ModifiedAt, out.File)
}
