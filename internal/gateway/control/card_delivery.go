package control

import (
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"time"
)

// cardEvents 返回待投递的刷卡事件（含原始事件与已冻结的回复），按事件 id 升序，最多 100 条。
func (a TelemetryAPI) cardEvents(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	rows, err := store.ListPendingCardDeliveries(c.Request.Context(), a.DB)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡事件暂不可读取", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{
			"event_key": row.EventKey, "payload": string(row.EventJSON),
			"response": string(row.ResponseJSON),
		})
	}
	httpapi.OK(c, gin.H{"items": items})
}

// cardDecideBegin 锁定投递行并返回决策输入：已有冻结回复时只回回复；
// 否则校验事件身份后返回原始事件。锁在事务内释放，回复由 decide-finish 首写冻结。
func (a TelemetryAPI) cardDecideBegin(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		EventKey string `json:"event_key" binding:"required,max=64"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "event_key 无效")
		return
	}
	var eventJSON string
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		frozen, err := store.LockCardDelivery(tx, in.EventKey)
		if err != nil {
			return err
		}
		if frozen != "" {
			return errResponseFrozen(frozen)
		}
		raw, err := store.FindDeviceEvent(c.Request.Context(), tx, in.EventKey)
		if err != nil {
			return err
		}
		var e protocol.Event
		if json.Unmarshal(raw, &e) != nil || e.EventID != in.EventKey || uuid.Validate(e.EventID) != nil {
			return errCardIdentity
		}
		eventJSON = string(raw)
		return nil
	})
	var frozen *frozenResponse
	if errors.As(err, &frozen) {
		httpapi.OK(c, gin.H{"event_json": "", "response_json": frozen.response})
		return
	}
	if errors.Is(err, errCardIdentity) {
		httpapi.Write(c, http.StatusConflict, 2009, "card event identity mismatch", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡决策输入暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"event_json": eventJSON, "response_json": ""})
}

var errCardIdentity = errors.New("card event identity mismatch")

// frozenResponse 是 decide-begin 发现已有冻结回复时的正常出口。
type frozenResponse struct{ response string }

func (e *frozenResponse) Error() string { return "card response already frozen" }

func errResponseFrozen(response string) error { return &frozenResponse{response: response} }

// cardDecideFinish 首写冻结决策回复；并发输掉时返回已存回复，调用方使用存量。
func (a TelemetryAPI) cardDecideFinish(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		EventKey     string `json:"event_key" binding:"required,max=64"`
		ResponseJSON string `json:"response_json" binding:"required,max=65535"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "冻结请求无效")
		return
	}
	stored, err := store.FreezeCardResponse(c.Request.Context(), a.DB, in.EventKey, in.ResponseJSON)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复冻结暂不可用", nil)
		return
	}
	if stored == 1 {
		httpapi.OK(c, gin.H{"stored": true, "response_json": in.ResponseJSON})
		return
	}
	frozen, err := store.FindFrozenCardResponse(c.Request.Context(), a.DB, in.EventKey)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"stored": false, "response_json": frozen})
}

// cardEventAdvance 推进投递状态：retry 增加尝试次数并 5 秒后重排；done 标记完成。
func (a TelemetryAPI) cardEventAdvance(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		EventKey string `json:"event_key" binding:"required,max=64"`
		Outcome  string `json:"outcome" binding:"required,oneof=retry done"`
		Error    string `json:"error" binding:"omitempty,max=255"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "推进请求无效")
		return
	}
	affected, err := store.AdvanceCardDelivery(c.Request.Context(), a.DB, in.EventKey,
		in.Outcome == "retry", in.Error, time.Now().UTC().Add(5*time.Second))
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡投递状态暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"advanced": affected == 1})
}

// resolvePort 按设备与端口序号取二维码，未开通返回 found:false。
func (a TelemetryAPI) resolvePort(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Query("device_id")
	port, err := strconv.ParseUint(c.Query("port_no"), 10, 8)
	if err != nil || device == "" || len(device) > 64 || port == 0 {
		httpapi.BadRequest(c, "设备或端口无效")
		return
	}
	code, err := store.PortCodeByNumber(c.Request.Context(), a.DB, device, uint8(port))
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口暂不可解析", nil)
		return
	}
	if code == "" {
		httpapi.OK(c, gin.H{"found": false})
		return
	}
	httpapi.OK(c, gin.H{"found": true, "port_code": code})
}
