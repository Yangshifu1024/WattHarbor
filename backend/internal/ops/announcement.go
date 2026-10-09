package ops

import (
	"chargepilot/internal/domain/operations"
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"time"
)

// AnnouncementInput 是公告发布参数，站点和城市目标保留字符串。
type AnnouncementInput struct {
	Title     string     `json:"title"`      // 必填标题，最多128字符
	Content   string     `json:"content"`    // 必填正文，最多20000字符
	Scope     string     `json:"scope"`      // global／station／city
	TargetIDs []string   `json:"target_ids"` // 非全局必填，最多100项，每项64字符
	StartAt   time.Time  `json:"start_at"`   // 生效时刻，必填
	EndAt     *time.Time `json:"end_at"`     // 可空失效时刻，非空必须晚于生效时刻
}

// validate 检查公告内容、目标范围和有效时间，不查询城市字典。
func (in AnnouncementInput) validate() error {
	contentValid := operations.ValidText(in.Title, 128) && operations.ValidText(in.Content, 20000)
	scopeValid := in.Scope == "global" || in.Scope == "station" || in.Scope == "city"
	timeValid := !in.StartAt.IsZero() && (in.EndAt == nil || in.EndAt.After(in.StartAt))
	targetValid := in.Scope == "global" || len(in.TargetIDs) > 0
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
			"status": "published", "created_by": actor.ID}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "create", Target: "announcement", ID: id, After: after})
	})
	return id, err
}
