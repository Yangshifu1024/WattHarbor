package admin

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// ReadAdminPage 解析并校验通用分页参数：page ≥ 1、page_size 1–100（page 上限 1000000），
// 关键词不超过 128 字符，状态必须在该接口传入的白名单内（留空表示不按状态过滤）。
// 任一项不合法直接写 400 并返回 false，调用方必须立即 return。
func ReadAdminPage(c *gin.Context, statuses string) (persist.PageQuery, bool) {
	// 保留后台分页上限 1000000，与 C 端分页上限分别配置。
	page, pageSize, paged := httpapi.ReadPaging(c, 20, httpapi.MaxAdminPageLimit)
	if !paged {
		return persist.PageQuery{}, false
	}
	q := persist.PageQuery{Page: page, PageSize: pageSize, Keyword: strings.TrimSpace(c.Query("keyword")), Status: c.Query("status")}
	if utf8.RuneCountInString(q.Keyword) > 128 || !AdminOneOf(q.Status, statuses) {
		httpapi.BadRequest(c, "关键词过长或状态无效")
		return q, false
	}
	return q, true
}

// AdminOneOf 返回 value 是否属于空格分隔的 allowed 白名单；空 value 表示不筛选并返回 true。
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
// c 的路由参数须命名为 id；成功返回非零 ID 和 true，失败返回 0 和 false。
func AdminPathID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "ID 必须为正整数")
		return 0, false
	}
	return id, true
}

// ErrConflict 表示记录当前状态拒绝写入，例如期望版本已发生变化，
// 映射为 409 并提示前端刷新后重试。
var ErrConflict = operations.ErrConflict

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

// AdminAuditContext 从请求提取已实时授权的操作人与请求审计信息，不开启事务。
func AdminAuditContext(c *gin.Context) ops.AuditContext {
	return ops.AuditContext{Actor: c.MustGet("admin_profile").(operations.Profile), ClientIP: c.ClientIP(), RequestID: httpapi.RequestID(c)}
}
