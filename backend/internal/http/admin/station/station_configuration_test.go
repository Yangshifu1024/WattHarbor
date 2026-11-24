package station

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/http/admin"
)

func TestStationFilterValidation(t *testing.T) {
	for _, tc := range []struct {
		query string
		// required 表示是否必填，valid 表示预期解析是否成功。
		required, valid bool
		// id 是成功解析后的站点号，缺失或非法时为零。
		id uint64
	}{
		{"", false, true, 0}, {"", true, false, 0}, {"?station_id=17", true, true, 17},
		{"?station_id=", false, false, 0}, {"?station_id=0", false, false, 0},
		{"?station_id=-1", false, false, 0}, {"?station_id=abc", false, false, 0},
		{"?station_id=1&station_id=2", false, false, 0}, {"?station_id=9223372036854775808", false, false, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/"+tc.query, nil)
			id, valid := admin.QueryStationID(c, tc.required)
			if valid != tc.valid || id != tc.id {
				t.Fatalf("got (%d,%v), want (%d,%v)", id, valid, tc.id, tc.valid)
			}
			if !valid && w.Code != 400 {
				t.Fatalf("invalid filter returned %d", w.Code)
			}
		})
	}
}
