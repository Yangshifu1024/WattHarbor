package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"chargepilot/internal/platform/netguard"
)

// Sender 是监管报送的投递适配器；Mode 记入 regulatory_report.delivered_mode。
type Sender interface {
	// Mode 返回发送方式，供保存投递记录使用。
	Mode() string
	// Send 向接收方提交事件，失败时返回错误。
	Send(context.Context, Event) error
}

// SimulationSender 模拟发送成功，便于测试队列处理；不请求外部服务。
type SimulationSender struct{}

// Mode 返回 simulation，供保存投递方式使用。
func (SimulationSender) Mode() string { return "simulation" }

// Send 模拟发送成功，不执行网络请求。
func (SimulationSender) Send(context.Context, Event) error { return nil }

// HTTPSender 提供通用 HTTP 发送能力；实际监管接口所需的字段、签名和国密算法须按其规范另行实现。
type HTTPSender struct {
	Endpoint string
	Secret   string
	Client   *http.Client
}

// Mode 返回 http，供保存投递方式使用。
func (HTTPSender) Mode() string { return "http" }

// Send 校验公网 HTTPS 地址，签名并 POST 事件 JSON；非 2xx 回复视为失败。
// 未配置客户端时使用 10 秒超时并禁止重定向。
func (s HTTPSender) Send(ctx context.Context, event Event) error {
	if s.Secret == "" {
		return errors.New("regulatory signing secret missing")
	}
	if err := netguard.ValidatePublicHTTPS(s.Endpoint); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(s.Secret))
	_, _ = mac.Write([]byte(ts + "."))
	_, _ = mac.Write(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-ChargePilot-Event-Id", event.EventID)
	request.Header.Set("X-ChargePilot-Timestamp", ts)
	request.Header.Set("X-ChargePilot-Signature", hex.EncodeToString(mac.Sum(nil)))
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("regulatory receiver HTTP " + strconv.Itoa(response.StatusCode))
	}
	return nil
}
