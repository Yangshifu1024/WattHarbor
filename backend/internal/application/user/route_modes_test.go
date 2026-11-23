package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"slices"
	"testing"

	"chargepilot/internal/identity"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/config"
)

// routeModeChannel 只提供真实装配要求的渠道接口，不执行预支付、验签或数据库操作。
type routeModeChannel struct{}

// Prepay 返回固定错误，防止路由测试意外执行支付。
func (routeModeChannel) Prepay(context.Context, payment.PrepayRequest) (payment.PrepayParams, error) {
	return payment.PrepayParams{}, errors.New("route inventory must not execute payment")
}

// VerifyNotification 返回固定错误，不处理支付通知。
func (routeModeChannel) VerifyNotification(context.Context, *http.Request) (payment.VerifiedTransaction, error) {
	return payment.VerifiedTransaction{}, errors.New("route inventory must not verify notification")
}

// userRouteInventory 返回指定模式下注册的全部方法和路径，包含健康检查、指标和支付入口。
func userRouteInventory(mode string) []string {
	router := newRouter(config.User{LoginMode: "development", PaymentMode: mode}, &resources{}, nil, identity.MiniProgram{}, payment.PaymentChannel{Prepay: routeModeChannel{}}, nil)
	routes := make([]string, 0, len(router.Routes()))
	for _, route := range router.Routes() {
		routes = append(routes, route.Method+" "+route.Path)
	}
	slices.Sort(routes)
	return routes
}

// TestUserAssemblyKeepsAllRoutesAndModeSwitches 核对 disabled、simulation、wechat_direct 三种模式的完整路由。
func TestUserAssemblyKeepsAllRoutesAndModeSwitches(t *testing.T) {
	data, err := os.ReadFile("testdata/routes-before-user-split.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string][]string
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"disabled", "simulation", "wechat_direct"} {
		t.Run(mode, func(t *testing.T) {
			actual := userRouteInventory(mode)
			if !reflect.DeepEqual(actual, expected[mode]) {
				t.Fatalf("route inventory changed: got=%v want=%v", actual, expected[mode])
			}
		})
	}
}
