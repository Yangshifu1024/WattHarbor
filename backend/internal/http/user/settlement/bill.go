package settlement

import (
	"errors"
	"strconv"

	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// BillAPI 适配用户的账单列表、详情及已读记录；确认金额来自结算账单。
type BillAPI struct {
	Auth  authhttp.SessionAuthenticator // 用户凭据、会话及当前状态；失败已写错误响应。
	Bills settlement.UserBillService    // 账单查询和完整已读操作，连接生命周期由 cmd 管理。
}

// Register 在 r 挂载三个账单端点，不打开资源或执行业务 IO。
func (a BillAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/bills", a.list)
	r.GET("/api/v1/user/bills/:id", a.detail)
	r.POST("/api/v1/user/bills/:id/read", a.markRead)
}

// list 在 c 下认证并读取本用户账单分页；认证失败立即返回，列表失败保持 503。
func (a BillAPI) list(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	page, pageSize, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	rows, total, err := a.Bills.List(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		httpapi.Write(c, 503, 5003, "账单暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

// detail 从 c 验证路径 ID 并按用户归属读取账单；认证失败只保留错误，查询错误沿用 404。
func (a BillAPI) detail(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "账单编号无效")
		return
	}
	// 查询同时限制账单 ID 和用户 ID，使越权访问与记录不存在返回一致错误。
	bill, err := a.Bills.Detail(c.Request.Context(), userID, id)
	if err != nil {
		httpapi.Write(c, 404, 1004, "账单不存在", nil)
		return
	}
	httpapi.OK(c, bill)
}

// markRead 在 c 下先按本用户读取账单，再保存已读凭据，沿用查不到 404 及保存失败 503。
// 认证失败不读取或写入账单，鉴权适配负责唯一错误响应。
func (a BillAPI) markRead(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "账单编号无效")
		return
	}
	if err := a.Bills.MarkRead(c.Request.Context(), userID, id); err != nil {
		if errors.Is(err, settlement.ErrUserBillLookup) {
			httpapi.Write(c, 404, 1004, "账单不存在", nil)
		} else {
			httpapi.Write(c, 503, 5003, "账单状态暂时无法保存", nil)
		}
		return
	}
	httpapi.OK(c, gin.H{"id": strconv.FormatUint(id, 10), "read": true})
}
