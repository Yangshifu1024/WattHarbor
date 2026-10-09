package central

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/serviceclient"
)

// orderPath 将订单号转义后填入路径；拒绝空值、超长值及含 /?# 的订单号。
func orderPath(route, orderNo string) (string, error) {
	if orderNo == "" || len(orderNo) > 64 || strings.ContainsAny(orderNo, "/?#") {
		return "", errors.New("invalid charge order number")
	}
	return strings.Replace(route, ":order_no", url.PathEscape(orderNo), 1), nil
}

// authorizationError 将 HTTP 404 和 409 标记为授权拒绝，并保留底层错误。
func authorizationError(err error) error {
	var rejection *serviceclient.HTTPError
	if errors.As(err, &rejection) && (rejection.StatusCode == http.StatusNotFound || rejection.StatusCode == http.StatusConflict) {
		return errors.Join(centralcontract.ErrAuthorizationDenied, err)
	}
	return err
}

// StartAuthorization 查询订单是否允许启动，并核对响应中的订单号和订单 ID。
// 只查询授权，不发送设备命令或修改订单。
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

// StopAuthorization 查询用户是否允许停止订单，并核对订单号、用户和订单 ID。
// 用户 ID 保持整数精度；central 同时保存首次计费截止点，本客户端不发送停机命令。
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
