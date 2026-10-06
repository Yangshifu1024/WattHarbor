package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
)

// routeModeChannel 只提供真实装配要求的渠道接口，不执行预支付、验签或数据库操作。
type routeModeChannel struct{}

// Prepay 在意外调用时忽略ctx及request并返回空参数和固定错误，避免将路由检查当作支付证据。
func (routeModeChannel) Prepay(context.Context, payment.PrepayRequest) (payment.PrepayParams, error) {
	return payment.PrepayParams{}, errors.New("route inventory must not execute payment")
}

// VerifyNotification 在意外调用时忽略ctx及request并返回空交易和固定错误，不验签或持久到账。
func (routeModeChannel) VerifyNotification(context.Context, *http.Request) (payment.VerifiedTransaction, error) {
	return payment.VerifiedTransaction{}, errors.New("route inventory must not verify notification")
}

// userRouteInventory 返回mode的真实cmd装配完整方法／路径排序集合，不发送HTTP请求或打开资源。
// 健康、指标和按模式注册的支付入口也纳入；窄渠道fixture仅满足构造接口，不证明支付执行。
func userRouteInventory(mode string) []string {
	router := newRouter(config.User{LoginMode: "development", PaymentMode: mode}, &resources{}, nil, identity.MiniProgram{}, payment.PaymentChannel{Prepay: routeModeChannel{}}, nil)
	routes := make([]string, 0, len(router.Routes()))
	for _, route := range router.Routes() {
		routes = append(routes, route.Method+" "+route.Path)
	}
	slices.Sort(routes)
	return routes
}

// TestUserAssemblyKeepsAllRoutesAndModeSwitches 用t对照迁移前真实装配的完整路由快照。
// disabled、simulation、wechat_direct分别核对，不只检查OpenAPI的子集；无业务IO或真实渠道调用。
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
