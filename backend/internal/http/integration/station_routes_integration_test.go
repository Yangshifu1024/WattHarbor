//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"chargepilot/internal/device"
	"chargepilot/internal/gateway/control"
	userdevice "chargepilot/internal/http/user/device"
	userops "chargepilot/internal/http/user/ops"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/testfixture"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TestRegisteredPublicStationQueriesUseOwnedSources 验证无需登录的公开接口、真实两库和实际摘要端点。
// 数据在外层事务回滚，持续查询条件只隔离本测试公告／站点；不创建用户、资金或 Redis 会话。
func TestRegisteredPublicStationQueriesUseOwnedSources(t *testing.T) {
	central := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	defer central.Rollback()
	gw := cardRouteDB(t, "DATABASE_URL_GATEWAY").Begin()
	defer gw.Rollback()
	if central.Error != nil || gw.Error != nil {
		t.Fatalf("begin %v %v", central.Error, gw.Error)
	}
	id := testfixture.NewID()
	tag := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i, status := range []string{"active", "active", "disabled", "construction", "active"} {
		row := map[string]any{"id": id + uint64(i), "name": tag + fmt.Sprint(i), "longitude": "-147.12345678", "latitude": -77 + float64(i)/100, "status": status, "created_at": now.Add(time.Duration(i) * time.Minute)}
		if i == 4 {
			row["deleted_at"] = now
		}
		if err := central.Table("station").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 101; i++ {
		board := fmt.Sprintf("SQ%s-%03d", tag[:20], i)
		if err := central.Table("device_meta").Create(map[string]any{"device_id": board, "station_id": id, "vendor_id": 1, "protocol_adapter": "dc589", "status": "enabled"}).Error; err != nil {
			t.Fatal(err)
		}
		var heartbeat any = now.Add(-time.Minute)
		if i == 0 {
			heartbeat = nil
		}
		if i == 1 {
			heartbeat = now.Add(-2 * time.Hour)
		}
		if i == 2 {
			continue
		} // 中央设备尚未在网关开通，运行态未知
		if err := gw.Table("device").Create(map[string]any{"device_id": board, "vendor_id": 1, "station_id": id, "port_count": 1, "status": "enabled", "last_heartbeat_at": heartbeat}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := central.Table("device_meta").Create(map[string]any{"device_id": "retired-" + tag, "station_id": id, "vendor_id": 1, "protocol_adapter": "dc589", "status": "retired"}).Error; err != nil {
		t.Fatal(err)
	}
	for i, scope := range []string{"global", "station", "station", "city", "global", "global", "global", "station"} {
		row := map[string]any{"id": id + uint64(i), "title": tag + fmt.Sprint(i), "content": "中文公告", "scope": scope, "start_at": now.Add(-time.Hour), "status": "published", "created_by": 1}
		if scope == "station" {
			row["target_ids"] = `["` + strconv.FormatUint(id, 10) + `"]`
		}
		if i == 2 {
			row["target_ids"] = `["other"]`
		}
		if i == 4 {
			row["end_at"] = now.Add(-time.Second)
		}
		if i == 5 {
			row["start_at"] = now.Add(time.Hour)
		}
		if i == 6 {
			row["status"] = "draft"
		}
		if i == 7 {
			row["target_ids"] = `[123]`
		} // 规则只接受字符串目标，数字不猜测
		if err := central.Table("announcement").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	key := "station_query_" + tag
	if err := central.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "station" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.IN{Column: "id", Values: []any{id, id + 1, id + 2, id + 3, id + 4}}}})
		}
		if q.Statement.Table == "announcement" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Gte{Column: "id", Value: id}, clause.Lt{Column: "id", Value: id + 8}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer central.Callback().Query().Remove(key)
	calls := 0
	gwr := httpapi.NewRouter()
	gwr.Use(func(c *gin.Context) { calls++; c.Next() })
	(control.DeviceSummaryAPI{DB: gw, ServiceToken: "station-source"}).Register(gwr)
	server := httptest.NewServer(gwr)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "station-source")
	if err != nil {
		t.Fatal(err)
	}
	announcements := ops.AnnouncementQueryService{DB: central}
	service := device.UserStationService{DB: central, Announcements: announcements, Summaries: client}
	router := httpapi.NewRouter()
	userdevice.API{Stations: service}.Register(router)
	userops.API{Announcements: announcements}.Register(router)
	call := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	nearby := "/api/v1/user/station/nearby"
	w := call(nearby)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"page_size":10`) || !strings.Contains(w.Body.String(), `"located":false`) || strings.Count(w.Body.String(), `"distance_km":null`) != 2 || !strings.Contains(w.Body.String(), `"longitude":"-147.12345678"`) {
		t.Fatalf("unlocated %d %s", w.Code, w.Body)
	}
	for _, query := range []string{"?longitude=1", "?longitude=NaN&latitude=0", "?radius_km=Inf", "?radius_km=51", "?page_size=101"} {
		if w := call(nearby + query); w.Code != 400 {
			t.Fatalf("invalid %s %d", query, w.Code)
		}
	}
	w = call(nearby + "?longitude=-147.12345678&latitude=-77&radius_km=1")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"located":true`) || !strings.Contains(w.Body.String(), `"distance_km":0`) || strings.Contains(w.Body.String(), tag+"1") {
		t.Fatalf("located %d %s", w.Code, w.Body)
	}
	detail := "/api/v1/user/station/" + strconv.FormatUint(id, 10)
	w = call(detail)
	if w.Code != 200 || calls != 2 || strings.Contains(w.Body.String(), "distance_km") {
		t.Fatalf("detail %d calls=%d %s", w.Code, calls, w.Body)
	}
	var reply struct {
		Data struct {
			// Announcements 站点详情公开公告集合。
			Announcements []map[string]any `json:"announcements"`
			// Devices 站点详情公开设备摘要集合。
			Devices []struct {
				// DeviceID 本断言核对的设备业务编码。
				DeviceID string `json:"device_id"`
				// Online gateway 回执中的设备在线标志。
				Online bool `json:"online"`
				// Known gateway 回执中的设备状态是否已知。
				Known bool `json:"runtime_available"`
				// Last gateway 回执中的最后在线时刻。
				Last *time.Time `json:"last_heartbeat_at"`
			} `json:"devices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Data.Announcements) != 2 || len(reply.Data.Devices) != 101 {
		t.Fatalf("shape %s", w.Body)
	}
	for i, d := range reply.Data.Devices {
		if i == 0 {
			if !d.Known || d.Last != nil || d.Online {
				t.Fatal("nil heartbeats guessed")
			}
		}
		if i == 1 && (!d.Known || d.Online) {
			t.Fatal("offline lost")
		}
		if i == 2 && (d.Known || d.Last != nil || d.Online) {
			t.Fatal("unknown guessed")
		}
		if i > 2 && (!d.Known || !d.Online) {
			t.Fatal("online lost")
		}
	}
	if w := call("/api/v1/user/announcement/list"); w.Code != 200 || strings.Count(w.Body.String(), `"title":`) != 1 || !strings.Contains(w.Body.String(), tag+"0") {
		t.Fatalf("public announcements %d %s", w.Code, w.Body)
	}
	for _, sid := range []uint64{id + 2, id + 3, id + 4, id + 50} {
		if w := call("/api/v1/user/station/" + strconv.FormatUint(sid, 10)); w.Code != 404 {
			t.Fatalf("inactive/missing %d %d", sid, w.Code)
		}
	}
	if w := call("/api/v1/user/station/" + strconv.FormatUint(id+1, 10)); w.Code != 200 || !strings.Contains(w.Body.String(), `"devices":[]`) {
		t.Fatalf("empty station %d %s", w.Code, w.Body)
	}
	failure := errors.New("station source unavailable")
	broken := "broken_" + tag
	if err := central.Callback().Query().Before("gorm:query").Register(broken, func(q *gorm.DB) {
		if q.Statement.Table == "announcement" {
			q.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if w := call(detail); w.Code != 503 || !strings.Contains(w.Body.String(), "站点公告暂时无法读取") {
		t.Fatalf("failed announcement %d %s", w.Code, w.Body)
	}
	central.Callback().Query().Remove(broken)
	server.Close()
	w = call(detail)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"runtime_available":true`) || strings.Count(w.Body.String(), `"runtime_available":false`) != 101 {
		t.Fatalf("unknown gateway %d %s", w.Code, w.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Detail(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}
