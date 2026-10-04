package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var empty T
	status, body, err := c.transport.GetStatus(ctx, c.baseURL, c.token, path)
	if err != nil {
		return empty, err
	}
	if status != http.StatusOK {
		return empty, fmt.Errorf("%w: HTTP %d, want 200", ErrInvalidResponse, status)
	}
	var reply response[T]
	if err := json.Unmarshal(body, &reply); err != nil {
		return empty, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	if reply.Code == nil {
		return empty, fmt.Errorf("%w: response has no business code", ErrInvalidResponse)
	}
	if *reply.Code != 0 {
		return empty, BusinessError{Code: *reply.Code}
	}
	if reply.Data == nil {
		return empty, fmt.Errorf("%w: response has no data", ErrInvalidResponse)
	}
	return *reply.Data, nil
}

func (c *Client) StartResults(ctx context.Context, commandID string) ([]gatewaycontract.SyncCommand, error) {
	path := gatewaycontract.StartResultsPath
	if commandID != "" {
		path += "?command_id=" + url.QueryEscape(commandID)
	}
	data, err := get[gatewaycontract.StartResultsData](ctx, c, path)
	if err != nil {
		return nil, err
	}
	if data.Items == nil {
		return nil, fmt.Errorf("%w: start results has no items", ErrInvalidResponse)
	}
	return data.Items, nil
}

func (c *Client) EndEvents(ctx context.Context) ([]gatewaycontract.SyncEvent, error) {
	data, err := get[gatewaycontract.EventsData](ctx, c, gatewaycontract.EndEventsPath)
	if err != nil {
		return nil, err
	}
	if data.Items == nil {
		return nil, fmt.Errorf("%w: end events has no items", ErrInvalidResponse)
	}
	return data.Items, nil
}

func (c *Client) ChargeCommand(ctx context.Context, orderID uint64) (*gatewaycontract.SyncCommand, error) {
	path := strings.Replace(gatewaycontract.ChargeCommandRoute, ":order_id", strconv.FormatUint(orderID, 10), 1)
	data, err := get[gatewaycontract.CommandData](ctx, c, path)
	if err != nil {
		return nil, err
	}
	if !data.Found {
		return nil, nil
	}
	if data.Command == nil || data.Command.ChargeOrderID != orderID {
		return nil, fmt.Errorf("%w: command identity mismatch", ErrInvalidResponse)
	}
	return data.Command, nil
}

func mark(ctx context.Context, c *Client, path string, payload any, requested int) (int, error) {
	data, err := post[struct {
		Marked *int `json:"marked"`
	}](ctx, c, path, payload, http.StatusOK)
	if err != nil {
		return 0, err
	}
	if data.Marked == nil || *data.Marked < 0 || *data.Marked > requested {
		return 0, fmt.Errorf("%w: missing or invalid mark count", ErrInvalidResponse)
	}
	return *data.Marked, nil
}

func (c *Client) MarkStartResultsReported(ctx context.Context, ids []string) (int, error) {
	return mark(ctx, c, gatewaycontract.MarkStartResultsPath, gatewaycontract.MarkStartResultsRequest{CommandIDs: ids}, len(ids))
}
func (c *Client) MarkDeviceEventsProcessed(ctx context.Context, ids []uint64) (int, error) {
	return mark(ctx, c, gatewaycontract.MarkDeviceEventsPath, gatewaycontract.MarkDeviceEventsRequest{IDs: ids}, len(ids))
}

func (c *Client) ReleasePort(ctx context.Context, request gatewaycontract.ReleasePortRequest) (gatewaycontract.ReleasePortResult, error) {
	data, err := post[gatewaycontract.ReleasePortResult](ctx, c, gatewaycontract.ReleasePortPath, request, http.StatusOK)
	if err != nil {
		return data, err
	}
	if !data.Released {
		return gatewaycontract.ReleasePortResult{}, fmt.Errorf("%w: port not released", ErrInvalidResponse)
	}
	return data, nil
}

func (c *Client) MeterSamples(ctx context.Context, query gatewaycontract.MeterSamplesQuery) ([]gatewaycontract.SyncEvent, error) {
	path := strings.Replace(gatewaycontract.MeterSamplesRoute, ":device_id", url.PathEscape(query.DeviceID), 1)
	params := url.Values{"ack_at": {query.AckAt.UTC().Format(time.RFC3339Nano)}, "end_at": {query.EndAt.UTC().Format(time.RFC3339Nano)}, "end_id": {strconv.FormatUint(query.EndID, 10)}}
	data, err := get[gatewaycontract.EventsData](ctx, c, path+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	if data.Items == nil {
		return nil, fmt.Errorf("%w: meter samples has no items", ErrInvalidResponse)
	}
	return data.Items, nil
}

func (c *Client) FreezeEndDelivery(ctx context.Context, request gatewaycontract.FreezeEndDeliveryRequest) (gatewaycontract.FreezeEndDeliveryResult, error) {
	data, err := post[gatewaycontract.FreezeEndDeliveryResult](ctx, c, gatewaycontract.FreezeEndDeliveryPath, request, http.StatusOK)
	if err != nil {
		return data, err
	}
	if data.Payload == "" {
		return gatewaycontract.FreezeEndDeliveryResult{}, fmt.Errorf("%w: frozen payload missing", ErrInvalidResponse)
	}
	return data, nil
}

func (c *Client) CompensateStart(ctx context.Context, orderNo, commandID string) error {
	if orderNo == "" || len(orderNo) > 64 || strings.ContainsAny(orderNo, "/?#") {
		return errors.New("invalid charge order number")
	}
	path := strings.Replace(gatewaycontract.CompensateStartRoute, ":order_no", url.PathEscape(orderNo), 1)
	data, err := post[gatewaycontract.CompensationAcceptance](ctx, c, path, gatewaycontract.CompensateStartRequest{CommandID: commandID}, http.StatusOK)
	if err != nil {
		return err
	}
	if !data.Accepted {
		return fmt.Errorf("%w: compensation not accepted", ErrInvalidResponse)
	}
	return nil
}
