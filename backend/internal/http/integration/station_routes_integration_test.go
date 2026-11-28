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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/device"
	"wattharbor/internal/gateway/control"
	userdevice "wattharbor/internal/http/user/device"
	userops "wattharbor/internal/http/user/ops"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
	"wattharbor/internal/testfixture"
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
	id := testfixture.NewID() &^ 15 // 保证相邻站点编号转成 JavaScript Number 后相同。
	tag := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i, status := range []string{"active", "active", "disabled", "construction", "active", "active"} {
		row := map[string]any{"id": id + uint64(i), "name": tag + fmt.Sprint(i), "longitude": "-147.12345678", "latitude": -77 + float64(i)/100, "status": status, "created_at": now.Add(time.Duration(i) * time.Minute)}
		if i == 4 {
			row["deleted_at"] = now
		}
		if err := central.Table("station").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := gw.Table("vendor").Create(map[string]any{"id": id, "vendor_code": tag, "vendor_name": tag, "adapter_class": "dc589", "status": "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 102; i++ {
		board := fmt.Sprintf("SQ%s-%03d", tag[:20], i)
		station := id
		if i == 101 {
			station = id + 5
		}
		if err := central.Table("device_meta").Create(map[string]any{"device_id": board, "station_id": station, "vendor_id": 1, "protocol_adapter": "dc589", "status": "enabled"}).Error; err != nil {
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
		if err := gw.Table("device").Create(map[string]any{"device_id": board, "vendor_id": id, "station_id": station, "port_count": 4, "status": "enabled", "last_heartbeat_at": heartbeat, "last_seen_at": heartbeat}).Error; err != nil {
			t.Fatal(err)
		}
		if i == 1 || i == 3 || i == 101 {
			for j, status := range []string{"idle", "charging", "idle", "idle"} {
				port := map[string]any{"device_id": board, "port_no": j + 1, "port_code": fmt.Sprintf("%s-%d", board, j+1), "status": status}
				if j == 2 {
					port["current_order_id"] = "reserved"
				}
				if j == 3 {
					port["deleted_at"] = now
				}
				if err := gw.Table("device_port").Create(port).Error; err != nil {
					t.Fatal(err)
				}
			}
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
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.IN{Column: "id", Values: []any{id, id + 1, id + 2, id + 3, id + 4, id + 5}}}})
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
	if w.Code != 200 || calls != 2 || !strings.Contains(w.Body.String(), `"page_size":10`) || !strings.Contains(w.Body.String(), `"located":false`) || strings.Count(w.Body.String(), `"distance_km":null`) != 3 || !strings.Contains(w.Body.String(), `"longitude":"-147.12345678"`) {
		t.Fatalf("unlocated %d %s", w.Code, w.Body)
	}
	var page struct {
		Data struct {
			Items []struct {
				IDText    string          `json:"id_text"`              // 客户端可直接用于导航的无损字符串。
				ID        uint64          `json:"id"`                   // 站点主键，用于对应不同站点的摘要。
				Available json.RawMessage `json:"available_port_count"` // 区分字段缺省、零和 null。
				Total     json.RawMessage `json:"total_port_count"`     // 未删除的端口。
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	for _, station := range page.Data.Items {
		want := map[uint64][2]string{id: {"null", "null"}, id + 1: {"0", "0"}, id + 5: {"1", "3"}}[station.ID]
		if station.IDText != strconv.FormatUint(station.ID, 10) {
			t.Fatalf("站点编号丢失精度 %d %s", station.ID, station.IDText)
		}
		if string(station.Available) != want[0] || string(station.Total) != want[1] {
			t.Fatalf("站点端口统计错误 %+v", station)
		}
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
	if calls != 4 {
		t.Fatalf("两次列表查询共请求网关 %d 次，预期 4 次", calls)
	}
	detail := "/api/v1/user/station/" + strconv.FormatUint(id, 10)
	w = call(detail)
	if w.Code != 200 || calls != 6 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"id_text":"%d"`, id)) || strings.Contains(w.Body.String(), "distance_km") {
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
				Last      *time.Time      `json:"last_heartbeat_at"`
				Available json.RawMessage `json:"available_port_count"` // 已测量可用数，缺失摘要为 null。
				Total     json.RawMessage `json:"total_port_count"`     // 已测量端口总数。
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
		available, total := "0", "0"
		switch i {
		case 1:
			total = "3"
		case 2:
			available, total = "null", "null"
		case 3:
			available, total = "1", "3"
		}
		if string(d.Available) != available || string(d.Total) != total {
			t.Fatalf("设备 %d 端口数错误 %+v", i, d)
		}
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
