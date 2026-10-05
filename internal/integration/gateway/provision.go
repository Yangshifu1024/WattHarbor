package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

// Provision 在 ctx 下同步开通 devices 原身份，最多等待十秒，返回网络或原状态失败。
// 沿用原导入语义：只以 HTTP 200 确认成功，不用未知响应重建新的设备身份；业务调用方保存失败状态。
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
