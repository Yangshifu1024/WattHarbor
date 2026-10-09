package charging

import "time"

// EndMeter 保存设备结束回执的分段、总电量（Wh）、总时长（秒）和结束时刻。
type EndMeter struct {
	Segments       []MeterSegment `json:"segments,omitempty"`
	ChargedWh      uint32         `json:"charged_wh"`
	ChargedSeconds uint32         `json:"charged_seconds"`
	EndedAt        time.Time      `json:"ended_at"`
	StopReason     uint8          `json:"stop_reason"`
}
