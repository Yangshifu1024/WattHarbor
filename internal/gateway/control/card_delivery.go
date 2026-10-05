package control

import (
	"encoding/json"
	"errors"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"time"
)

// cardEvents 在 c 的鉴权及取消范围读取待投递事件（含原始事件与冻结回复），按 id 升序最多100条。
// 返回共享集合或503，不领取事件或修改投递状态。
func (a TelemetryAPI) cardEvents(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	rows, err := repo.ListPendingCardDeliveries(c.Request.Context(), a.DB)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡事件暂不可读取", nil)
		return
	}
	items := make([]contract.CardEvent, 0, len(rows))
	for _, row := range rows {
		items = append(items, contract.CardEvent{EventKey: row.EventKey, Payload: string(row.EventJSON), Response: string(row.ResponseJSON)})
	}
	httpapi.OK(c, contract.CardEventsReply{Items: items})
}

// cardDecideBegin 锁定投递行并返回决策输入：已有冻结回复时只回回复；
// 否则校验事件身份后返回原始事件。锁在事务内释放，回复由 decide-finish 首写冻结。
func (a TelemetryAPI) cardDecideBegin(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		// EventKey 原持久事件幂等键。
		EventKey string `json:"event_key" binding:"required,max=64"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "event_key 无效")
		return
	}
	var eventJSON string
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		frozen, err := repo.LockCardDelivery(tx, in.EventKey)
		if err != nil {
			return err
		}
		if frozen != "" {
			return errResponseFrozen(frozen)
		}
		raw, err := repo.FindDeviceEvent(c.Request.Context(), tx, in.EventKey)
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
type frozenResponse struct {
	response string /* 原已冻结 JSON */
}

// Error 返回已冻结提示，不读写持久状态。
func (e *frozenResponse) Error() string { return "card response already frozen" }

// errResponseFrozen 包装 response 原 JSON 为已有决定出口；无 IO。
func errResponseFrozen(response string) error { return &frozenResponse{response: response} }

// cardDecideFinish 在 c 的鉴权及取消范围首写原事件回复；并发输掉返回已存决定。
// 参数错误400、存储失败503，不重算金额或生成新事件。
func (a TelemetryAPI) cardDecideFinish(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in contract.CardFreezeRequest
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "冻结请求无效")
		return
	}
	stored, err := repo.FreezeCardResponse(c.Request.Context(), a.DB, in.EventKey, in.ResponseJSON)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复冻结暂不可用", nil)
		return
	}
	if stored == 1 {
		httpapi.OK(c, contract.CardFreezeReply{Stored: true, ResponseJSON: in.ResponseJSON})
		return
	}
	frozen, err := repo.FindFrozenCardResponse(c.Request.Context(), a.DB, in.EventKey)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复暂不可读取", nil)
		return
	}
	httpapi.OK(c, contract.CardFreezeReply{Stored: false, ResponseJSON: frozen})
}

// cardEventAdvance 在 c 的鉴权及取消范围推进原事件：retry 增加次数并5秒后重排，done 标记完成。
// 返回共享推进事实，参数错误400、存储失败503，冻结决定保持原值。
func (a TelemetryAPI) cardEventAdvance(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in contract.CardAdvanceRequest
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "推进请求无效")
		return
	}
	affected, err := repo.AdvanceCardDelivery(c.Request.Context(), a.DB, in.EventKey,
		in.Outcome == "retry", in.Error, time.Now().UTC().Add(5*time.Second))
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡投递状态暂不可用", nil)
		return
	}
	httpapi.OK(c, contract.CardAdvanceReply{Advanced: affected == 1})
}

// resolvePort 在 c 的鉴权及取消范围按设备与端口序号只读取业务码，未开通返回 found:false。
// 参数错误400、读取失败503，不创建端口或修改设备状态。
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
	code, err := repo.PortCodeByNumber(c.Request.Context(), a.DB, device, uint8(port))
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口暂不可解析", nil)
		return
	}
	if code == "" {
		httpapi.OK(c, contract.CardPortReply{Found: false})
		return
	}
	httpapi.OK(c, contract.CardPortReply{Found: true, PortCode: code})
}
