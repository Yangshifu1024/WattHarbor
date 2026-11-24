package ops

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"time"
	"wattharbor/internal/domain/operations"
)

// AnnouncementStatus 是 announcement.status 列的公告发布状态，用于后台列表与写入。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
type AnnouncementStatus string

const (
	// AnnouncementDraft 表示草稿，尚未对外发布。
	AnnouncementDraft AnnouncementStatus = "draft"
	// AnnouncementPublished 表示已发布，是列表查询与审计写入使用的状态。
	AnnouncementPublished AnnouncementStatus = "published"
	// AnnouncementExpired 表示已过期。当前 Go 代码没有写入 expired 的路径，
	// 保留该取值以完整覆盖列约束，不得据此虚构到期流转。
	AnnouncementExpired AnnouncementStatus = "expired"
)

// Known 判断是否属于该列的数据库合法集合（三值）。
func (s AnnouncementStatus) Known() bool {
	switch s {
	case AnnouncementDraft, AnnouncementPublished, AnnouncementExpired:
		return true
	default:
		return false
	}
}

// AllAnnouncementStatuses 是 AnnouncementStatus 的全量取值串，供读取筛选白名单直接引用。
// 顺序与上方常量声明一致；新增取值不会自动进入该白名单。
const AllAnnouncementStatuses = "draft published expired"

// AnnouncementScope 是 announcement.scope 列的公告可见范围。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（global／station／city）。
// 与 operations.ScopeType 分立：后者是 admin_data_scope.scope_type 的账号数据范围维度，
// 词表与语义都不同，不得合并。
type AnnouncementScope string

const (
	// AnnouncementScopeGlobal 表示对全部用户可见，不需要目标列表。
	AnnouncementScopeGlobal AnnouncementScope = "global"
	// AnnouncementScopeStation 表示按充电站点投放，target_ids 为站点 ID。
	AnnouncementScopeStation AnnouncementScope = "station"
	// AnnouncementScopeCity 表示按城市投放，target_ids 为城市标识。
	AnnouncementScopeCity AnnouncementScope = "city"
)

// Known 判断是否属于该列的数据库合法集合（三值）。
func (s AnnouncementScope) Known() bool {
	switch s {
	case AnnouncementScopeGlobal, AnnouncementScopeStation, AnnouncementScopeCity:
		return true
	default:
		return false
	}
}

// AllAnnouncementScopes 是 AnnouncementScope 的全量取值串，供写入准入白名单直接引用。
const AllAnnouncementScopes = "global station city"

// AnnouncementInput 是公告发布参数，站点和城市目标保留字符串。
type AnnouncementInput struct {
	Title     string            `json:"title"`      // 必填标题，最多128字符
	Content   string            `json:"content"`    // 必填正文，最多20000字符
	Scope     AnnouncementScope `json:"scope"`      // global／station／city
	TargetIDs []string          `json:"target_ids"` // 非全局必填，最多100项，每项64字符
	StartAt   time.Time         `json:"start_at"`   // 生效时刻，必填
	EndAt     *time.Time        `json:"end_at"`     // 可空失效时刻，非空必须晚于生效时刻
}

// validate 检查公告内容、目标范围和有效时间，不查询城市字典。
// scopeValid 同时承担写入准入：空串与未知取值都不是该列的合法值。
func (in AnnouncementInput) validate() error {
	contentValid := operations.ValidText(in.Title, 128) && operations.ValidText(in.Content, 20000)
	scopeValid := in.Scope.Known()
	timeValid := !in.StartAt.IsZero() && (in.EndAt == nil || in.EndAt.After(in.StartAt))
	targetValid := in.Scope == AnnouncementScopeGlobal || len(in.TargetIDs) > 0
	if !contentValid || !scopeValid || !timeValid || !targetValid {
		return &ManagementInputError{Message: "请填写标题、内容、有效时间和公告目标"}
	}
	if len(in.TargetIDs) > 100 {
		return &ManagementInputError{Message: "公告目标最多 100 项"}
	}
	for _, target := range in.TargetIDs {
		if !operations.ValidText(target, 64) {
			return &ManagementInputError{Message: "公告目标无效"}
		}
	}
	return nil
}

// AnnouncementService 负责公告发布校验和同事务审计，列表使用全局 announcement.read 权限。
type AnnouncementService struct {
	DB *gorm.DB /* 所属公告与后台账号数据库 */
}

// List 核对 announcement.read 权限后读取公告。
func (s AnnouncementService) List(ctx context.Context, actor operations.Profile) ([]AnnouncementRow, error) {
	if _, err := readManagementActor(ctx, s.DB, actor, "announcement.read"); err != nil {
		return nil, err
	}
	return (AnnouncementRepo{DB: s.DB}).List(ctx)
}

// Create 核对发布权限和内容后立即保存 published 公告，与审计同事务提交。
func (s AnnouncementService) Create(ctx context.Context, audit AuditContext, in AnnouncementInput) (uint64, error) {
	if err := in.validate(); err != nil {
		return 0, err
	}
	if s.DB == nil {
		return 0, errors.New("ops announcement unavailable")
	}
	var id uint64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "announcement.create")
		if err != nil {
			return err
		}
		audit.Actor = actor
		id, err = (AnnouncementRepo{DB: tx}).Insert(tx, in, actor.ID)
		if err != nil {
			return err
		}
		// 审计保存目标 JSON、发布状态和创建人，数据库字段由仓储写入。
		targets, err := json.Marshal(in.TargetIDs)
		if err != nil {
			return err
		}
		after := map[string]any{"title": in.Title, "content": in.Content, "scope": in.Scope,
			"target_ids": string(targets), "start_at": in.StartAt, "end_at": in.EndAt,
			"status": AnnouncementPublished, "created_by": actor.ID}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "create", Target: "announcement", ID: id, After: after})
	})
	return id, err
}
