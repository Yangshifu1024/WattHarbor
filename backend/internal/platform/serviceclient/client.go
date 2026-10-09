// Package serviceclient 统一服务间 HTTP 调用的超时、服务令牌及错误处理。
package serviceclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthorized 表示服务返回 HTTP 401 或 403。
var ErrUnauthorized = errors.New("service token rejected")

// ErrNotFound 表示被调用方有应答，但资源不存在。
var ErrNotFound = errors.New("resource not found")

// HTTPError 保留已收到的非成功 HTTP 状态；不包含响应正文或服务凭据。
type HTTPError struct {
	StatusCode int
	Path       string
}

// Error 返回调用路径和 HTTP 状态码。
func (e *HTTPError) Error() string {
	return fmt.Sprintf("service call %s returned %d", e.Path, e.StatusCode)
}

// Is 将 HTTP 401、403 归为 ErrUnauthorized，404 归为 ErrNotFound。
func (e *HTTPError) Is(target error) bool {
	return (target == ErrUnauthorized && (e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden)) ||
		(target == ErrNotFound && e.StatusCode == http.StatusNotFound)
}

// Client 用 X-Service-Token 请求头调用内部服务。
type Client struct {
	HTTP    *http.Client
	Timeout time.Duration
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// Get 发送带服务令牌的 GET 并返回响应体；404 映射为 ErrNotFound，其他失败独立返回。
func (c Client) Get(ctx context.Context, baseURL, serviceToken, path string) ([]byte, error) {
	_, body, err := c.GetStatus(ctx, baseURL, serviceToken, path)
	return body, err
}

// GetStatus 返回 HTTP 状态供专用客户端核对；未收到响应时状态为 0。
func (c Client) GetStatus(ctx context.Context, baseURL, serviceToken, path string) (int, []byte, error) {
	target := strings.TrimRight(baseURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("X-Service-Token", serviceToken)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// 读取并丢弃最多 4 MiB 错误正文，尽量复用连接；读取失败仍返回 HTTP 错误。
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<20))
		return response.StatusCode, nil, &HTTPError{StatusCode: response.StatusCode, Path: path}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, body, nil
}

// GetJSON 将成功响应解码到 out；out 为 nil 时只检查请求是否成功。
func (c Client) GetJSON(ctx context.Context, baseURL, serviceToken, path string, out any) error {
	body, err := c.Get(ctx, baseURL, serviceToken, path)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// Post 发出一个带鉴权的 JSON POST，并把应答解码进 out。
func (c Client) Post(ctx context.Context, baseURL, serviceToken, path string, payload any, out any) error {
	_, err := c.PostStatus(ctx, baseURL, serviceToken, path, payload, out)
	return err
}

// PostStatus 发送 JSON POST，返回 HTTP 状态并将成功响应解码到 out。
// 请求未收到响应时状态码为 0；收到响应后的读取、解码或服务错误保留状态码。
func (c Client) PostStatus(ctx context.Context, baseURL, serviceToken, path string, payload any, out any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	target := strings.TrimRight(baseURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", serviceToken)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<20))
		return response.StatusCode, &HTTPError{StatusCode: response.StatusCode, Path: path}
	}
	reply, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return response.StatusCode, err
	}
	if out == nil || len(reply) == 0 {
		return response.StatusCode, nil
	}
	return response.StatusCode, json.Unmarshal(reply, out)
}
