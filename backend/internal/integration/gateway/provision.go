package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	contract "wattharbor/internal/contracts/gateway"
)

// Provision 同步开通设备，最多等待十秒，仅 HTTP 200 表示成功。
// 结果不确定时不生成新设备编号，调用方保存失败状态。
func (c *Client) Provision(ctx context.Context, devices []contract.ProvisionDevice) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, err := json.Marshal(contract.ProvisionRequest{Devices: devices})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+contract.ProvisionPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Token", c.token)
	client := c.transport.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return errors.New("gateway did not confirm device provision")
	}
	return nil
}
