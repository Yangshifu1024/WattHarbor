package http

import (
	"context"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// BillStore 与 Bill 是账单生成与查询的读写入口，定义在持久化层。
type BillStore = database.BillStore

type Bill = database.Bill

// BillHTTP 把账单视图暴露给客户。
type BillHTTP struct {
	Auth  SessionAuthenticator
	Bills BillStore
}

func (a BillHTTP) Register(r *gin.Engine) {
	r.GET("/api/v1/user/bills", a.list)
	r.GET("/api/v1/user/bills/:id", a.detail)
	r.POST("/api/v1/user/bills/:id/read", a.markRead)
}

func (a BillHTTP) list(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	page, pageSize, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	rows, total, err := a.Bills.ListBills(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		httpapi.Write(c, 503, 5003, "账单暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

func (a BillHTTP) detail(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "账单编号无效")
		return
	}
	// 查询同时限制账单 ID 和用户 ID，使越权访问与记录不存在返回一致错误。
	bill, err := a.Bills.LoadByID(c.Request.Context(), userID, id)
	if err != nil {
		httpapi.Write(c, 404, 1004, "账单不存在", nil)
		return
	}
	httpapi.OK(c, bill)
}

func (a BillHTTP) markRead(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "账单编号无效")
		return
	}
	if _, err := a.Bills.LoadByID(c.Request.Context(), userID, id); err != nil {
		httpapi.Write(c, 404, 1004, "账单不存在", nil)
		return
	}
	if err := a.Bills.MarkRead(c.Request.Context(), userID, id); err != nil {
		httpapi.Write(c, 503, 5003, "账单状态暂时无法保存", nil)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "read": true})
}

// BillStoreIssuer 把 BillStore 适配成计费包的接口，
// 同时不让计费反过来依赖本模块。
type BillStoreIssuer struct{ Store BillStore }

// Issue 为已结算的充电发布账单。
func (a BillStoreIssuer) Issue(ctx context.Context, chargeOrderID uint64) (uint64, error) {
	bill, created, err := a.Store.Issue(ctx, chargeOrderID)
	if err != nil || !created {
		return 0, err
	}
	return bill.ID, nil
}
