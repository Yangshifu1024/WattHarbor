package control

import (
	"encoding/json"
	"errors"
	gatewaystore "github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"time"
)

// registerWorkerSync 把 worker 结果同步需要的 gateway_db 读写暴露为内部
// HTTP：启动回执上报、结束事件处理、端口释放、计量证据、结束回执冻结与
// 刷卡事件投递。语义逐项对齐原 worker 直读实现（第 5 批⑤），
// worker 侧不再持有 gateway 库句柄。
func (a TelemetryAPI) registerWorkerSync(r *gin.Engine) {
	r.GET("/api/v1/internal/start-results", a.startResults)
	r.POST("/api/v1/internal/start-results/mark-reported", a.markStartResultsReported)
	r.GET("/api/v1/internal/end-events", a.endEvents)
	r.GET("/api/v1/internal/charge-commands/by-order/:order_id", a.chargeCommandByOrder)
	r.POST("/api/v1/internal/device-events/mark-processed", a.markDeviceEventsProcessed)
	r.POST("/api/v1/internal/ports/release", a.releasePort)
	r.GET("/api/v1/internal/devices/:device_id/meter-samples", a.meterSamples)
	r.POST("/api/v1/internal/charge-end-deliveries/freeze", a.freezeEndDelivery)
	r.GET("/api/v1/internal/card-events", a.cardEvents)
	r.POST("/api/v1/internal/card-events/decide-begin", a.cardDecideBegin)
	r.POST("/api/v1/internal/card-events/decide-finish", a.cardDecideFinish)
	r.POST("/api/v1/internal/card-events/advance", a.cardEventAdvance)
	r.GET("/api/v1/internal/ports/resolve", a.resolvePort)
}

// startResults 返回已回执未上报的启动命令，按 updated_at 升序，最多 50 条。
func (a TelemetryAPI) startResults(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	query := a.DB.WithContext(c.Request.Context()).Model(&workerSyncCommandRow{}).
		Where("status IN ('acked','rejected') AND result_reported = FALSE AND result_code IS NOT NULL AND ack_at IS NOT NULL")
	if filter := c.Query("command_id"); filter != "" {
		if len(filter) > 64 {
			httpapi.BadRequest(c, "command_id 无效")
			return
		}
		query = query.Where("command_id = ?", filter)
	}
	var rows []workerSyncCommandRow
	if err := query.Order("updated_at").Limit(50).Find(&rows).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "启动回执暂不可读取", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{
			"command_id": row.CommandID, "stop_command_id": row.StopCommandID,
			"charge_order_id": row.ChargeOrderID, "order_no": row.OrderNo,
			"device_id": row.DeviceID, "port_no": row.PortNo,
			"port_id": nullableInt(row.PortID), "status": row.Status,
			"result_code": gatewaystore.NullInt16ToAny(row.ResultCode), "ack_at": gatewaystore.NullTimeToAny(row.AckAt),
		})
	}
	httpapi.OK(c, gin.H{"items": items})
}

// markStartResultsReported 只推进仍处于未上报状态的行，重放安全。
func (a TelemetryAPI) markStartResultsReported(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		CommandIDs []string `json:"command_ids" binding:"required,min=1,max=50"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "上报标记请求无效")
		return
	}
	marked := 0
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, id := range in.CommandIDs {
			res := tx.Model(&workerSyncCommandRow{}).
				Where("command_id = ? AND status IN ('acked','rejected') AND result_reported = FALSE", id).
				Update("result_reported", true)
			if res.Error != nil {
				return res.Error
			}
			marked += int(res.RowsAffected)
		}
		return nil
	})
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "上报标记暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"marked": marked})
}

// endEvents 返回未处理的刷卡/扫码结束事件（ConsumerType 2/3），按 id 升序，最多 50 条。
func (a TelemetryAPI) endEvents(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var rows []workerSyncEventRow
	err := a.DB.WithContext(c.Request.Context()).
		Select("id, event_json").
		Where("event_type = 'charge_end' AND processed_at IS NULL AND JSON_EXTRACT(event_json, '$.ConsumerType') IN (2,3)").
		Order("id").Limit(50).Find(&rows).Error
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束事件暂不可读取", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{"id": row.ID, "payload": string(row.EventJSON)})
	}
	httpapi.OK(c, gin.H{"items": items})
}

// chargeCommandByOrder 按订单取一条启动命令（原 Take 语义：不保证多行时的选择），不存在返回 found:false。
func (a TelemetryAPI) chargeCommandByOrder(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	orderID, err := strconv.ParseUint(c.Param("order_id"), 10, 64)
	if err != nil || orderID == 0 {
		httpapi.BadRequest(c, "订单 ID 无效")
		return
	}
	var row workerSyncCommandRow
	res := a.DB.WithContext(c.Request.Context()).Model(&workerSyncCommandRow{}).Where("charge_order_id = ?", orderID).Limit(1).Find(&row)
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "启动命令暂不可读取", nil)
		return
	}
	if res.RowsAffected == 0 {
		httpapi.OK(c, gin.H{"found": false})
		return
	}
	httpapi.OK(c, gin.H{"found": true, "command": gin.H{
		"command_id": row.CommandID, "stop_command_id": row.StopCommandID,
		"charge_order_id": row.ChargeOrderID, "order_no": row.OrderNo,
		"device_id": row.DeviceID, "port_no": row.PortNo,
		"port_id": nullableInt(row.PortID), "status": row.Status,
		"result_code": gatewaystore.NullInt16ToAny(row.ResultCode), "ack_at": gatewaystore.NullTimeToAny(row.AckAt),
	}})
}

// markDeviceEventsProcessed 只推进未处理的行，返回推进条数。
func (a TelemetryAPI) markDeviceEventsProcessed(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		IDs []uint64 `json:"ids" binding:"required,min=1,max=100"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "处理标记请求无效")
		return
	}
	marked := 0
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, id := range in.IDs {
			affected, err := gatewaystore.MarkSyncEventProcessed(tx, id)
			if err != nil {
				return err
			}
			marked += int(affected)
		}
		return nil
	})
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "处理标记暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"marked": marked})
}

// releasePort 在 central 确认结算后释放端口：条件更新占用中的端口；
// 已被释放过的端口返回 already，其余状态视为释放失败（409）。
func (a TelemetryAPI) releasePort(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		PortID  uint64 `json:"port_id" binding:"required"`
		OrderNo string `json:"order_no" binding:"required,max=64"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "端口释放请求无效")
		return
	}
	res := a.DB.WithContext(c.Request.Context()).Model(&workerSyncPortRow{}).
		Where("id = ? AND current_order_id = ? AND status = 'charging'", in.PortID, in.OrderNo).
		Updates(map[string]any{"status": "idle", "current_order_id": nil})
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口释放暂不可用", nil)
		return
	}
	if res.RowsAffected == 1 {
		httpapi.OK(c, gin.H{"released": true, "already": false})
		return
	}
	var port workerSyncPortRow
	if err := a.DB.WithContext(c.Request.Context()).Select("status, current_order_id").Where("id = ?", in.PortID).Take(&port).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口状态暂不可读取", nil)
		return
	}
	if port.Status == "idle" && !port.CurrentOrderID.Valid {
		httpapi.OK(c, gin.H{"released": true, "already": true})
		return
	}
	httpapi.Write(c, http.StatusConflict, 2009, "端口在结算后无法释放", nil)
}

// meterSamples 返回计量证据：设备心跳在 [ack_at, end_at] 内且 id 不超过
// end_id 的全部原始事件，按 id 升序，上限 10081（末行为哨兵，与旧实现一致）。
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
	var rows []workerSyncEventRow
	err := a.DB.WithContext(c.Request.Context()).Select("id, event_json").
		Where("device_id=? AND event_type='heartbeat' AND id<=? AND received_at>=? AND received_at<=?", device, endID, ackAt.UTC(), endAt.UTC()).
		Order("id").Limit(10081).Find(&rows).Error
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "计量证据暂不可读取", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{"id": row.ID, "payload": string(row.EventJSON)})
	}
	httpapi.OK(c, gin.H{"items": items})
}

// freezeEndDelivery 冻结结束回执：已有冻结时校验身份（忽略 segments 后
// 逐字节一致）并返回存量；否则落库首写。并发首写冲突按错误重读校验，
// 不做静默空更新（B19 约束）。
func (a TelemetryAPI) freezeEndDelivery(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		DeviceEventID uint64 `json:"device_event_id" binding:"required"`
		ChargeOrderID uint64 `json:"charge_order_id" binding:"required"`
		Payload       string `json:"payload" binding:"required,max=65535"`
	}
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
		err = gatewaystore.InsertFrozenEndDelivery(a.DB.WithContext(c.Request.Context()), in.DeviceEventID, in.ChargeOrderID, in.Payload)
		if err == nil {
			httpapi.OK(c, gin.H{"payload": in.Payload, "frozen": false})
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
	httpapi.OK(c, gin.H{"payload": stored, "frozen": true})
}

func (a TelemetryAPI) frozenEndPayload(c *gin.Context, deviceEventID uint64) (string, error) {
	return gatewaystore.FrozenEndPayload(c.Request.Context(), a.DB, deviceEventID)
}

// sameEndIdentity 比较两份结束回执是否同一事实：忽略 meter.segments 后
// 语义一致即可（重放时 segments 允许为空，其余字段必须逐字节相同）。
func sameEndIdentity(stored, candidate string) bool {
	strip := func(raw string) string {
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
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
