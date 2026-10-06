package device

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
	"time"
)

// FaultDispatch 是原人员和可空备注，不接受报告状态或用户身份覆盖。
type FaultDispatch struct {
	AssignedTo uint64  `json:"assigned_to"` // 原处理人管理员主键，非0
	Note       *string `json:"note"`        // 原可空备注，最多2000字
}

// FaultResolution 是处理人申请的原状态及修复说明。
type FaultResolution struct {
	Status string  `json:"status"` // 原fixed或closed，不允许跳级
	Note   *string `json:"note"`   // 原可空备注；fixed必须有非空说明，最多2000字
}

// List 在 ctx 下核对 actor 原fault.read权限并加载当前范围，按 q 返回原报告页或禁止／输入／存储错误。
// 范围内列表与历史／修改一致，不访问网关或改报告。
func (s FaultService) List(ctx context.Context, actor operations.Profile, q persist.PageQuery) (persist.Page[map[string]any], error) {
	out := persist.Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
	if err := requireFaultPermission(actor, "fault.read"); err != nil {
		return out, err
	}
	if !validFaultPage(q) || (q.Status != "" && q.Status != FaultOpen && q.Status != FaultDispatched && q.Status != FaultFixed && q.Status != FaultClosed) {
		return out, &FaultInputError{"分页或报修状态无效"}
	}
	scope, err := ops.LoadDataScope(ctx, s.DB, actor)
	if err != nil {
		return out, err
	}
	out.Items, out.Total, err = (FaultRepo{DB: s.DB}).ListReports(ctx, scope, q.Status, q.Page, q.PageSize)
	return out, err
}

// History 在 ctx 下按 actor 原fault.read及当前范围核对 id，再按 q 返回原全可见性历史页。
// 范围外与不存在同为缺失，SQL故障原样返回，不能查询其他工单的历史。
func (s FaultService) History(ctx context.Context, actor operations.Profile, id uint64, q persist.PageQuery) (persist.Page[map[string]any], error) {
	out := persist.Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
	if err := requireFaultPermission(actor, "fault.read"); err != nil {
		return out, err
	}
	if id == 0 || !validFaultPage(q) {
		return out, &FaultInputError{"分页或报修编号无效"}
	}
	scope, err := ops.LoadDataScope(ctx, s.DB, actor)
	if err != nil {
		return out, err
	}
	repo := FaultRepo{DB: s.DB}
	exists, err := repo.ScopedFaultExists(ctx, scope, id)
	if err != nil {
		return out, err
	}
	if !exists {
		return out, gorm.ErrRecordNotFound
	}
	out.Items, out.Total, err = repo.ListFaultEvents(ctx, id, q.Page, q.PageSize)
	return out, err
}

// checkFaultScope 在 ctx、tx 下核对 scope 对 row 原设备身份的可见性；不受限兼容无元数据报告。
// 返回范围外／缺失或存储错误，不以缺失元数据补造设备，不取得反向设备写锁。
func checkFaultScope(ctx context.Context, tx *gorm.DB, scope operations.DataScope, row FaultRow) error {
	if scope.Unrestricted {
		return nil
	}
	_, err := FindDeviceByID(ctx, tx, scope, row.DeviceID)
	return err
}

// Dispatch 在 ctx 下以 audit 原fault.dispatch和当前范围将 id 指派给 in 原处理人，返回原输入／冲突／存储错误。
// READ COMMITTED事务锁工单后读取处理人的实时权限及范围；状态、用户可见事件与审计一起提交，同人重放不新增事件。
func (s FaultService) Dispatch(ctx context.Context, audit ops.AuditContext, id uint64, in FaultDispatch) error {
	if err := requireFaultPermission(audit.Actor, "fault.dispatch"); err != nil {
		return err
	}
	if id == 0 || in.AssignedTo == 0 || (in.Note != nil && len([]rune(*in.Note)) > MaxFaultNoteLength) {
		return &FaultInputError{"请指定有效人员，备注最多 2000 字"}
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := ops.LoadDataScope(ctx, tx, audit.Actor)
		if err != nil {
			return err
		}
		repo := FaultRepo{DB: tx}
		row, err := repo.LockFault(tx, id)
		if err != nil {
			return err
		}
		if err := checkFaultScope(ctx, tx, scope, row); err != nil {
			return err
		}
		assignee, err := (ops.AccountRepo{DB: tx}).Profile(ctx, in.AssignedTo)
		if errors.Is(err, operations.ErrCredentials) {
			return &FaultInputError{"指派账号不存在、未启用或无故障处理权限"}
		}
		if err != nil {
			return err
		}
		if !assignee.HasPermission("fault.resolve") {
			return &FaultInputError{"指派账号不存在、未启用或无故障处理权限"}
		}
		assigneeScope, err := ops.LoadDataScope(ctx, tx, assignee)
		if err != nil {
			return err
		}
		if err := checkFaultScope(ctx, tx, assigneeScope, row); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return &FaultInputError{"指派账号不在该设备数据范围内"}
			}
			return err
		}
		if row.Status != FaultOpen && row.Status != FaultDispatched {
			return operations.ErrConflict
		}
		if row.AssignedTo != nil && *row.AssignedTo == in.AssignedTo {
			return nil
		}
		event := "dispatched"
		if row.AssignedTo != nil {
			event = "reassigned"
		}
		if err := repo.UpdateReport(tx, id, map[string]any{"status": FaultDispatched, "assigned_to": in.AssignedTo}); err != nil {
			return err
		}
		if err := repo.AppendFaultEvent(tx, FaultEventInput{ReportID: id, ActorID: audit.Actor.ID, EventType: event, FromStatus: row.Status, ToStatus: FaultDispatched, AssignedTo: in.AssignedTo, Note: in.Note, UserVisible: true}); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: event, Target: "device_fault_report", ID: id, Before: row, After: in})
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// Resolve 在 ctx 下以 audit 原fault.resolve及当前范围推进 id 的 in 原状态，返回输入／冲突／缺失／存储错误。
// 仅原处理人可操作；同状态重放不改时间或新增事件，状态、可见事件与审计同事务回滚，不回退或跳级。
func (s FaultService) Resolve(ctx context.Context, audit ops.AuditContext, id uint64, in FaultResolution) error {
	if err := requireFaultPermission(audit.Actor, "fault.resolve"); err != nil {
		return err
	}
	if id == 0 || (in.Status != FaultFixed && in.Status != FaultClosed) || (in.Note != nil && len([]rune(*in.Note)) > MaxFaultNoteLength) || (FaultNoteRequired(in.Status) && (in.Note == nil || !operations.ValidText(*in.Note, MaxFaultNoteLength))) {
		return &FaultInputError{"状态无效或缺少修复说明"}
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := ops.LoadDataScope(ctx, tx, audit.Actor)
		if err != nil {
			return err
		}
		repo := FaultRepo{DB: tx}
		row, err := repo.LockFault(tx, id)
		if err != nil {
			return err
		}
		if err := checkFaultScope(ctx, tx, scope, row); err != nil {
			return err
		}
		if row.AssignedTo == nil || *row.AssignedTo != audit.Actor.ID {
			return operations.ErrConflict
		}
		if err := CanTransitionFault(row.Status, in.Status); err != nil {
			return operations.ErrConflict
		}
		if row.Status == in.Status {
			return nil
		}
		if err := repo.UpdateReport(tx, id, map[string]any{"status": in.Status, "resolved_at": time.Now().UTC()}); err != nil {
			return err
		}
		if err := repo.AppendFaultEvent(tx, FaultEventInput{ReportID: id, ActorID: audit.Actor.ID, EventType: in.Status, FromStatus: row.Status, ToStatus: in.Status, AssignedTo: audit.Actor.ID, Note: in.Note, UserVisible: true}); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: in.Status, Target: "device_fault_report", ID: id, Before: row, After: in})
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
