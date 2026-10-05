package order

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrFeedbackExists        = errors.New("order already has feedback")                  // 原订单已评价，重复提交仍为冲突
	ErrFeedbackOrderNotReady = errors.New("order is not a completed order of this user") // 未完成、未拥有或缺失统一不可评价
)

// FeedbackInputError 保留业务准入的原公开文案，不包含用户或订单存在性。
type FeedbackInputError struct {
	Message string /* 原评分、类型、内容或附件错误 */
}

// Error 返回 e 的原公开准入文案，无 IO。
func (e *FeedbackInputError) Error() string { return e.Message }

// FeedbackInput 是用户原反馈字段，归属由事务中的订单提供。
type FeedbackInput struct {
	Rating   int8     `json:"rating"`   // 1–5 原评分
	Category string   `json:"category"` // rating/complaint/suggestion
	Content  *string  `json:"content"`  // 可空，最多2000个字符
	Images   []string `json:"images"`   // 最多5个HTTPS附件，nil 原样保存 JSON null
}

// FeedbackReplyInput 是原后台动作；close 忽略回复正文，reply 必须提供原有效文本。
type FeedbackReplyInput struct {
	Action       string `json:"action"`        // reply 回复或 close 关闭
	ReplyContent string `json:"reply_content"` // reply 时1–2000字符
}

// FeedbackService 拥有订单评价准入、回复状态与审计的完整事务；不依赖 HTTP 或设备领域实现。
type FeedbackService struct {
	DB *gorm.DB /* 所属订单与反馈连接，由进程管理 */
}

// ResolveReference 在 ctx 下按 userID 解析 raw 原主键或业务单号，返回身份或输入／缺失／读取错误。
// 数字主键沿用原无预查语义；业务单号先核对原用户，真正提交仍在锁内再次核对。
func (s FeedbackService) ResolveReference(ctx context.Context, userID uint64, raw string) (uint64, error) {
	if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
		return id, nil
	}
	orderNo := strings.TrimSpace(raw)
	if orderNo == "" || len(orderNo) > 64 {
		return 0, &FeedbackInputError{"订单号无效"}
	}
	return (FeedbackRepo{DB: s.DB}).ResolveOwnedReference(ctx, userID, orderNo)
}

// ValidateFeedback 核对 in 原评分、类别、内容长度及附件，返回原准入文案或 nil，无 IO。
func ValidateFeedback(in FeedbackInput) error {
	if in.Rating < 1 || in.Rating > 5 || (in.Category != "rating" && in.Category != "complaint" && in.Category != "suggestion") {
		return &FeedbackInputError{"评分或反馈类型无效"}
	}
	if in.Content != nil && len([]rune(*in.Content)) > 2000 {
		return &FeedbackInputError{"反馈内容过长"}
	}
	if len(in.Images) > 5 {
		return &FeedbackInputError{"图片最多 5 张"}
	}
	for _, image := range in.Images {
		if !ValidFeedbackImage(image) {
			return &FeedbackInputError{"图片链接必须为 HTTPS"}
		}
	}
	return nil
}

// ValidFeedbackImage 核对 raw 是无首尾空白、认证信息的 HTTPS 域名链接，最多512字节，返回事实，无 IO。
func ValidFeedbackImage(raw string) bool {
	if len(raw) == 0 || len(raw) > 512 || strings.TrimSpace(raw) != raw {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// Submit 在 ctx 下为 userID 的 orderID 提交已绑定 in，返回原新反馈主键或准入／不可评价／重复／存储错误。
// 先锁订单、核对完成与归属，再当前读已有反馈并插入，整个事务失败回滚，不把重复提交视为成功。
func (s FeedbackService) Submit(ctx context.Context, userID, orderID uint64, in FeedbackInput) (uint64, error) {
	if err := ValidateFeedback(in); err != nil {
		return 0, err
	}
	images, err := json.Marshal(in.Images)
	if err != nil {
		return 0, err
	}
	var id uint64
	repo := FeedbackRepo{DB: s.DB}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		order, err := repo.LockOrder(tx, orderID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrFeedbackOrderNotReady
		}
		if err != nil {
			return err
		}
		if order.UserID != userID || (order.Status != "completed" && order.Status != "refunding" && order.Status != "refunded") {
			return ErrFeedbackOrderNotReady
		}
		exists, err := repo.HasFeedback(tx, orderID)
		if err != nil {
			return err
		}
		if exists {
			return ErrFeedbackExists
		}
		id, err = repo.Insert(tx, order, in, string(images))
		return err
	})
	return id, err
}

// List 在 ctx 下按 actor 原 feedback.read 权限读取 q 反馈页，返回原页或禁止／读取错误，不修改工单。
// 反馈无站点范围列，沿用原全局权限，不按报修或订单页面位置引入额外过滤。
func (s FeedbackService) List(ctx context.Context, actor operations.Profile, q persist.PageQuery) (persist.Page[map[string]any], error) {
	if !actor.HasPermission("feedback.read") {
		return persist.Page[map[string]any]{}, operations.ErrForbidden
	}
	rows, total, err := (FeedbackRepo{DB: s.DB}).List(ctx, q.Status, q.Page, q.PageSize)
	return persist.Page[map[string]any]{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize}, err
}

// Reply 在 ctx 下按 audit 原 feedback.reply 权限处理 id 的 in 动作，返回禁止／输入／缺失／状态冲突／存储错误。
// reply 仅允许 pending→processed；close 可从未关闭状态关闭，已关闭重放无重复审计；状态与审计同事务。
func (s FeedbackService) Reply(ctx context.Context, audit ops.AuditContext, id uint64, in FeedbackReplyInput) error {
	if !audit.Actor.HasPermission("feedback.reply") {
		return operations.ErrForbidden
	}
	if in.Action != "close" && (in.Action != "reply" || !operations.ValidText(in.ReplyContent, 2000)) {
		return &FeedbackInputError{"请填写 1–2000 字回复或关闭反馈"}
	}
	repo := FeedbackRepo{DB: s.DB}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		status, err := repo.LockStatus(tx, id)
		if err != nil {
			return err
		}
		if in.Action == "close" && status == "closed" {
			return nil
		}
		if status == "closed" || (in.Action == "reply" && status != "pending") {
			return operations.ErrConflict
		}
		values := map[string]any{"status": "closed"}
		if in.Action == "reply" {
			values = map[string]any{"status": "processed", "reply_content": in.ReplyContent, "replied_by": audit.Actor.ID, "replied_at": time.Now().UTC()}
		}
		if err := repo.Update(tx, id, values); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: in.Action, Target: "feedback", ID: id, Before: map[string]any{"status": status}, After: values})
	})
}
