package control

import (
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"time"
)

// cardEvents 返回待投递的刷卡事件（含原始事件与已冻结的回复），按事件 id 升序，最多 100 条。
func (a TelemetryAPI) cardEvents(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var rows []struct {
		EventKey     string `gorm:"column:event_key"`
		EventJSON    []byte `gorm:"column:event_json"`
		ResponseJSON []byte `gorm:"column:response_json"`
	}
	err := a.DB.WithContext(c.Request.Context()).Table("card_event_delivery x").
		Select("x.event_key,e.event_json,x.response_json").
		Joins("JOIN device_event e ON e.event_key=x.event_key").
		Where("x.status='pending' AND x.next_attempt_at<=UTC_TIMESTAMP(3)").
		Order("e.id").Limit(100).Find(&rows).Error
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
		var stored struct {
			ResponseJSON []byte `gorm:"column:response_json"`
		}
		if err := tx.Table("card_event_delivery").Clauses(clause.Locking{Strength: "UPDATE"}).Where("event_key=?", in.EventKey).Take(&stored).Error; err != nil {
			return err
		}
		if len(stored.ResponseJSON) > 0 {
			eventJSON = ""
			return errResponseFrozen(string(stored.ResponseJSON))
		}
		var row struct {
			EventJSON []byte `gorm:"column:event_json"`
		}
		if err := tx.Table("device_event").Where("event_key=?", in.EventKey).Take(&row).Error; err != nil {
			return err
		}
		var e protocol.Event
		if json.Unmarshal(row.EventJSON, &e) != nil || e.EventID != in.EventKey || uuid.Validate(e.EventID) != nil {
			return errCardIdentity
		}
		eventJSON = string(row.EventJSON)
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
	res := a.DB.WithContext(c.Request.Context()).Table("card_event_delivery").
		Where("event_key=? AND (response_json IS NULL OR response_json='')", in.EventKey).
		Update("response_json", in.ResponseJSON)
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复冻结暂不可用", nil)
		return
	}
	if res.RowsAffected == 1 {
		httpapi.OK(c, gin.H{"stored": true, "response_json": in.ResponseJSON})
		return
	}
	var stored string
	if err := a.DB.WithContext(c.Request.Context()).Table("card_event_delivery").Select("response_json").Where("event_key=?", in.EventKey).Take(&stored).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"stored": false, "response_json": stored})
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
	var res *gorm.DB
	if in.Outcome == "retry" {
		res = a.DB.WithContext(c.Request.Context()).Table("card_event_delivery").Where("event_key=?", in.EventKey).
			Updates(map[string]any{
				"attempts":        gorm.Expr("attempts+1"),
				"next_attempt_at": time.Now().UTC().Add(5 * time.Second),
				"last_error":      in.Error,
			})
	} else {
		res = a.DB.WithContext(c.Request.Context()).Table("card_event_delivery").Where("event_key=?", in.EventKey).
			Updates(map[string]any{"status": "done", "last_error": nil})
	}
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡投递状态暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"advanced": res.RowsAffected == 1})
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
	var code string
	res := a.DB.WithContext(c.Request.Context()).Table("device_port").
		Where("device_id=? AND port_no=? AND deleted_at IS NULL", device, uint8(port)).
		Pluck("port_code", &code)
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口暂不可解析", nil)
		return
	}
	if code == "" {
		httpapi.OK(c, gin.H{"found": false})
		return
	}
	httpapi.OK(c, gin.H{"found": true, "port_code": code})
}
