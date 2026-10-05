package http

import (
	"database/sql"
	"errors"
	"strings"

	chargingquery "github.com/ChargePilot2026/charge-pilot/internal/charging/query"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UserQueryAPI 仅适配用户充电查询的凭据、输入与结果，业务隔离及组合读取由所属用例执行。
type UserQueryAPI struct {
	Orders orderstore.QueryService       // 所属正在充电及批量历史查询
	Query  chargingquery.UserService     // 用户详情、状态及有界曲线用例
	Auth   authhttp.SessionAuthenticator // 当前用户会话准入
}

// Register 将 a 的六个原用户查询路径注册到 r，无存储或网络读取。
func (a UserQueryAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/charge/ongoing", a.ongoing)
	r.GET("/api/v1/user/charge/ongoing/snapshot", a.snapshot)
	r.GET("/api/v1/user/charge/ongoing/curve", a.curve)
	r.GET("/api/v1/user/charge/history", a.history)
	r.GET("/api/v1/user/charge/:order_no/curve", a.historyCurve)
	r.GET("/api/v1/user/charge/:order_no", a.detail)
}

// queryUser 核对 c 当前凭据，返回原用户主键或写出401，无业务写入。
func (a UserQueryAPI) queryUser(c *gin.Context) (uint64, bool) {
	id, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
	}
	return id, ok
}

// queryFailure 将 err 所属业务准入及阶段错误映射到 c 原公开响应，不读取或更改业务数据。
func queryFailure(c *gin.Context, err error) {
	var input *orderstore.QueryInputError
	if errors.As(err, &input) {
		httpapi.BadRequest(c, input.Message)
		return
	}
	var window *chargingquery.UserInputError
	if errors.As(err, &window) {
		switch window.Kind {
		case "conflict":
			httpapi.Write(c, 409, 2009, window.Message, nil)
		case "expired":
			httpapi.Write(c, 422, 2018, window.Message, nil)
		default:
			httpapi.BadRequest(c, window.Message)
		}
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "充电订单不存在", nil)
		return
	}
	message := "订单暂时无法读取"
	var read *chargingquery.UserReadError
	if errors.As(err, &read) {
		switch read.Stage {
		case "fee":
			message = "费用暂时无法读取"
		case "state":
			message = "订单方案及确认状态暂不可读取"
		case "payment":
			message = "支付记录暂时无法读取"
		case "refund":
			message = "退款记录暂时无法读取"
		case "curve":
			message = "充电曲线暂时无法读取，请稍后重试"
		case "history_curve":
			message = "历史充电曲线暂时无法读取"
		}
	}
	httpapi.Write(c, 503, 5003, message, nil)
}

// queryOrderNo 从 c 原兼容参数读取业务号，优先order_id；仅适配文本，不校验归属。
func queryOrderNo(c *gin.Context) string {
	no := strings.TrimSpace(c.Query("order_id"))
	if no == "" {
		no = strings.TrimSpace(c.Query("order_no"))
	}
	return no
}

// snapshot 将 c 原用户与单号传给快照用例，输出原读模型或映射错误，无业务写入。
func (a UserQueryAPI) snapshot(c *gin.Context) {
	id, ok := a.queryUser(c)
	if !ok {
		return
	}
	out, err := a.Query.Snapshot(c.Request.Context(), id, queryOrderNo(c))
	if err != nil {
		queryFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// detail 将 c 原用户与路径单号传给详情用例，输出原读模型或映射错误，无业务写入。
func (a UserQueryAPI) detail(c *gin.Context) {
	id, ok := a.queryUser(c)
	if !ok {
		return
	}
	out, err := a.Query.Detail(c.Request.Context(), id, c.Param("order_no"))
	if err != nil {
		queryFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// curve 将 c 原用户、单号与回看窗口传给曲线用例，输出原读模型或映射错误，无业务写入。
func (a UserQueryAPI) curve(c *gin.Context) {
	id, ok := a.queryUser(c)
	if !ok {
		return
	}
	out, err := a.Query.Curve(c.Request.Context(), id, queryOrderNo(c), c.Query("window"))
	if err != nil {
		queryFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// historyCurve 将 c 原用户、路径单号与粒度传给历史曲线用例，输出原读模型或映射错误，无业务写入。
func (a UserQueryAPI) historyCurve(c *gin.Context) {
	id, ok := a.queryUser(c)
	if !ok {
		return
	}
	out, err := a.Query.HistoricalCurve(c.Request.Context(), id, c.Param("order_no"), c.DefaultQuery("granularity", "15min"))
	if err != nil {
		queryFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// nullTime 将 v 原可空时刻转换为公开 UTC 或null，无IO。
func nullTime(v sql.NullTime) any {
	if !v.Valid {
		return nil
	}
	return v.Time.UTC()
}

// ongoing 适配原用户最新正在充电查询；无记录仍返回空状态，读取失败返回503。
func (a UserQueryAPI) ongoing(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	order, err := a.Orders.Ongoing(c.Request.Context(), userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.OK(c, gin.H{"order_no": "", "status": ""})
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"order_no": order.OrderNo, "status": order.Status, "device_id": order.DeviceID,
		"port_no": order.PortNo, "started_at": nullTime(order.StartedAt), "ended_at": nullTime(order.EndedAt),
	})
}

// history 按最新优先分页返回客户自己的订单，
// 归属规则与其他所有订单读取完全一致。
func (a UserQueryAPI) history(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	page, pageSize, paged := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !paged {
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !oneOfStatus(status, "pending_payment paid charging completed cancelled failed refunding refunded") {
		httpapi.BadRequest(c, "订单状态无效")
		return
	}
	out, err := a.Orders.History(c.Request.Context(), userID, persist.PageQuery{Page: page, PageSize: pageSize, Status: status})
	if err != nil {
		if orderstore.IsFeeQueryError(err) {
			httpapi.Write(c, 503, 5003, "费用暂时无法读取", nil)
		} else {
			httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		}
		return
	}
	httpapi.OK(c, gin.H{"items": out.Items, "total": out.Total, "page": page, "page_size": pageSize})
}
