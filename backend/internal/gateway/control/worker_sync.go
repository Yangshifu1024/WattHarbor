package control

import (
	gatewaycontract "chargepilot/internal/contracts/gateway"
	gatewayrepo "chargepilot/internal/gateway/repo"
	"chargepilot/internal/platform/httpapi"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"time"
)

// registerWorkerSync 注册 worker 所需的内部同步接口，要求服务令牌。
// 网关负责启动结果、结束事件、端口释放、计量读数和刷卡事件的读写。
func (a TelemetryAPI) registerWorkerSync(r *gin.Engine) {
	r.GET(gatewaycontract.StartResultsPath, a.startResults)
	r.POST(gatewaycontract.MarkStartResultsPath, a.markStartResultsReported)
	r.GET(gatewaycontract.EndEventsPath, a.endEvents)
	r.GET(gatewaycontract.ChargeCommandRoute, a.chargeCommandByOrder)
	r.POST(gatewaycontract.MarkDeviceEventsPath, a.markDeviceEventsProcessed)
	r.POST(gatewaycontract.ReleasePortPath, a.releasePort)
	r.GET(gatewaycontract.MeterSamplesRoute, a.meterSamples)
	r.POST(gatewaycontract.FreezeEndDeliveryPath, a.freezeEndDelivery)
	r.GET(gatewaycontract.CardEventsPath, a.cardEvents)
	r.POST("/api/v1/internal/card-events/decide-begin", a.cardDecideBegin)
	r.POST(gatewaycontract.CardFreezePath, a.cardDecideFinish)
	r.POST(gatewaycontract.CardAdvancePath, a.cardEventAdvance)
	r.GET(gatewaycontract.CardPortPath, a.resolvePort)
}

// startResults 鉴权后查询已回执但未上报的启动命令，可按 command_id 筛选；按更新时间升序最多 50 条。
func (a TelemetryAPI) startResults(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	filter := c.Query("command_id")
	if len(filter) > 64 {
		httpapi.BadRequest(c, "command_id 无效")
		return
	}
	rows, err := gatewayrepo.ListStartResults(c.Request.Context(), a.DB, filter)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "启动回执暂不可读取", nil)
		return
	}
	items := make([]gatewaycontract.SyncCommand, 0, len(rows))
	for _, row := range rows {
		items = append(items, syncCommandDTO(row))
	}
	httpapi.OK(c, gatewaycontract.StartResultsReply{Items: items})
}

// markStartResultsReported 在同一事务内标记输入命令已上报，返回实际影响行数。
// 重复标记不重复计数；任一写入失败整批回滚。
func (a TelemetryAPI) markStartResultsReported(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in gatewaycontract.MarkStartResultsRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "上报标记请求无效")
		return
	}
	marked, err := gatewayrepo.MarkStartResultsReported(c.Request.Context(), a.DB, in.CommandIDs)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "上报标记暂不可用", nil)
		return
	}
	httpapi.OK(c, gatewaycontract.MarkReply{Marked: marked})
}

// endEvents 鉴权后按 ID 升序返回最多 50 条未处理的 ConsumerType 2/3 结束事件。
func (a TelemetryAPI) endEvents(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	rows, err := gatewayrepo.ListUnprocessedEndEvents(c.Request.Context(), a.DB)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束事件暂不可读取", nil)
		return
	}
	items := make([]gatewaycontract.SyncEvent, 0, len(rows))
	for _, row := range rows {
		items = append(items, gatewaycontract.SyncEvent{ID: row.ID, Payload: string(row.EventJSON)})
	}
	httpapi.OK(c, gatewaycontract.EventsReply{Items: items})
}

// chargeCommandByOrder 鉴权后查询订单命令，不指定排序；无记录返回 found:false。
func (a TelemetryAPI) chargeCommandByOrder(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	orderID, err := strconv.ParseUint(c.Param("order_id"), 10, 64)
	if err != nil || orderID == 0 {
		httpapi.BadRequest(c, "订单 ID 无效")
		return
	}
	row, found, err := gatewayrepo.FindChargeCommandByOrder(c.Request.Context(), a.DB, orderID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "启动命令暂不可读取", nil)
		return
	}
	if !found {
		httpapi.OK(c, gatewaycontract.CommandReply{Found: false})
		return
	}
	command := syncCommandDTO(row)
	httpapi.OK(c, gatewaycontract.CommandReply{Found: true, Command: &command})
}

// markDeviceEventsProcessed 在同一事务内标记输入事件已处理，返回实际影响行数。
// 重复标记不重复计数；任一写入失败整批回滚。
func (a TelemetryAPI) markDeviceEventsProcessed(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in gatewaycontract.MarkDeviceEventsRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "处理标记请求无效")
		return
	}
	marked, err := gatewayrepo.MarkDeviceEventsProcessed(c.Request.Context(), a.DB, in.IDs)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "处理标记暂不可用", nil)
		return
	}
	httpapi.OK(c, gatewaycontract.MarkReply{Marked: marked})
}

// releasePort 只释放该订单占用且处于 charging 的端口。
// 更新成功返回 released；已是 idle/NULL 返回 already；其他占用状态返回 409。
func (a TelemetryAPI) releasePort(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in gatewaycontract.ReleasePortRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "端口释放请求无效")
		return
	}
	affected, err := gatewayrepo.ReleaseChargingPort(
		c.Request.Context(),
		a.DB,
		in.PortID,
		in.OrderNo,
	)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口释放暂不可用", nil)
		return
	}
	if affected == 1 {
		httpapi.OK(c, gatewaycontract.ReleasePortReply{Released: true, Already: false})
		return
	}
	port, err := gatewayrepo.ReadSyncPort(c.Request.Context(), a.DB, in.PortID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口状态暂不可读取", nil)
		return
	}
	if port.Status == "idle" && !port.CurrentOrderID.Valid {
		httpapi.OK(c, gatewaycontract.ReleasePortReply{Released: true, Already: true})
		return
	}
	httpapi.Write(c, http.StatusConflict, 2009, "端口在结算后无法释放", nil)
}

// meterSamples 鉴权后查询 UTC 时间闭区间内、ID 不超过 end_id 的心跳。
// 按 ID 升序最多 10081 条，包含一条超限检查记录。
func (a TelemetryAPI) meterSamples(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Param("device_id")
	if device == "" || len(device) > 64 {
		httpapi.BadRequest(c, "设备无效")
		return
	}
	ackAt, err1 := time.Parse(time.RFC3339Nano, c.Query("ack_at"))
	endAt, err2 := time.Parse(time.RFC3339Nano, c.Query("end_at"))
	endID, err3 := strconv.ParseUint(c.Query("end_id"), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || endID == 0 || ackAt.After(endAt) {
		httpapi.BadRequest(c, "计量窗口无效")
		return
	}
	rows, err := gatewayrepo.ListMeterSamples(
		c.Request.Context(),
		a.DB,
		device,
		ackAt,
		endAt,
		endID,
	)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "计量证据暂不可读取", nil)
		return
	}
	items := make([]gatewaycontract.SyncEvent, 0, len(rows))
	for _, row := range rows {
		items = append(items, gatewaycontract.SyncEvent{ID: row.ID, Payload: string(row.EventJSON)})
	}
	httpapi.OK(c, gatewaycontract.EventsReply{Items: items})
}

// freezeEndDelivery 保存首次结束请求；重复请求关键字段一致时返回已保存内容。
// frozen 表示使用了旧内容；关键字段冲突返回 409，数据库错误返回 503。
func (a TelemetryAPI) freezeEndDelivery(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in gatewaycontract.FreezeEndDeliveryRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "冻结请求无效")
		return
	}
	payload, frozen, err := gatewayrepo.FreezeEndDelivery(
		c.Request.Context(),
		a.DB,
		in.DeviceEventID,
		in.ChargeOrderID,
		in.Payload,
	)
	if err != nil {
		switch {
		case errors.Is(err, gatewayrepo.ErrEndDeliveryReadFailed):
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执暂不可读取", nil)
		case errors.Is(err, gatewayrepo.ErrEndDeliveryIdentityConflict):
			httpapi.Write(c, http.StatusConflict, 2009, "frozen end result identity conflict", nil)
		case errors.Is(err, gatewayrepo.ErrEndDeliveryFreezeFailed):
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执冻结失败", nil)
		default:
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执冻结失败", nil)
		}
		return
	}
	httpapi.OK(c, gatewaycontract.FreezeEndDeliveryReply{Payload: payload, Frozen: frozen})
}
