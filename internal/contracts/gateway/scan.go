package gateway

import "errors"

// ScanResolvePath 是原设备／端口发现端点，保持二维码发现 URL 契约。
const ScanResolvePath = "/api/v1/internal/scan/resolve"

// ErrScanNotFound 表示网关已响应但扫描身份不存在，不代表设备离线。
var ErrScanNotFound = errors.New("scan identity not found")
