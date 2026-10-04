package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	exportpkg "github.com/ChargePilot2026/charge-pilot/internal/export"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// ExportTask 提供 CSV、XLSX 和汇总 PDF 导出，限制单次行数。
// 申请时校验资源权限和数据范围；下载时复查权限、归属、范围及有效期。
type ExportTask struct {
	DB        *gorm.DB             // central_db 连接池
	Auth      chargehttp.AdminAuth // 鉴权器,申请与下载两个动作都走它的权限校验
	ExportDir string               // 导出文件目录,留空则用系统临时目录下的 chargepilot-exports
	MaxRows   int                  // 单次导出行数上限,留空默认 50000,超出直接判失败而不是截断
}

// maxRows 返回单次导出的行数上限，未配置时回落到 50000；取数会多取一行用来判断是否超限。
func (t ExportTask) maxRows() int {
	if t.MaxRows > 0 {
		return t.MaxRows
	}
	return 50000
}

// exportDir 返回导出文件目录，未配置时使用系统临时目录下的 chargepilot-exports。
func (t ExportTask) exportDir() string {
	if t.ExportDir != "" {
		return t.ExportDir
	}
	return filepath.Join(os.TempDir(), "chargepilot-exports")
}

// store 返回导出专用的读写入口，使用同一个 central_db 连接池。
func (t ExportTask) store() exportpkg.ExportStore {
	return exportpkg.ExportStore{DB: t.DB}
}

// CleanupExpired 每批清理最多 1000 个过期任务，删除文件并更新状态为 expired。
// 下载窗口为 24 小时，文件路径按任务重新推导，不信任数据库中存储的路径。
func (t ExportTask) CleanupExpired(ctx context.Context) (int, error) {
	db := t.DB.WithContext(ctx)
	rows, err := t.store().ListExpirableExportTasks(db, time.Now().UTC(), 1000)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, row := range rows {
		format := row.FileFormat
		if format == "" {
			format = "csv"
		}
		expected := filepath.Join(t.exportDir(), row.TaskNo+"."+format)
		if row.FilePath != nil && *row.FilePath == expected && filepath.Base(row.TaskNo) == row.TaskNo {
			if err := os.Remove(expected); err != nil && !errors.Is(err, os.ErrNotExist) {
				return removed, err
			}
		}
		if err := t.store().ExpireExportTask(db, row.TaskNo); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// register 注册导出路由：申请与下载需要 export.create，列表与详情需要 finance.read。
func (t ExportTask) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/exports", t.Auth.Require("finance.read"), t.listTasks)
	r.GET("/api/v1/admin/exports/:id", t.Auth.Require("finance.read"), t.detailTask)
	r.POST("/api/v1/admin/exports", t.Auth.Require("export.create"), t.createTask)
	r.GET("/api/v1/admin/exports/:id/download", t.Auth.Require("export.create"), t.downloadTask)
	r.GET("/api/v1/admin/exports/resources", t.Auth.Require("export.create"), t.listResources)
}

// detailTask 返回单个导出任务的状态且不含文件路径；能不能下载由下载接口单独判定。
func (t ExportTask) detailTask(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	row, err := t.store().FindExportTaskByID(t.DB.WithContext(c.Request.Context()), id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	// FilePath 不参与 JSON 响应；下载接口单独校验申请人和资源权限。
	httpapi.OK(c, row)
}

// listResources 返回当前管理员可导出的资源与格式清单，并带上行数上限，
// 前端据此渲染导出选项，避免给出点了必然 403 的组合。
func (t ExportTask) listResources(c *gin.Context) {
	profile := c.MustGet("admin_profile").(operations.Profile)
	scope, err := ops.LoadDataScope(c.Request.Context(), t.DB, profile)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	items := []string{}
	for name, spec := range exportpkg.ExportResources() {
		if profile.HasPermission(spec.Permission) && (scope.Unrestricted || !exportRequiresGlobalScope(name)) {
			items = append(items, name)
		}
	}
	httpapi.OK(c, gin.H{"items": items, "formats": []string{"csv", "xlsx", "pdf"}, "pdf_resources": []string{"bills"}, "max_rows": t.maxRows()})
}

// exportRequiresGlobalScope 限制 bills 仅由全局范围管理员导出。
func exportRequiresGlobalScope(resource string) bool {
	return resource == "bills"
}

// listTasks 分页返回导出任务，支持按状态过滤，按 ID 倒序。
func (t ExportTask) listTasks(c *gin.Context) {
	page, ok := chargehttp.ReadAdminPage(c, "pending running completed failed expired")
	if !ok {
		return
	}
	rows, total, err := t.store().ListExportTasks(t.DB.WithContext(c.Request.Context()), page.Status, page.Page, page.PageSize)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, admin.Page[exportpkg.ExportTaskRow]{Items: rows, Page: page.Page, PageSize: page.PageSize, Total: total})
}

// createTask 受理一次导出申请：校验请求号、资源、格式、权限与数据范围后落一条 pending 任务，
// 随后同步生成文件并直接返回结果；相同请求号重放返回原任务，条件不一致则报冲突。
func (t ExportTask) createTask(c *gin.Context) {
	var in struct {
		RequestID string         `json:"request_id"` // 客户端请求号,必须是 UUID,用于幂等
		Resource  string         `json:"resource"`   // 要导出的资源名,必须已在 exportpkg.ExportResources 登记
		Format    string         `json:"format"`     // 文件格式，留空按 csv；pdf 只支持 bills
		Filter    map[string]any `json:"filter"`     // 筛选条件，bills 必填 from/to 且跨度不超过 31 天
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !operations.ValidRequestID(in.RequestID) {
		httpapi.BadRequest(c, "请提供 UUID 格式的请求号")
		return
	}
	spec, known := exportpkg.ExportResource(in.Resource)
	if !known {
		httpapi.BadRequest(c, "不支持的导出资源")
		return
	}
	if in.Format == "" {
		in.Format = "csv"
	}
	if in.Format != "csv" && in.Format != "xlsx" && in.Format != "pdf" {
		httpapi.BadRequest(c, "导出格式无效")
		return
	}
	if in.Format == "pdf" && in.Resource != "bills" {
		httpapi.BadRequest(c, "PDF 仅支持账单")
		return
	}
	if in.Resource == "bills" && !operations.ValidExportPeriod(in.Filter) {
		httpapi.BadRequest(c, "账单导出需提供不超过 31 天的 from/to 日期")
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	if !profile.HasPermission(spec.Permission) {
		httpapi.Write(c, 403, 1003, "没有导出该资源的权限", nil)
		return
	}
	if exportRequiresGlobalScope(in.Resource) {
		scope, err := ops.LoadDataScope(c.Request.Context(), t.DB, profile)
		if err != nil {
			chargehttp.AdminResourceFailure(c, err)
			return
		}
		if !scope.Unrestricted {
			httpapi.Write(c, 403, 1003, "该财务资源只允许全局数据范围导出", nil)
			return
		}
	}
	// 128 位 UUID 一个位都不截。早先砍掉尾部字节，导致前缀相同、
	// 请求号其实不同的两个请求撞在了一起。
	taskNo := "EXP" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	filter, err := json.Marshal(in.Filter)
	if err != nil {
		httpapi.BadRequest(c, "导出筛选条件无效")
		return
	}
	var replayed *exportpkg.ExportTaskRow
	err = t.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		existing, found := t.store().FindExportTaskByNo(tx, taskNo)
		if found == nil {
			var savedFilter map[string]any
			if existing.FilterJSON != nil {
				_ = json.Unmarshal([]byte(*existing.FilterJSON), &savedFilter)
			}
			if existing.RequestedBy != profile.ID || existing.Resource != in.Resource || existing.FileFormat != in.Format || !reflect.DeepEqual(savedFilter, in.Filter) {
				return admin.ErrConflict
			}
			replayed = &existing
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		if err := t.store().InsertExportTask(tx, exportpkg.ExportTaskInsert{
			TaskNo: taskNo, Resource: in.Resource, FileFormat: in.Format, RequestedBy: profile.ID,
			FilterJSON: string(filter), ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		}); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, profile, "create", "export_task", 0, nil, gin.H{"resource": in.Resource, "task_no": taskNo}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if replayed != nil {
		httpapi.OK(c, gin.H{"task_no": taskNo, "status": replayed.Status, "row_count": replayed.RowCount, "expires_at": replayed.ExpiresAt})
		return
	}
	// 在行数上限内同步生成文件，响应直接返回完成状态。
	if err := t.run(c.Request.Context(), taskNo, spec, profile); err != nil {
		httpapi.OK(c, gin.H{"task_no": taskNo, "status": "failed", "message": "导出失败，请稍后重试"})
		return
	}
	var finished exportpkg.ExportTaskRow
	if r, err := t.store().FindExportTaskByNo(t.DB.WithContext(c.Request.Context()), taskNo); err == nil {
		finished = r
	}
	httpapi.OK(c, gin.H{"task_no": taskNo, "status": finished.Status, "row_count": finished.RowCount, "expires_at": finished.ExpiresAt})
}

// run 按资源、数据范围和日期取数，脱敏后写入目标格式。
// 先写 .partial 文件，再原子改名，避免下载未完成文件；任一步失败将任务置为 failed。
func (t ExportTask) run(ctx context.Context, taskNo string, spec exportpkg.ExportSpec, profile operations.Profile) error {
	dir := t.exportDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return t.fail(ctx, taskNo, err)
	}
	row, err := t.store().FindExportTaskByNo(t.DB.WithContext(ctx), taskNo)
	if err != nil {
		return err
	}
	scope, err := ops.LoadDataScope(ctx, t.DB, profile)
	if err != nil {
		return t.fail(ctx, taskNo, err)
	}
	if exportRequiresGlobalScope(row.Resource) && !scope.Unrestricted {
		return t.fail(ctx, taskNo, errors.New("scoped finance export is not available"))
	}
	var filters map[string]any
	if row.FilterJSON != nil && *row.FilterJSON != "" {
		_ = json.Unmarshal([]byte(*row.FilterJSON), &filters)
	}
	if filters == nil {
		filters = map[string]any{}
	}
	// 多取一行，用来判断是否超出单次行数上限。
	records, err := t.store().QueryExportSource(row.Resource, scope, filters, t.maxRows()+1)
	if err != nil {
		return t.fail(ctx, taskNo, err)
	}
	if len(records) > t.maxRows() {
		return t.fail(ctx, taskNo, fmt.Errorf("导出超过 %d 行，请缩小范围", t.maxRows()))
	}
	mask, err := ops.LoadFieldMask(ctx, t.DB, profile.RoleID)
	if err != nil {
		return t.fail(ctx, taskNo, err)
	}
	names := make([]string, 0, len(spec.Columns))
	for _, column := range spec.Columns {
		names = append(names, column.Column)
	}
	records = mask.MaskSlice(spec.Table, records)

	format := row.FileFormat
	if format == "" {
		format = "csv"
	}
	path := filepath.Join(dir, taskNo+"."+format)
	pending := path + ".partial"
	file, err := os.OpenFile(pending, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return t.fail(ctx, taskNo, err)
	}
	headers := make([]string, 0, len(spec.Columns))
	for _, column := range spec.Columns {
		headers = append(headers, column.Header)
	}
	lines := make([][]string, 0, len(records))
	for _, record := range records {
		line := make([]string, 0, len(names))
		for _, name := range names {
			line = append(line, stringifyCell(record[name]))
		}
		lines = append(lines, line)
	}
	if err := writeExport(file, format, row.Resource, headers, lines, filters); err != nil {
		file.Close()
		os.Remove(pending)
		return t.fail(ctx, taskNo, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(pending)
		return t.fail(ctx, taskNo, err)
	}
	if err := os.Rename(pending, path); err != nil {
		os.Remove(pending)
		return t.fail(ctx, taskNo, err)
	}
	return t.store().MarkExportCompleted(t.DB.WithContext(ctx), taskNo, len(records), path, time.Now().UTC())
}

// fail 把任务标记为失败并记录截断后的错误原因，同时把错误原样返回给调用方。
func (t ExportTask) fail(ctx context.Context, taskNo string, cause error) error {
	_ = t.store().MarkExportFailed(t.DB.WithContext(ctx), taskNo, truncate(cause.Error(), 255))
	return cause
}

// stringifyCell 把驱动返回的任意值转成导出用字符串：nil 输出空串，[]byte 按文本解包。
func stringifyCell(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case []byte:
		return string(v)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// truncate 按字节上限截断错误信息。
func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

// downloadTask 每次下载复查归属、资源权限、数据范围及有效期。
// 过期文件被删除，任务置为 expired；权限撤销后禁止继续下载。
func (t ExportTask) downloadTask(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	row, err := t.store().LockExportTaskByID(t.DB.WithContext(c.Request.Context()), id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if row.RequestedBy != profile.ID && !profile.HasPermission("admin_user.read") {
		httpapi.Write(c, 403, 1003, "只能下载自己创建的导出文件", nil)
		return
	}
	spec, known := exportpkg.ExportResource(row.Resource)
	if !known || !profile.HasPermission(spec.Permission) {
		// 下载时重新校验资源读取权限。
		httpapi.Write(c, 403, 1003, "没有下载该导出的权限", nil)
		return
	}
	if exportRequiresGlobalScope(row.Resource) {
		scope, err := ops.LoadDataScope(c.Request.Context(), t.DB, profile)
		if err != nil {
			chargehttp.AdminResourceFailure(c, err)
			return
		}
		if !scope.Unrestricted {
			httpapi.Write(c, 403, 1003, "当前数据范围无权下载该财务导出", nil)
			return
		}
	}
	if row.Status == "expired" {
		httpapi.Write(c, http.StatusGone, httpapi.CodeResourceExpired, "导出文件已过期，请重新导出", nil)
		return
	}
	if row.Status != "completed" || row.FilePath == nil {
		httpapi.Write(c, 409, 2009, "导出尚未完成", nil)
		return
	}
	if row.ExpiresAt != nil && *row.ExpiresAt != "" {
		expiry, err := time.Parse("2006-01-02 15:04:05.999", *row.ExpiresAt)
		if err == nil && time.Now().UTC().After(expiry) {
			_ = t.store().ExpireExportTask(t.DB.WithContext(c.Request.Context()), row.TaskNo)
			_ = os.Remove(*row.FilePath)
			httpapi.Write(c, http.StatusGone, httpapi.CodeResourceExpired, "导出文件已过期，请重新导出", nil)
			return
		}
	}
	format := row.FileFormat
	if format == "" {
		format = "csv"
	}
	mime := map[string]string{"csv": "text/csv; charset=utf-8", "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "pdf": "application/pdf"}[format]
	if mime == "" {
		httpapi.Write(c, 409, 2009, "导出格式不可用", nil)
		return
	}
	c.Header("Content-Type", mime)
	c.Header("Content-Disposition", `attachment; filename="`+row.TaskNo+`.`+format+`"`)
	c.File(*row.FilePath)
}
