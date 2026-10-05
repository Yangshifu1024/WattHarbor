package control

import (
	"encoding/json"
	"errors"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// registerWorkerSync 把 worker 结果同步需要的 gateway_db 读写暴露为内部
// HTTP：启动回执上报、结束事件处理、端口释放、计量证据、结束回执冻结与
// 刷卡事件投递。语义逐项对齐原 worker 直读实现（第 5 批⑤），
// worker 侧不再持有 gateway 库句柄。
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

// startResults 接收 c 中的服务令牌与可选 command_id，返回已回执未上报的启动命令，
// 按 updated_at 升序最多五十条；鉴权失败返回 401，过长筛选值返回 400，读取失败返回 503，无写入。
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

// markStartResultsReported 接收 c 中的服务令牌和命令身份集合，返回同一事务内实际推进的条数。
// 鉴权失败返回 401，绑定失败返回 400；事务失败返回 503 且不输出中间计数，重复身份不重复推进。
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

// endEvents 接收 c 中的服务令牌，返回未处理的 ConsumerType 2/3 结束事件，按 id 升序最多五十条。
// 鉴权失败返回 401，读取失败返回 503；响应保留原事件载荷，不标记事件或写入数据。
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

// chargeCommandByOrder 接收 c 中的服务令牌和订单 ID，以无排序 Limit(1).Find 读取命令，
// 不存在返回 found:false；鉴权失败返回 401，无效 ID 返回 400，读取失败返回 503，无写入。
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

// markDeviceEventsProcessed 接收 c 中的服务令牌和事件身份集合，返回同一事务内实际推进的条数。
// 鉴权失败返回 401，绑定失败返回 400；事务失败返回 503 且不输出中间计数，重复身份不重复推进。
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

// releasePort 接收 c 中的服务令牌、端口 ID 和订单号，条件更新该订单占用的 charging 端口。
// 更新一行返回 released；未更新时读取端口，idle 且占用为 NULL 返回 already，其余状态返回 409。
// 鉴权失败返回 401，绑定失败返回 400，更新与读取失败分别返回原 503 消息；本端点不验证结算状态。
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

// meterSamples 接收 c 中的服务令牌、设备 ID、RFC3339Nano 起止时刻和 end_id，
// 返回 UTC 闭区间内且 id 不超过 end_id 的心跳原始载荷，按 id 升序最多 10081 条（含哨兵预算）。
// 鉴权失败返回 401，无效设备或窗口返回 400，读取失败返回 503，无写入。
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

// freezeEndDelivery 冻结结束回执：已有冻结时校验身份（忽略 segments 后
// 逐字节一致）并返回存量；否则落库首写。并发首写冲突按错误重读校验，
// 不做静默空更新（B19 约束）。
func (a TelemetryAPI) freezeEndDelivery(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in gatewaycontract.FreezeEndDeliveryRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "冻结请求无效")
		return
	}
	stored, err := a.frozenEndPayload(c, in.DeviceEventID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执暂不可读取", nil)
		return
	}
	if stored == "" {
		err = gatewayrepo.InsertFrozenEndDelivery(a.DB.WithContext(c.Request.Context()), in.DeviceEventID, in.ChargeOrderID, in.Payload)
		if err == nil {
			httpapi.OK(c, gatewaycontract.FreezeEndDeliveryReply{Payload: in.Payload, Frozen: false})
			return
		}
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执冻结失败", nil)
			return
		}
		// 主键冲突：并发首写已落库，重读并校验身份。
		stored, err = a.frozenEndPayload(c, in.DeviceEventID)
		if err != nil || stored == "" {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执冻结失败", nil)
			return
		}
	}
	if !sameEndIdentity(stored, in.Payload) {
		httpapi.Write(c, http.StatusConflict, 2009, "frozen end result identity conflict", nil)
		return
	}
	httpapi.OK(c, gatewaycontract.FreezeEndDeliveryReply{Payload: stored, Frozen: true})
}

// frozenEndPayload 接收 c 与 deviceEventID，用原请求 context 读取冻结结束载荷；返回原文或 SQL 错误，无写入。
func (a TelemetryAPI) frozenEndPayload(c *gin.Context, deviceEventID uint64) (string, error) {
	return gatewayrepo.FrozenEndPayload(c.Request.Context(), a.DB, deviceEventID)
}

// sameEndIdentity 比较两份结束回执是否同一事实：忽略 meter.segments 后
// 语义一致即可（重放时 segments 允许为空，其余字段必须逐字节相同）。
func sameEndIdentity(stored, candidate string) bool {
	strip := func(raw string) string {
		var value map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil || !json.Valid([]byte(raw)) {
			return raw
		}
		if meter, ok := value["meter"].(map[string]any); ok {
			delete(meter, "segments")
		}
		normalized, err := json.Marshal(value)
		if err != nil {
			return raw
		}
		return string(normalized)
	}
	return strip(stored) == strip(candidate)
}
