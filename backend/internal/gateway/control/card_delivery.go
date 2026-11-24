package control

import (
	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/gateway/repo"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/protocol"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
	"strconv"
	"time"
)

// cardEvents 按 ID 升序返回最多 100 条待投递卡事件及其已保存回复。
func (a DeviceEventAPI) cardEvents(c *gin.Context) {
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

// cardDecideBegin 锁定投递记录后查询事件；已有回复时直接返回并跳过事件校验。
func (a DeviceEventAPI) cardDecideBegin(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		// EventKey 持久事件幂等键。
		EventKey string `json:"event_key" binding:"required,max=64"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "event_key 无效")
		return
	}
	evidence, err := repo.ReadCardDecision(
		c.Request.Context(),
		a.DB,
		in.EventKey,
		func(raw []byte) error {
			var e protocol.Event
			if json.Unmarshal(raw, &e) != nil || e.EventID != in.EventKey || uuid.Validate(e.EventID) != nil {
				return errCardIdentity
			}
			return nil
		},
	)
	if errors.Is(err, errCardIdentity) {
		httpapi.Write(c, http.StatusConflict, 2009, "card event identity mismatch", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡决策输入暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"event_json": string(evidence.RawEvent), "response_json": evidence.FrozenResponse})
}

// errCardIdentity 表示协议事件无法解码或其 UUID 身份与请求键不一致，供 HTTP 映射冲突响应。
var errCardIdentity = errors.New("card event identity mismatch")

// cardDecideFinish 保存首次回复；已有回复时原样返回。
// stored 表示本次是否写入成功，更新失败与读取失败使用不同错误提示。
func (a DeviceEventAPI) cardDecideFinish(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in contract.CardFreezeRequest
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "冻结请求无效")
		return
	}
	frozen, stored, err := repo.FreezeCardResponse(
		c.Request.Context(),
		a.DB,
		in.EventKey,
		in.ResponseJSON,
	)
	if err != nil {
		if errors.Is(err, repo.ErrCardResponseReadFailed) {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复暂不可读取", nil)
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复冻结暂不可用", nil)
		return
	}
	httpapi.OK(c, contract.CardFreezeReply{Stored: stored, ResponseJSON: frozen})
}

// cardEventAdvance 标记完成，或将失败事件延后 5 秒并增加重试次数。
func (a DeviceEventAPI) cardEventAdvance(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in contract.CardAdvanceRequest
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "推进请求无效")
		return
	}
	affected, err := repo.AdvanceCardDelivery(c.Request.Context(), a.DB, in.EventKey,
		in.Outcome == contract.CardAdvanceOutcomeRetry, in.Error, time.Now().UTC().Add(5*time.Second))
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡投递状态暂不可用", nil)
		return
	}
	httpapi.OK(c, contract.CardAdvanceReply{Advanced: affected == 1})
}

// resolvePort 按设备和端口序号查询端口编码；未开通时返回 found=false。
func (a DeviceEventAPI) resolvePort(c *gin.Context) {
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
