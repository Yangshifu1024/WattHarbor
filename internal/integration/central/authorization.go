package central

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

func orderPath(route, orderNo string) (string, error) {
	if orderNo == "" || len(orderNo) > 64 || strings.ContainsAny(orderNo, "/?#") {
		return "", errors.New("invalid charge order number")
	}
	return strings.Replace(route, ":order_no", url.PathEscape(orderNo), 1), nil
}

func authorizationError(err error) error {
	var rejection *serviceclient.HTTPError
	if errors.As(err, &rejection) && (rejection.StatusCode == http.StatusNotFound || rejection.StatusCode == http.StatusConflict) {
		return errors.Join(centralcontract.ErrAuthorizationDenied, err)
	}
	return err
}

func (c *Client) StartAuthorization(ctx context.Context, orderNo string) (centralcontract.StartAuthorizationReply, error) {
	path, err := orderPath(centralcontract.StartAuthorizationRoute, orderNo)
	if err != nil {
		return centralcontract.StartAuthorizationReply{}, errors.Join(centralcontract.ErrAuthorizationDenied, err)
	}
	data, err := get[centralcontract.StartAuthorizationReply](ctx, c, path)
	if err != nil {
		return data, authorizationError(err)
	}
	if data.OrderNo != orderNo || data.ChargeOrderID == 0 {
		return centralcontract.StartAuthorizationReply{}, fmt.Errorf("%w: start identity mismatch", centralcontract.ErrAuthorizationDenied)
	}
	return data, nil
}

func (c *Client) StopAuthorization(ctx context.Context, orderNo string, userID uint64) (centralcontract.StopAuthorizationReply, error) {
	path, err := orderPath(centralcontract.StopAuthorizationRoute, orderNo)
	if err != nil || userID == 0 {
		return centralcontract.StopAuthorizationReply{}, centralcontract.ErrAuthorizationDenied
	}
	path += "?user_id=" + strconv.FormatUint(userID, 10)
	data, err := get[centralcontract.StopAuthorizationReply](ctx, c, path)
	if err != nil {
		return data, authorizationError(err)
	}
	if data.OrderNo != orderNo || data.UserID != userID || data.ChargeOrderID == 0 {
		return centralcontract.StopAuthorizationReply{}, fmt.Errorf("%w: stop identity mismatch", centralcontract.ErrAuthorizationDenied)
	}
	return data, nil
}
