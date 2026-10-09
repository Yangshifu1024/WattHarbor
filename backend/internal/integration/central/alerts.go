package central

import (
	"context"
	"fmt"
	"net/http"
	"time"

	contract "chargepilot/internal/contracts/central"
)

// SyncDeviceAlerts 在ctx下触发原同步用例，返回本轮新告警数或传输／契约错误。
// 最多30秒，禁止重定向且使用受管理Transport；未知结果交由下一轮原事件重扫，不重试。
func (c *Client) SyncDeviceAlerts(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	copyClient := *c
	transport := c.transport
	httpClient := http.Client{Timeout: 30 * time.Second}
	if transport.HTTP != nil {
		httpClient = *transport.HTTP
	}
	// worker共用池原默认5秒；本触发允许central的25秒受管理轮次，复制请求Client覆盖期限而不新建Transport。
	httpClient.Timeout = 30 * time.Second
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	transport.HTTP = &httpClient
	copyClient.transport = transport
	// deviceAlertSyncWireReply 只校验原进度字段是否存在，不把缺失当正常零值。
	type deviceAlertSyncWireReply struct {
		Raised *int `json:"raised"` /* 必需的原新告警数 */
	}
	data, err := post[deviceAlertSyncWireReply](ctx, &copyClient, contract.DeviceAlertSyncPath, nil)
	if err != nil {
		return 0, err
	}
	if data.Raised == nil || *data.Raised < 0 {
		return 0, fmt.Errorf("%w: raised count", ErrInvalidResponse)
	}
	return *data.Raised, nil
}
