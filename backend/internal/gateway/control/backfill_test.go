package control

import (
	"encoding/json"
	"testing"
	"time"
)

// frames 解析 raw 测试正文并返回帧集合，非法 JSON 终止当前测试；不注册路由或访问数据库。
func frames(t *testing.T, raw string) []backfillFrame {
	t.Helper()
	var in struct {
		// Frames JSON 测试帧列表，仅由解析夹具使用。
		Frames []backfillFrame `json:"frames"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	return in.Frames
}

// validFrame 保留合法补采帧正文模板，不创建持久记录。
const validFrame = `{"device_id":"xx_001_abc","port_no":1,"msg_type":"telemetry","ts":"%s","payload":{"voltage_v":"220.5","power_w":704.0}}`

func TestParseBackfillAcceptsStringAndNumberPayloads(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-time.Hour).Format(time.RFC3339)
	got, err := parseBackfill("xx_001_abc", frames(t, "{\"frames\":["+
		`{"device_id":"xx_001_abc","port_no":1,"msg_type":"telemetry","ts":"`+ts+`","payload":{"voltage_v":"220.5","power_w":704.0}}`+
		"]}"), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 samples, got %d", len(got))
	}
	// 数值会归一到列的 6 位小数精度上，
	// 全程都不经过 float64。
	byMetric := map[string]string{}
	for _, s := range got {
		byMetric[s.Metric] = s.Value
	}
	if byMetric["voltage_v"] != "220.500000" || byMetric["power_w"] != "704.000000" {
		t.Fatalf("unexpected values: %+v", byMetric)
	}
}

func TestParseBackfillRejectsInvalidFrames(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	valid := now.Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		name string
		body string
	}{
		{"device id mismatch", `{"frames":[{"device_id":"other_device","msg_type":"telemetry","ts":"` + valid + `","payload":{"power_w":"1"}}]}`},
		{"unsupported metric", `{"frames":[{"device_id":"xx_001_abc","msg_type":"telemetry","ts":"` + valid + `","payload":{"temperature_f":"70"}}]}`},
		{"non numeric value", `{"frames":[{"device_id":"xx_001_abc","msg_type":"telemetry","ts":"` + valid + `","payload":{"power_w":"overload"}}]}`},
		{"malformed timestamp", `{"frames":[{"device_id":"xx_001_abc","msg_type":"telemetry","ts":"27/09/2026","payload":{"power_w":"1"}}]}`},
		{"future beyond skew", `{"frames":[{"device_id":"xx_001_abc","msg_type":"telemetry","ts":"` + now.Add(time.Hour).Format(time.RFC3339) + `","payload":{"power_w":"1"}}]}`},
		{"older than retention", `{"frames":[{"device_id":"xx_001_abc","msg_type":"telemetry","ts":"` + now.Add(-400*24*time.Hour).Format(time.RFC3339) + `","payload":{"power_w":"1"}}]}`},
		{"empty payload", `{"frames":[{"device_id":"xx_001_abc","msg_type":"telemetry","ts":"` + valid + `","payload":{}}]}`},
		{"wrong message type", `{"frames":[{"device_id":"xx_001_abc","msg_type":"fault","ts":"` + valid + `","payload":{"power_w":"1"}}]}`},
		{"port out of range", `{"frames":[{"device_id":"xx_001_abc","port_no":0,"msg_type":"telemetry","ts":"` + valid + `","payload":{"power_w":"1"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseBackfill("xx_001_abc", frames(t, tc.body), now); err == nil {
				t.Fatal("expected the batch to be rejected")
			}
		})
	}
}

func TestParseBackfillCollapsesDuplicateSamples(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-time.Hour).Format(time.RFC3339)
	// 设备在传输途中重试时会逐字重发同一批帧；重试绝不能
	// 把同一条读数算两遍。
	body := `{"frames":[` +
		`{"device_id":"xx_001_abc","port_no":1,"msg_type":"telemetry","ts":"` + ts + `","payload":{"power_w":"10"}},` +
		`{"device_id":"xx_001_abc","port_no":1,"msg_type":"telemetry","ts":"` + ts + `","payload":{"power_w":"10"}},` +
		`{"device_id":"xx_001_abc","port_no":2,"msg_type":"telemetry","ts":"` + ts + `","payload":{"power_w":"10"}}` +
		`]}`
	got, err := parseBackfill("xx_001_abc", frames(t, body), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 distinct samples, got %d", len(got))
	}
}
