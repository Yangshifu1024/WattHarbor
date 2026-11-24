package order

import (
	"database/sql"
	"errors"
	"strings"

	chargingquery "chargepilot/internal/charging/query"
	orderdomain "chargepilot/internal/domain/order"
	authhttp "chargepilot/internal/http/auth"
	userhttp "chargepilot/internal/http/user"
	orderstore "chargepilot/internal/order"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// QueryAPI 提供当前用户的充电状态、历史订单和曲线查询。
type QueryAPI struct {
	Orders orderstore.QueryService       // 正在充电及批量历史查询
	Query  chargingquery.UserService     // 用户详情、状态及有界曲线用例
	Auth   authhttp.SessionAuthenticator // 当前用户会话验证
}

// Register 注册六个用户充电查询接口。
func (a QueryAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/charge/ongoing", a.ongoing)
	r.GET("/api/v1/user/charge/ongoing/snapshot", a.snapshot)
	r.GET("/api/v1/user/charge/ongoing/curve", a.curve)
	r.GET("/api/v1/user/charge/history", a.history)
	r.GET("/api/v1/user/charge/:order_no/curve", a.historyCurve)
	r.GET("/api/v1/user/charge/:order_no", a.detail)
}

// queryFailure 将订单状态和查询错误转为公开提示。
func queryFailure(c *gin.Context, err error) {
	var input *orderstore.QueryInputError
	if errors.As(err, &input) {
		httpapi.BadRequest(c, input.Message)
		return
	}
	var window *chargingquery.UserInputError
	if errors.As(err, &window) {
		if window.Kind == "conflict" {
			httpapi.Write(c, 409, 2009, window.Message, nil)
		} else {
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

// queryOrderNo 优先读取 order_id，其次读取订单号参数。
func queryOrderNo(c *gin.Context) string {
	no := strings.TrimSpace(c.Query("order_id"))
	if no == "" {
		no = strings.TrimSpace(c.Query("order_no"))
	}
	return no
}

// snapshot 返回当前用户订单的充电状态。
func (a QueryAPI) snapshot(c *gin.Context) {
	id, ok := a.Auth.Authenticate(c)
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

// detail 返回当前用户订单的详情。
func (a QueryAPI) detail(c *gin.Context) {
	id, ok := a.Auth.Authenticate(c)
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

// curve 返回当前用户订单指定回看时间内的曲线。
func (a QueryAPI) curve(c *gin.Context) {
	id, ok := a.Auth.Authenticate(c)
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

// historyCurve 按指定粒度返回当前用户订单的历史曲线。
func (a QueryAPI) historyCurve(c *gin.Context) {
	id, ok := a.Auth.Authenticate(c)
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

// nullTime 将有效时间转换为 UTC，无值时返回 nil。
func nullTime(v sql.NullTime) any {
	if !v.Valid {
		return nil
	}
	return v.Time.UTC()
}

// ongoing 返回当前用户最新的充电订单；没有记录时返回空状态。
func (a QueryAPI) ongoing(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
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

// history 按最新优先分页返回当前用户的订单，可按状态筛选。
func (a QueryAPI) history(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	page, pageSize, paged := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !paged {
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !userhttp.AllowedStatus(status, orderdomain.AllStatuses) {
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
