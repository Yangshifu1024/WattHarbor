// Package export 校验导出权限和数据范围，同步生成文件，并负责下载校验和到期清理。
package export

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/ops"
	"wattharbor/internal/persist"
)

// InputError 表示导出请求参数不合法，提示不含存储细节。
type InputError struct {
	Message string /* 公开参数提示 */
}

// Error 返回请求参数提示。
func (e *InputError) Error() string { return e.Message }

// ForbiddenError 表示当前权限、归属或范围不足，HTTP 映射403。
type ForbiddenError struct {
	Message string /* 公开拒绝提示 */
}

// Error 返回权限拒绝提示，不包含数据库错误。
func (e *ForbiddenError) Error() string { return e.Message }

// DownloadError 表示当前状态不允许下载，过期返回 410，其他状态返回 409。
type DownloadError struct {
	Message string /* 当前状态提示 */
	Expired bool   /* 24 小时截止或 expired 状态 */
}

// Error 返回公开下载状态提示。
func (e *DownloadError) Error() string { return e.Message }

// CreateInput 是导出申请，空格式默认 CSV，过滤条件保留 null 与空对象区别。
type CreateInput struct {
	RequestID string         `json:"request_id"` // 完整 UUID，任务号
	Resource  string         `json:"resource"`   // 已登记只读资源
	Format    string         `json:"format"`     // csv／xlsx，空值默认 csv
	Filter    map[string]any `json:"filter"`     // 筛选，账单限定 from／to
}

// CreateResult 是同步创建或重放结果，Message 非空时 HTTP 返回 failed 和公开提示。
// Status 是对外契约字段（OpenAPI 依赖）：自定义字符串类型序列化为字符串，内容逐字节不变。
type CreateResult struct {
	TaskNo    string       // 同一任务号
	Status    ExportStatus // 同步状态
	RowCount  uint32       // 任务已保存的导出行数，尚未完成时为零
	ExpiresAt *string      // 可空公开截止字符串
	Message   string       // 已持久失败的公开提示
}

// ResourcesResult 返回可导出资源，不暴露来源表或 SQL 查询对象。
type ResourcesResult struct {
	Items   []string `json:"items"`    // 当前权限及范围允许的资源
	Formats []string `json:"formats"`  // csv／xlsx 清单
	MaxRows int      `json:"max_rows"` // 单次上限
}

// ExportService 校验导出请求并同步生成、发布文件，不另起后台任务。
type ExportService struct {
	DB        *gorm.DB         // 中央导出任务及来源查询连接
	ExportDir string           // 导出根目录；空值使用系统临时目录下的 wattharbor-exports
	MaxRows   int              // 单次上限，非正数默认50000
	Clock     func() time.Time // 可注入测试时钟，nil 使用实际墙钟；到期检查须在等待锁后调用
}

// now 在每个需要判断的步骤读取 UTC 当前时间，不缓存请求开始时间。
func (s ExportService) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}

// maxRows 返回单次导出行数上限。
func (s ExportService) maxRows() int {
	if s.MaxRows > 0 {
		return s.MaxRows
	}
	return 50000
}

// exportDir 返回配置的可信根目录，不采用数据库路径或请求参数。
func (s ExportService) exportDir() string {
	if s.ExportDir != "" {
		return s.ExportDir
	}
	return filepath.Join(os.TempDir(), "wattharbor-exports")
}

// repo 返回当前连接的仓储，不创建新连接池。
func (s ExportService) repo() ExportRepo { return ExportRepo{DB: s.DB} }

// actor 重新读取账号，核对认证版本和当前所需权限。
func (s ExportService) actor(ctx context.Context, original operations.Profile, permission string) (operations.Profile, error) {
	if s.DB == nil {
		return operations.Profile{}, errors.New("export unavailable")
	}
	p, err := (ops.AccountRepo{DB: s.DB}).Profile(ctx, original.ID)
	if err != nil {
		return p, err
	}
	if p.AuthVersion != original.AuthVersion {
		return p, operations.ErrCredentials
	}
	if !p.HasPermission(permission) {
		return p, &ForbiddenError{"没有导出该资源的权限"}
	}
	return p, nil
}

// lockedActor 按角色集合→账号→角色及权限加锁并当前读，沿用管理写入锁序。
// 发布文件前核对最新权限，避免旧 RR 快照或角色变化使撤销权限仍生效。
func (s ExportService) lockedActor(tx *gorm.DB, original operations.Profile, permission string) (operations.Profile, error) {
	repo := ops.AccountRepo{DB: tx}
	if err := repo.LockCustomerAdminRole(tx); err != nil {
		return operations.Profile{}, err
	}
	a, err := repo.LockActiveAdmin(tx, original.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return operations.Profile{}, operations.ErrCredentials
	}
	if err != nil {
		return operations.Profile{}, err
	}
	if a.AuthVersion != original.AuthVersion {
		return operations.Profile{}, operations.ErrCredentials
	}
	r, err := (ops.RoleRepo{DB: tx}).LockRole(tx, a.RoleID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return operations.Profile{}, operations.ErrCredentials
	}
	if err != nil {
		return operations.Profile{}, err
	}
	codes, err := (ops.RoleRepo{DB: tx}).RolePermissionCodesInTx(tx, a.RoleID)
	if err != nil {
		return operations.Profile{}, err
	}
	p := operations.Profile{ID: a.ID, Username: a.Username, AuthVersion: a.AuthVersion, RoleID: a.RoleID, Role: r.Code, Permissions: codes}
	if !p.HasPermission(permission) {
		return p, &ForbiddenError{"没有导出该资源的权限"}
	}
	return p, nil
}

// Resources 核对当前权限和范围后返回可导出资源，不创建任务。
func (s ExportService) Resources(ctx context.Context, original operations.Profile) (ResourcesResult, error) {
	p, err := s.actor(ctx, original, "export.create")
	if err != nil {
		return ResourcesResult{}, err
	}
	scope, err := ops.LoadDataScope(ctx, s.DB, p)
	if err != nil {
		return ResourcesResult{}, err
	}
	out := ResourcesResult{Items: []string{}, Formats: []string{string(ExportFormatCSV), string(ExportFormatXLSX)}, MaxRows: s.maxRows()}
	for name, spec := range ExportResources() {
		if p.HasPermission(spec.Permission) && (scope.Unrestricted || name != "bills") {
			out.Items = append(out.Items, name)
		}
	}
	return out, nil
}

// List 核对 finance.read 权限和分页参数后读取全局导出任务。
func (s ExportService) List(ctx context.Context, original operations.Profile, page persist.PageQuery) (persist.Page[ExportTaskRow], error) {
	if page.Page < 1 || page.Page > 1000000 || page.PageSize < 1 || page.PageSize > 100 {
		return persist.Page[ExportTaskRow]{}, &InputError{"分页参数无效"}
	}
	if page.Status != "" && !ExportStatus(page.Status).Known() {
		return persist.Page[ExportTaskRow]{}, &InputError{"关键词过长或状态无效"}
	}
	if _, err := s.actor(ctx, original, "finance.read"); err != nil {
		return persist.Page[ExportTaskRow]{}, err
	}
	rows, total, err := s.repo().ListExportTasks(s.DB.WithContext(ctx), page.Status, page.Page, page.PageSize)
	return persist.Page[ExportTaskRow]{Items: rows, Total: total, Page: page.Page, PageSize: page.PageSize}, err
}

// Detail 核对 finance.read 权限后读取任务状态。
func (s ExportService) Detail(ctx context.Context, original operations.Profile, id uint64) (ExportTaskRow, error) {
	if _, err := s.actor(ctx, original, "finance.read"); err != nil {
		return ExportTaskRow{}, err
	}
	return s.repo().FindExportTaskByID(s.DB.WithContext(ctx), id)
}

// validate 检查 UUID、资源、格式和账单时间范围，返回默认格式及资源定义。
func (in CreateInput) validate() (CreateInput, ExportSpec, error) {
	if !operations.ValidRequestID(in.RequestID) {
		return in, ExportSpec{}, &InputError{"请提供 UUID 格式的请求号"}
	}
	spec, ok := ExportResource(in.Resource)
	if !ok {
		return in, spec, &InputError{"不支持的导出资源"}
	}
	if in.Format == "" {
		in.Format = string(ExportFormatCSV)
	}
	if !ExportFileFormat(in.Format).Known() {
		return in, spec, &InputError{"导出格式无效"}
	}
	if in.Resource == "bills" && !operations.ValidExportPeriod(in.Filter) {
		return in, spec, &InputError{"账单导出需提供不超过 31 天的 from/to 日期"}
	}
	return in, spec, nil
}

// Create 使用完整 UUID 受理并同步生成，登记、当前读重放和首次审计同事务。
// pending 可用同请求恢复，failed／completed 重放不换任务号。
func (s ExportService) Create(ctx context.Context, audit ops.AuditContext, in CreateInput) (CreateResult, error) {
	in, spec, err := in.validate()
	if err != nil {
		return CreateResult{}, err
	}
	if s.DB == nil {
		return CreateResult{}, errors.New("export unavailable")
	}
	filter, err := json.Marshal(in.Filter)
	if err != nil {
		return CreateResult{}, &InputError{"导出筛选条件无效"}
	}
	taskNo := "EXP" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	var row ExportTaskRow
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p, err := s.lockedActor(tx, audit.Actor, "export.create")
		if err != nil {
			return err
		}
		if !p.HasPermission(spec.Permission) {
			return &ForbiddenError{"没有导出该资源的权限"}
		}
		scope, err := ops.LockDataScopeInTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if in.Resource == "bills" && !scope.Unrestricted {
			return &ForbiddenError{"该财务资源只允许全局数据范围导出"}
		}
		inserted, err := s.repo().ClaimExportTask(tx, ExportTaskInsert{TaskNo: taskNo, Resource: in.Resource, FileFormat: ExportFileFormat(in.Format), RequestedBy: p.ID, FilterJSON: string(filter), ExpiresAt: s.now().Add(24 * time.Hour)})
		if err != nil {
			return err
		}
		row, err = s.repo().LockExportTaskByNo(tx, taskNo)
		if err != nil {
			return err
		}
		var saved map[string]any
		if row.FilterJSON != nil {
			if err := json.Unmarshal([]byte(*row.FilterJSON), &saved); err != nil {
				return err
			}
		}
		if row.RequestedBy != p.ID || row.Resource != in.Resource || string(row.FileFormat) != in.Format || !reflect.DeepEqual(saved, in.Filter) {
			return operations.ErrConflict
		}
		if !inserted {
			return nil
		}
		audit.Actor = p
		// 创建审计的目标 ID 保持为 0，X-Request-ID 由 HTTP 传入，不能用资源任务号替换。
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "create", Target: "export_task", After: map[string]any{"resource": in.Resource, "task_no": taskNo}})
	})
	if err != nil {
		return CreateResult{}, err
	}
	if row.Status == ExportStatusPending || row.Status == ExportStatusRunning {
		if err := s.Generate(ctx, taskNo, audit.Actor); err != nil {
			var failed *generationFailure
			if errors.As(err, &failed) {
				return CreateResult{TaskNo: taskNo, Status: ExportStatusFailed, Message: "导出失败，请稍后重试"}, nil
			}
			return CreateResult{}, err
		}
		row, err = s.repo().FindExportTaskByNo(s.DB.WithContext(ctx), taskNo)
		if err != nil {
			return CreateResult{}, err
		}
	}
	return CreateResult{TaskNo: taskNo, Status: row.Status, RowCount: row.RowCount, ExpiresAt: row.ExpiresAt}, nil
}

// generationFailure 表示生成错误及 failed 状态已保存，HTTP 仍返回 200 和 failed 结果。
type generationFailure struct {
	cause error /* 生成取消、IO 或查询错误 */
}

// Error 返回内部生成原因，仅用于错误链和日志。
func (e *generationFailure) Error() string { return e.cause.Error() }

// Unwrap 返回底层取消、文件或查询错误，支持 errors.Is／As。
func (e *generationFailure) Unwrap() error { return e.cause }
