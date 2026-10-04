package charge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PaidStarter 重试已支付订单，直到 gateway 接受持久化 START 命令；重复调用由 gateway 保证幂等。
type PaidStarter struct {
	CentralURL   string
	GatewayURL   string
	ServiceToken string
	Client       *http.Client
	cursor       uint64
}

func (s *PaidStarter) DispatchBatch(ctx context.Context) (int, error) {
	if s == nil || s.CentralURL == "" || s.ServiceToken == "" {
		return 0, errors.New("paid start dispatcher is not configured")
	}
	base, err := url.Parse(s.GatewayURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "http" && base.Scheme != "https") {
		return 0, errors.New("invalid gateway URL")
	}
	var reply struct {
		Code *int `json:"code"`
		Data struct {
			Items []struct {
				ID      uint64 `json:"id"`
				OrderNo string `json:"order_no"`
			} `json:"items"`
		} `json:"data"`
	}
	err = (serviceclient.Client{HTTP: s.Client}).GetJSON(ctx, s.CentralURL, s.ServiceToken, fmt.Sprintf("/api/v1/internal/charge-orders/paid-starts?after_id=%d", s.cursor), &reply)
	rows := reply.Data.Items
	if err != nil {
		return 0, err
	}
	if reply.Code == nil || *reply.Code != 0 || rows == nil {
		return 0, errors.New("central paid start list rejected")
	}
	if len(rows) == 0 {
		s.cursor = 0
		return 0, nil
	}
	previous := s.cursor
	for _, row := range rows {
		if row.ID <= previous || row.OrderNo == "" {
			return 0, errors.New("central paid start cursor mismatch")
		}
		previous = row.ID
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	accepted := 0
	var firstError error
	for _, row := range rows {
		s.cursor = row.ID
		if err := postPaidStart(ctx, client, *base, s.ServiceToken, row.OrderNo); err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		accepted++
	}
	return accepted, firstError
}

func postPaidStart(ctx context.Context, client *http.Client, base url.URL, token, orderNo string) error {
	if orderNo == "" {
		return errors.New("empty paid charge order number")
	}
	data, err := json.Marshal(map[string]string{"order_no": orderNo})
	if err != nil {
		return err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/start"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("gateway paid start: HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			OrderNo string `json:"order_no"`
			Status  string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Code != 0 || envelope.Data.OrderNo != orderNo || envelope.Data.Status == "" {
		return errors.New("gateway paid start response mismatch")
	}
	return nil
}
