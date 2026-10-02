package admin

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
)

// RegulatoryQueue 把监管报送事件按 event_id 幂等写入 regulatory_report。
// 同 ID 的 type、key 或 data 不一致时拒绝，避免重写事件事实。
type RegulatoryQueue struct {
	AdminDB *sql.DB
}

var errRegulatoryConflict = errors.New("event_id already exists with different content")

// Enqueue 返回是否新创建；重复且内容一致时返回 (false, nil)。
func (q RegulatoryQueue) Enqueue(ctx context.Context, event delivery.Event) (bool, error) {
	data, err := delivery.ValidateEvent(event)
	if err != nil {
		return false, err
	}
	_, err = database.InsertRegulatoryReport(ctx, q.AdminDB, event.EventID, event.ObjectType, event.ObjectKey, string(data))
	if errors.Is(err, database.ErrRegulatoryConflict) {
		return false, errRegulatoryConflict
	}
	return err == nil, err
}

// RegulatoryEventsAPI 暴露监管报送的入队与状态查询；表在 central_db，
// 投递由 worker 的 delivery 组件触发，经内部端点回执状态。
type RegulatoryEventsAPI struct {
	Queue        RegulatoryQueue
	ServiceToken string
}

func (a RegulatoryEventsAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/regulatory/events", a.authorized(), a.enqueue)
	router.GET("/api/v1/internal/regulatory/events/:event_id", a.authorized(), a.status)
}

func (a RegulatoryEventsAPI) authorized() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

func (a RegulatoryEventsAPI) enqueue(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 70*1024)
	var event delivery.Event
	if err := c.ShouldBindJSON(&event); err != nil {
		httpapi.BadRequest(c, "监管对象格式无效")
		return
	}
	created, err := a.Queue.Enqueue(c.Request.Context(), event)
	if err != nil {
		if _, validationErr := delivery.ValidateEvent(event); validationErr != nil {
			httpapi.BadRequest(c, "监管对象字段无效: "+validationErr.Error())
			return
		}
		if errors.Is(err, errRegulatoryConflict) {
			httpapi.Write(c, http.StatusConflict, httpapi.CodeConflict, err.Error(), nil)
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "监管报送队列暂时不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"event_id": event.EventID, "queued": created})
}

func (a RegulatoryEventsAPI) status(c *gin.Context) {
	eventID := c.Param("event_id")
	if uuid.Validate(eventID) != nil {
		httpapi.BadRequest(c, "event_id 无效")
		return
	}
	row, err := database.FindRegulatoryReportStatus(c.Request.Context(), a.Queue.AdminDB, eventID)
	if err == sql.ErrNoRows {
		httpapi.Write(c, http.StatusNotFound, httpapi.CodeNotFound, "监管报送事件不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "监管报送状态暂时无法读取", nil)
		return
	}
	objectType, objectKey, state := row.ObjectType, row.ObjectKey, row.Status
	attempts, next := row.Attempts, row.NextAttempt
	delivered, deliveredMode, lastError := row.DeliveredAt, row.DeliveredMode, row.LastError
	var deliveredAt any
	var mode any
	var message any
	if delivered.Valid {
		deliveredAt = delivered.Time.UTC()
	}
	if lastError.Valid {
		message = lastError.String
	}
	if deliveredMode.Valid {
		mode = deliveredMode.String
	}
	httpapi.OK(c, gin.H{"event_id": eventID, "object_type": objectType, "object_key": objectKey,
		"status": state, "attempts": attempts, "next_attempt_at": next.UTC(), "delivered_at": deliveredAt,
		"delivered_mode": mode, "last_error": message})
}
