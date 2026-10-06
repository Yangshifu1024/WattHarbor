package ops

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
	"time"
)

// AnnouncementInput 保留原发布输入；目标是站点／城市字符串，不从名称推断数值身份。
type AnnouncementInput struct {
	Title     string     `json:"title"`      // 必填标题，最多128字符
	Content   string     `json:"content"`    // 必填正文，最多20000字符
	Scope     string     `json:"scope"`      // global／station／city
	TargetIDs []string   `json:"target_ids"` // 非全局必填，最多100项，每项64字符
	StartAt   time.Time  `json:"start_at"`   // 原生效时刻，必填
	EndAt     *time.Time `json:"end_at"`     // 原可空失效时刻，非空必须晚于生效时刻
}

// validate 核对in原内容、范围与时间，返回原输入文案；无IO，不核对不存在的城市字典。
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

// AnnouncementService 拥有公告发布准入及同事务审计，列表保留全局announcement.read权限。
type AnnouncementService struct {
	DB *gorm.DB /* 所属公告与后台账号数据库 */
}

// List 在ctx下核对actor当前读取权限，返回原公告清单或准入／取消／存储错误，不写入。
func (s AnnouncementService) List(ctx context.Context, actor operations.Profile) ([]AnnouncementRow, error) {
	if _, err := readManagementActor(ctx, s.DB, actor, "announcement.read"); err != nil {
		return nil, err
	}
	return (AnnouncementRepo{DB: s.DB}).List(ctx)
}

// Create 在ctx下核对audit当前发布权限及in，返回原ID或输入／准入／存储错误。
// 原公告立即published，目标JSON与created_by由所属repo写入；发布和审计同事务，不创建异步任务。
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
		// 保留原审计中目标JSON文本、发布状态及创建人，数据库列写入仍只在所属repo。
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
