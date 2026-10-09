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

// cardDecideBegin 接收 c 中的服务令牌及 UUID 事件键，交由 repo 锁投递行并在原事务中按需读取事件。
// 协议与事件身份验证仍由本层回调在持锁期间执行；已有冻结回复跳过验证并保持原正常回滚出口。
// 成功返回原事件或冻结回复；鉴权失败 401，输入无效 400，身份不符 409，持久/取消失败为原 503。
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

// errCardIdentity 表示原协议事件无法解码或其 UUID 身份与请求键不一致，供 HTTP 映射原冲突响应。
var errCardIdentity = errors.New("card event identity mismatch")

// cardDecideFinish 接收 c 中的服务令牌、UUID 事件键及候选回复，交由 repo 完成原条件首写和未首写时的存量读取。
// 返回原 stored 标志及权威回复；鉴权失败 401，输入无效 400，更新/读取失败分别映射原两种 503。
// 本层不重算金额、创建新事件、重试或覆盖已有决定。
func (a TelemetryAPI) cardDecideFinish(c *gin.Context) {
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
