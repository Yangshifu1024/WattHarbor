package central

// DeviceAlertSyncPath 是worker触发central所属告警同步的原内部端点。
const DeviceAlertSyncPath = "/api/v1/internal/device-alerts/sync"

// DeviceAlertSyncData 保留已成功新建的告警数；失败响应也可报告局部进度。
type DeviceAlertSyncData struct {
	Raised int `json:"raised"` /* 本轮新告警数，非负 */
}
