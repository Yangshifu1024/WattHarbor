package central

import (
	"context"
	"fmt"
	"net/http"
	"time"

	contract "wattharbor/internal/contracts/central"
)

// SyncDeviceAlerts 触发一次告警同步，返回新增告警数；请求最多三十秒。
// 不跟随重定向，结果不确定时由下一轮重新扫描事件，不立即重试。
func (c *Client) SyncDeviceAlerts(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	copyClient := *c
	transport := c.transport
	httpClient := http.Client{Timeout: 30 * time.Second}
	if transport.HTTP != nil {
		httpClient = *transport.HTTP
	}
	// 告警同步可能运行二十五秒，因此将请求超时从五秒改为三十秒，仍共用原连接池。
	httpClient.Timeout = 30 * time.Second
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	transport.HTTP = &httpClient
	copyClient.transport = transport
	// deviceAlertSyncWireReply 只校验进度字段是否存在，不把缺失当正常零值。
	type deviceAlertSyncWireReply struct {
		Raised *int `json:"raised"` /* 必需的新增告警数 */
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
