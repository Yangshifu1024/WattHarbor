package charging

// SameSegments 逐段比较时间、电量、峰值和可选功率；nil 与空列表视为相同。
func SameSegments(a, b []MeterSegment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].EnergyWh != b[i].EnergyWh || a[i].PeakW != b[i].PeakW || (a[i].PowerW == nil) != (b[i].PowerW == nil) || a[i].PowerW != nil && *a[i].PowerW != *b[i].PowerW || !a[i].StartedAt.Equal(b[i].StartedAt) || !a[i].EndedAt.Equal(b[i].EndedAt) {
			return false
		}
	}
	return true
}
