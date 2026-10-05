package central

import (
	"context"
	"net/http"
	"strconv"

	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// PaidStartsAPI 适配worker按原游标读取已付款待启动订单，筛选及数量边界由所属用例执行。
type PaidStartsAPI struct {
	// Service 是cmd注入的待启动查询窄能力，不暴露数据库或通用HTTP。
	Service interface {
		// PaidStarts 在请求context下读取after_id之后的原待启动订单，返回有界集合或存储错误，无写入。
		PaidStarts(context.Context, uint64) ([]chargestart.PaidChargeOrder, error)
	}
	ServiceToken string // 原内部服务令牌，仅HTTP准入使用，不进入业务用例。
}

// Register 在r注册原待启动订单GET路径，无返回值、数据库读取或后台任务。
func (a PaidStartsAPI) Register(r *gin.Engine) {
	r.GET(centralcontract.PaidStartsPath, a.list)
}

// list 从c校验服务令牌和after_id游标，在请求context下读取原待启动集合。
// 成功返回原整数ID／订单号及空数组；坏游标400、读取失败503，不派发设备命令。
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
