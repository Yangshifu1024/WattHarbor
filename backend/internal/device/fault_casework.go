package device

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"context"
	"database/sql"
	"errors"
	"gorm.io/gorm"
	"time"
)

// FaultDispatch 保存指派人和可空备注，不允许覆盖报告状态或用户。
type FaultDispatch struct {
	AssignedTo uint64  `json:"assigned_to"` // 处理人管理员主键，非0
	Note       *string `json:"note"`        // 可空备注，最多2000字
}

// FaultResolution 保存处理人提交的目标状态和修复说明。
type FaultResolution struct {
	Status string  `json:"status"` // fixed 或 closed，不允许跳级
	Note   *string `json:"note"`   // 可空备注；fixed 必须有非空说明，最多2000字
}

// List 核对 fault.read 权限和当前范围后分页读取报告。
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

// History 按 fault.read 权限和当前范围核对报告，再分页读取全部历史。
// 范围外和不存在都按缺失处理。
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

// checkFaultScope 核对报告设备是否在当前范围内；不受限账号可查看没有设备资料的历史报告。
// 不补建设备，也不反向取得设备写锁。
func checkFaultScope(ctx context.Context, tx *gorm.DB, scope operations.DataScope, row FaultRow) error {
	if scope.Unrestricted {
		return nil
	}
	_, err := FindDeviceByID(ctx, tx, scope, row.DeviceID)
	return err
}

// Dispatch 核对 fault.dispatch 权限和范围后，在 READ COMMITTED 事务中锁定工单。
// 检查处理人的当前权限和范围，指派、用户可见事件和审计同事务；同人重放不新增事件。
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

// Resolve 核对 fault.resolve 权限和范围，仅允许当前处理人顺序更新状态。
// 状态、可见事件和审计同事务；同状态重放不改时间或新增事件。
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
