package charging

import "time"

type EndMeter struct {
	Segments       []MeterSegment `json:"segments,omitempty"`
	ChargedWh      uint32         `json:"charged_wh"`
	ChargedSeconds uint32         `json:"charged_seconds"`
	EndedAt        time.Time      `json:"ended_at"`
	StopReason     uint8          `json:"stop_reason"`
}
