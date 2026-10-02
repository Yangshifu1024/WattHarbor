package http

import (
	"encoding/json"
	"errors"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// 后台列表接口统一的分页与筛选入参，本层以别名对外暴露。
type AdminPageQuery = database.PageQuery

// AdminPage 是后台列表接口的统一响应体，本层以别名对外暴露。
type AdminPage[T any] = database.Page[T]

// ReadAdminPage 解析并校验通用分页参数：page ≥ 1、page_size 1–100（page 上限 1000000），
// 关键词不超过 128 字符，状态必须在该接口传入的白名单内（留空表示不按状态过滤）。
// 任一项不合法直接写 400 并返回 false，调用方必须立即 return。
func ReadAdminPage(c *gin.Context, statuses string) (AdminPageQuery, bool) {
	// 分页参数走统一解析：后台的 page 上限比 C 端高一个数量级，
	// 导出与对账需要翻到很深的页，这是有意保留的差异。
	page, pageSize, paged := httpapi.ReadPaging(c, 20, httpapi.MaxAdminPageLimit)
	if !paged {
		return AdminPageQuery{}, false
	}
	q := AdminPageQuery{Page: page, PageSize: pageSize, Keyword: strings.TrimSpace(c.Query("keyword")), Status: c.Query("status")}
	if utf8.RuneCountInString(q.Keyword) > 128 || !AdminOneOf(q.Status, statuses) {
		httpapi.BadRequest(c, "关键词过长或状态无效")
		return q, false
	}
	return q, true
}

// AdminOneOf 判断值是否落在以空格分隔的白名单里；空值一律放行，表示不按这一项过滤。
func AdminOneOf(value, allowed string) bool {
	if value == "" {
		return true
	}
	for _, v := range strings.Fields(allowed) {
		if value == v {
			return true
		}
	}
	return false
}

// AdminPathID 读取路径参数 :id 并要求它是正整数，不合法直接写 400 并返回 false。
// 调用方所有 :id 路由的形参都必须命名为 id，否则这里取不到值。
func AdminPathID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "ID 必须为正整数")
		return 0, false
	}
	return id, true
}

// ErrConflict 表示这条记录当前的状态不接受这次写入（例如期望版本号已被别人改过），
// 映射为 409 并提示前端刷新后重试。
var ErrConflict = errors.New("当前记录状态不允许此操作，请刷新后重试")

// AdminResourceFailure 把存储层错误翻译成统一响应：业务冲突 409、记录不存在 404、
// 唯一索引冲突 409，其余一律 503 通用提示，不把底层错误暴露给前端。
func AdminResourceFailure(c *gin.Context, err error) {
	var duplicate *mysql.MySQLError
	switch {
	case errors.Is(err, ErrConflict):
		httpapi.Write(c, 409, 2009, err.Error(), nil)
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.Write(c, 404, 1004, "记录不存在或已删除", nil)
	case errors.As(err, &duplicate) && duplicate.Number == 1062:
		httpapi.Write(c, 409, 2009, "编码已存在，请使用其他编码", nil)
	default:
		httpapi.Write(c, 503, 5003, "数据暂时无法读取或保存，请稍后重试", nil)
	}
}

// AdminResourceAudit 在业务事务内保存变更快照、操作人、对象、客户端 IP 和请求号。
// 快照只记录接口层的入参与返回值，不含口令散列、令牌或密钥。
func AdminResourceAudit(tx *gorm.DB, p operations.Profile, action, target string, id uint64, before, after any, ip, requestID string) error {
	old, err := json.Marshal(before)
	if err != nil {
		return err
	}
	next, err := json.Marshal(after)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if len(requestID) > 64 {
		requestID = requestID[:64]
	}
	return orderstore.InsertAudit(tx, orderstore.AuditEntryInput{
		ActorID: p.ID, ActorName: p.Username, Module: target, Action: action,
		TargetType: target, TargetID: strconv.FormatUint(id, 10),
		BeforeJSON: old, AfterJSON: next, ClientIP: ip, RequestID: requestID,
	}, now)
}

// FirstNonEmpty 依次取参数中第一个非空字符串，全空则返回空串。
// 用来让业务自己带的请求号优先于框架生成的。
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// AuditEntry 是一条待写入的审计记录，与它的业务变更在同一事务提交。
type AuditEntry struct {
	Action    string // 动作，如 create / update / grant
	Target    string // 目标类型，同时也是所属模块，如 coupon
	ID        uint64 // 目标主键
	Before    any    // 变更前快照，新建时为 nil
	After     any    // 变更后快照
	RequestID string // 关联请求号，可为空
}

// AuditedTransaction 在同一事务内提交业务变更与审计：审计写不进去，整个事务回滚。
// entries 由调用方先填好；apply 拿到事务句柄执行实际写入。
func AuditedTransaction(c *gin.Context, db *gorm.DB, entries *[]AuditEntry, apply func(*gorm.DB) error) error {
	return db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := apply(tx); err != nil {
			return err
		}
		p := c.MustGet("admin_profile").(operations.Profile)
		for _, entry := range *entries {
			if err := AdminResourceAudit(tx, p, entry.Action, entry.Target, entry.ID, entry.Before, entry.After, c.ClientIP(), FirstNonEmpty(entry.RequestID, httpapi.RequestID(c))); err != nil {
				return err
			}
		}
		return nil
	})
}

// AuditToAdmin 记录由远端服务提交、响应后才落库的运营审计。
// 这类变更发生在别的服务上，无法与业务变更同事务，因此只记日志不阻断。
func AuditToAdmin(c *gin.Context, db *gorm.DB, action, target string, id uint64, before, after any, requestID string) {
	p := c.MustGet("admin_profile").(operations.Profile)
	if err := AdminResourceAudit(db.WithContext(c.Request.Context()), p, action, target, id, before, after, c.ClientIP(), FirstNonEmpty(requestID, httpapi.RequestID(c))); err != nil {
		log.Printf("gateway audit failed action=%s target=%s id=%d: %v", action, target, id, err)
	}
}

// AdminLikePattern 把关键词转成 LIKE 模式，转义规则与仓储层共用同一实现。
func AdminLikePattern(value string) string { return dbutil.LikePattern(value) }
