package central

import (
	"context"
	"net/http"
	"strconv"

	chargestart "chargepilot/internal/charging/start"
	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// PaidStartsAPI 为 worker 分页返回已支付的待启动订单。
type PaidStartsAPI struct {
	// Service 是 cmd 注入的待启动查询接口，不暴露数据库或通用 HTTP。
	Service interface {
		// PaidStarts 在请求 context 下读取 after_id 之后的待启动订单，返回有限条列表或存储错误，不写库。
		PaidStarts(context.Context, uint64) ([]chargestart.PaidChargeOrder, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 在 r 注册待启动订单 GET 路径。
func (a PaidStartsAPI) Register(r *gin.Engine) {
	r.GET(centralcontract.PaidStartsPath, a.list)
}

// list 从 c 校验服务令牌和 after_id 游标，在请求 context 下读取待启动集合。
// 成功返回整数 ID／订单号及空数组；坏游标 400、读取失败 503，不派发设备命令。
func (a PaidStartsAPI) list(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	after, err := strconv.ParseUint(c.DefaultQuery("after_id", "0"), 10, 64)
	if err != nil {
		httpapi.BadRequest(c, "invalid paid start cursor")
		return
	}
	items, err := a.Service.PaidStarts(c.Request.Context(), after)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "paid orders unavailable", nil)
		return
	}
	rows := make([]centralcontract.PaidStartOrder, len(items))
	for i, item := range items {
		rows[i] = centralcontract.PaidStartOrder{ID: item.ID, OrderNo: item.OrderNo}
	}
	httpapi.OK(c, centralcontract.PaidStartsReply{Items: rows})
}
