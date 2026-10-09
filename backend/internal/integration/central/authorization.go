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

// orderPath 接收 route 与 orderNo，拒绝空、超长及含 /?# 的订单号，返回替换首个路径占位符后的 URL 编码路径或输入错误；无网络 IO。
func orderPath(route, orderNo string) (string, error) {
	if orderNo == "" || len(orderNo) > 64 || strings.ContainsAny(orderNo, "/?#") {
		return "", errors.New("invalid charge order number")
	}
	return strings.Replace(route, ":order_no", url.PathEscape(orderNo), 1), nil
}

// authorizationError 接收 err，将真实 HTTP 404／409 与 ErrAuthorizationDenied 合并，其余错误原样返回，保留底层分类；不重发、补偿或修改业务状态。
func authorizationError(err error) error {
	var rejection *serviceclient.HTTPError
	if errors.As(err, &rejection) && (rejection.StatusCode == http.StatusNotFound || rejection.StatusCode == http.StatusConflict) {
		return errors.Join(centralcontract.ErrAuthorizationDenied, err)
	}
	return err
}

// StartAuthorization 在 ctx 下以 orderNo 查询中央启动授权，返回原 Reply 或输入、准入、身份失配、HTTP／业务／解码／取消错误。
// 仅核对回复订单号与非零订单 ID；不发 START、不分配新命令身份、不改订单或资金。
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

// StopAuthorization 在 ctx 下以 orderNo 和非零 userID 查询中央停机授权，保留用户大整数参数，返回原 Reply 或准入、身份失配及传输／响应错误。
// 核对回复订单号、用户及非零订单 ID；不发送 STOP、不释放端口，失配不自动补偿。
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
