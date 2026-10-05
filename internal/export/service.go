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

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
)

// InputError 保留原导出输入拒绝文案，不包含存储细节。
type InputError struct {
	Message string /* 原公开参数提示 */
}

// Error 返回e原公开输入提示，无IO。
func (e *InputError) Error() string { return e.Message }

// ForbiddenError 表示当前权限、归属或范围不足，HTTP映射403。
type ForbiddenError struct {
	Message string /* 原公开拒绝提示 */
}

// Error 返回e原公开准入提示，不包含账号或存储材料。
func (e *ForbiddenError) Error() string { return e.Message }

// DownloadError 表示导出状态不提供下载；Expired区分410与原409。
type DownloadError struct {
	Message string /* 原状态提示 */
	Expired bool   /* 原24小时截止或expired状态 */
}

// Error 返回e原公开下载状态提示，无IO。
func (e *DownloadError) Error() string { return e.Message }

// CreateInput 保留原导出申请字段，Format空值默认为CSV，Filter原null与空对象不同。
type CreateInput struct {
	RequestID string         `json:"request_id"` // 完整UUID，原任务身份
	Resource  string         `json:"resource"`   // 已登记只读资源
	Format    string         `json:"format"`     // csv／xlsx，空值默认csv
	Filter    map[string]any `json:"filter"`     // 原筛选，账单限定from／to
}

// CreateResult 是原同步创建／重放结果；Message非空时HTTP只返回failed及公开提示。
type CreateResult struct {
	TaskNo    string  // 原任务号
	Status    string  // 原同步状态
	RowCount  uint32  // 实际行数，普通成功／重放回执保留零
	ExpiresAt *string // 原可空公开截止字符串
	Message   string  // 已持久失败的公开提示
}

// ResourcesResult 是原可导出资源清单，不向HTTP暴露SQL构造器或表名。
type ResourcesResult struct {
	Items   []string `json:"items"`    // 当前权限及范围允许的资源
	Formats []string `json:"formats"`  // 原csv／xlsx清单
	MaxRows int      `json:"max_rows"` // 单次上限
}

// ExportService 拥有同步任务、文件发布和准入；没有未受管理的goroutine或独立worker。
type ExportService struct {
	DB        *gorm.DB         // 所属任务及跨模块只读投影数据库
	ExportDir string           // cmd注入的可信文件根目录，空值沿用系统临时目录
	MaxRows   int              // 原单次上限，非正数默认50000
	Clock     func() time.Time // 可注入测试时钟，nil使用实际墙钟；到期准入须在等待锁后调用
}

// now 返回s当前UTC时刻，在需要判定的业务步骤求值，不缓存请求开始时间。
func (s ExportService) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}

// maxRows 返回s原单次上限，无IO。
func (s ExportService) maxRows() int {
	if s.MaxRows > 0 {
		return s.MaxRows
	}
	return 50000
}

// exportDir 返回s原可信根目录，不接受数据库路径或请求参数。
func (s ExportService) exportDir() string {
	if s.ExportDir != "" {
		return s.ExportDir
	}
	return filepath.Join(os.TempDir(), "chargepilot-exports")
}

// repo 返回s所属连接的仓储，不打开新池。
func (s ExportService) repo() ExportRepo { return ExportRepo{DB: s.DB} }

// actor 在ctx下重读original账号及原版本并要求permission，返回当前档案或凭据／权限／存储错误。
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

// lockedActor 在tx下按集合→账号→角色／权限当前锁读original，返回permission所需档案或准入错误。
// 与管理变更沿用相同锁序，防止旧RR快照或角色变化使文件发布继承旧权限。
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

// Resources 在ctx下核对original当前权限和范围，返回原清单或准入／存储错误，不生成任务。
func (s ExportService) Resources(ctx context.Context, original operations.Profile) (ResourcesResult, error) {
	p, err := s.actor(ctx, original, "export.create")
	if err != nil {
		return ResourcesResult{}, err
	}
	scope, err := ops.LoadDataScope(ctx, s.DB, p)
	if err != nil {
		return ResourcesResult{}, err
	}
	out := ResourcesResult{Items: []string{}, Formats: []string{"csv", "xlsx"}, MaxRows: s.maxRows()}
	for name, spec := range ExportResources() {
		if p.HasPermission(spec.Permission) && (scope.Unrestricted || name != "bills") {
			out.Items = append(out.Items, name)
		}
	}
	return out, nil
}

// List 在ctx下核对original的finance.read及page，返回原全局任务分页或输入／准入／存储错误。
func (s ExportService) List(ctx context.Context, original operations.Profile, page persist.PageQuery) (persist.Page[ExportTaskRow], error) {
	if page.Page < 1 || page.Page > 1000000 || page.PageSize < 1 || page.PageSize > 100 {
		return persist.Page[ExportTaskRow]{}, &InputError{"分页参数无效"}
	}
	if page.Status != "" && page.Status != "pending" && page.Status != "running" && page.Status != "completed" && page.Status != "failed" && page.Status != "expired" {
		return persist.Page[ExportTaskRow]{}, &InputError{"关键词过长或状态无效"}
	}
	if _, err := s.actor(ctx, original, "finance.read"); err != nil {
		return persist.Page[ExportTaskRow]{}, err
	}
	rows, total, err := s.repo().ListExportTasks(s.DB.WithContext(ctx), page.Status, page.Page, page.PageSize)
	return persist.Page[ExportTaskRow]{Items: rows, Total: total, Page: page.Page, PageSize: page.PageSize}, err
}

// Detail 在ctx下核对original的finance.read，按id返回原任务状态或准入／缺失／存储错误。
func (s ExportService) Detail(ctx context.Context, original operations.Profile, id uint64) (ExportTaskRow, error) {
	if _, err := s.actor(ctx, original, "finance.read"); err != nil {
		return ExportTaskRow{}, err
	}
	return s.repo().FindExportTaskByID(s.DB.WithContext(ctx), id)
}

// validate 核对in原UUID、资源、格式和账单窗口，返回默认格式及所属声明或原参数错误，无IO。
func (in CreateInput) validate() (CreateInput, ExportSpec, error) {
	if !operations.ValidRequestID(in.RequestID) {
		return in, ExportSpec{}, &InputError{"请提供 UUID 格式的请求号"}
	}
	spec, ok := ExportResource(in.Resource)
	if !ok {
		return in, spec, &InputError{"不支持的导出资源"}
	}
	if in.Format == "" {
		in.Format = "csv"
	}
	if in.Format != "csv" && in.Format != "xlsx" {
		return in, spec, &InputError{"导出格式无效"}
	}
	if in.Resource == "bills" && !operations.ValidExportPeriod(in.Filter) {
		return in, spec, &InputError{"账单导出需提供不超过 31 天的 from/to 日期"}
	}
	return in, spec, nil
}

// Create 在ctx下按audit及in受理完整UUID并同步生成，返回原回执或输入／准入／事务错误。
// 唯一键登记后当前读取原申请，申请和首次审计一起提交；pending重放可恢复，failed／completed不换任务号。
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
		inserted, err := s.repo().ClaimExportTask(tx, ExportTaskInsert{TaskNo: taskNo, Resource: in.Resource, FileFormat: in.Format, RequestedBy: p.ID, FilterJSON: string(filter), ExpiresAt: s.now().Add(24 * time.Hour)})
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
		if row.RequestedBy != p.ID || row.Resource != in.Resource || row.FileFormat != in.Format || !reflect.DeepEqual(saved, in.Filter) {
			return operations.ErrConflict
		}
		if !inserted {
			return nil
		}
		audit.Actor = p
		// 原导出创建审计目标ID仍为0、原X-Request-ID由HTTP传入，不补换资源任务号。
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "create", Target: "export_task", After: map[string]any{"resource": in.Resource, "task_no": taskNo}})
	})
	if err != nil {
		return CreateResult{}, err
	}
	if row.Status == "pending" || row.Status == "running" {
		if err := s.Generate(ctx, taskNo, audit.Actor); err != nil {
			var failed *generationFailure
			if errors.As(err, &failed) {
				return CreateResult{TaskNo: taskNo, Status: "failed", Message: "导出失败，请稍后重试"}, nil
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

// generationFailure 表示原错误已伴随failed状态持久化，公开回执仍是原200／failed。
type generationFailure struct {
	cause error /* 原生成取消、IO或查询错误 */
}

// Error 返回e内部生成原因，仅用于错误链和日志，不作为公开回执。
func (e *generationFailure) Error() string { return e.cause.Error() }

// Unwrap 返回e原取消、IO或查询错误，支持errors.Is／As，不改变持久状态。
func (e *generationFailure) Unwrap() error { return e.cause }
