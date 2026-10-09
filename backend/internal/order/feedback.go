package order

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrFeedbackExists        = errors.New("order already has feedback")                  // 订单已评价，重复提交仍为冲突
	ErrFeedbackOrderNotReady = errors.New("order is not a completed order of this user") // 未完成、未负责或缺失统一不可评价
)

// FeedbackInputError 保留业务校验的公开文案，不包含用户或订单存在性。
type FeedbackInputError struct {
	Message string /* 评分、类型、内容或附件错误 */
}

// Error 返回 e 的公开校验错误文案。
func (e *FeedbackInputError) Error() string { return e.Message }

// FeedbackInput 是用户反馈字段，归属由事务中的订单提供。
type FeedbackInput struct {
	Rating   int8     `json:"rating"`   // 1–5 评分
	Category string   `json:"category"` // rating/complaint/suggestion
	Content  *string  `json:"content"`  // 可空，最多2000个字符
	Images   []string `json:"images"`   // 最多5个HTTPS附件，nil 原样保存 JSON null
}

// FeedbackReplyInput 是后台动作；close 忽略回复正文，reply 必须提供有效文本。
type FeedbackReplyInput struct {
	Action       string `json:"action"`        // reply 回复或 close 关闭
	ReplyContent string `json:"reply_content"` // reply 时1–2000字符
}

// FeedbackService 负责订单评价校验、回复状态与审计的完整事务；不依赖 HTTP 或设备领域实现。
type FeedbackService struct {
	DB *gorm.DB /* 所属订单与反馈连接，由进程管理 */
}

// ResolveReference 将订单主键或业务单号转换为订单 ID。
// 业务单号先核对用户；数字主键留待提交事务加锁后核对，两种输入都不能绕过归属校验。
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

// ValidateFeedback 校验评分、类别、内容长度与附件，失败返回明确文案，成功返回 nil。
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

// ValidFeedbackImage 核对 raw 是无首尾空白、认证信息的 HTTPS 域名链接，最多512字节，返回检查结果。
func ValidFeedbackImage(raw string) bool {
	if len(raw) == 0 || len(raw) > 512 || strings.TrimSpace(raw) != raw {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// Submit 为已完成且属于当前用户的订单保存反馈。
// 先锁订单再核对已有反馈，避免旧快照漏掉并发提交；重复反馈报错，失败全部回滚。
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

// List 按实时 feedback.read 权限读取反馈页；反馈属于全局管理功能，不按站点过滤。
func (s FeedbackService) List(ctx context.Context, actor operations.Profile, q persist.PageQuery) (persist.Page[map[string]any], error) {
	if !actor.HasPermission("feedback.read") {
		return persist.Page[map[string]any]{}, operations.ErrForbidden
	}
	rows, total, err := (FeedbackRepo{DB: s.DB}).List(ctx, q.Status, q.Page, q.PageSize)
	return persist.Page[map[string]any]{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize}, err
}

// Reply 按实时 feedback.reply 权限回复或关闭反馈，状态与审计同事务。
// 回复仅允许 pending→processed；关闭允许任意未关闭状态，重复关闭不重复审计。
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
