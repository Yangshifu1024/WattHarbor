package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// clearConfigEnvironment 隔离 t 中所有服务配置变量，清理时由 t.Setenv 恢复调用前的值。
// 删除变量而非设置空串，使 required 标签实际检验缺失配置；不读取或输出已有凭据。
func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME", "DB_CONN_MAX_IDLE_TIME",
		"DATABASE_URL", "DATABASE_URL_CENTRAL", "DATABASE_URL_GATEWAY", "DATABASE_URL_WORKER",
		"GATEWAY_HTTP_ADDR", "DC589_ADDR", "DEVICE_MAX_CONNECTIONS", "GATEWAY_DEBUG_HEARTBEAT",
		"CENTRAL_HTTP_ADDR", "USER_HTTP_ADDR", "ADMIN_HTTP_ADDR", "WORKER_HTTP_ADDR",
		"REDIS_CACHE_URL", "REDIS_STREAM_URL", "JWT_SECRET", "SERVICE_TOKEN",
		"CENTRAL_INTERNAL_URL", "GATEWAY_INTERNAL_URL", "WECHAT_APPID", "WECHAT_SECRET",
		"PAYMENT_MODE", "LOGIN_MODE", "WECHAT_MCH_ID", "WECHAT_CERT_SERIAL", "WECHAT_APIV3_KEY",
		"WECHAT_PRIVATE_KEY_PATH", "WECHAT_NOTIFY_URL", "ADMIN_BOOTSTRAP_USER", "ADMIN_BOOTSTRAP_PASSWORD", "EXPORT_DIR",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
}

// centralEnvironment 为 t 设置 central 的必填基础变量；使用测试占位值，不连接数据库或 Redis。
func centralEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL_CENTRAL", "mysql://fixture:fixture@localhost/central_db")
	t.Setenv("REDIS_CACHE_URL", "redis://localhost:6379/0")
	t.Setenv("SERVICE_TOKEN", "fixture-service-token")
	t.Setenv("WECHAT_APPID", "wx_fixture")
}

// wechatPaymentEnvironment 为 t 提供通过原直连配置校验的商户参数，不读取私钥或初始化渠道。
func wechatPaymentEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("PAYMENT_MODE", "wechat_direct")
	t.Setenv("WECHAT_MCH_ID", "fixture-merchant")
	t.Setenv("WECHAT_CERT_SERIAL", "fixture-certificate")
	t.Setenv("WECHAT_APIV3_KEY", strings.Repeat("k", 32))
	t.Setenv("WECHAT_PRIVATE_KEY_PATH", "/run/secrets/fixture.pem")
	t.Setenv("WECHAT_NOTIFY_URL", "https://fixture.example/api/v1/public/payments/wechat/callback")
}

// TestLoadGatewayNeedsNoLoginOrRedis 验证设备网关加载只需自己的数据库与服务令牌，并保持原监听默认值。
func TestLoadGatewayNeedsNoLoginOrRedis(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("DATABASE_URL", "mysql://fixture:fixture@localhost/gateway_db")
	t.Setenv("SERVICE_TOKEN", "fixture-service-token")
	c, err := LoadGateway()
	if err != nil || c.HTTPAddr != ":8083" || c.DC589Addr != ":9100" || c.MaxConnections != 10000 {
		t.Fatalf("gateway defaults or loading changed: err=%v http=%q tcp=%q limit=%d", err, c.HTTPAddr, c.DC589Addr, c.MaxConnections)
	}
}

// TestLoadCentralNeedsNoUserCredentialsOrStream 验证 central 无登录密钥、JWT、事件流与后台引导参数仍可加载。
// 非法用户登录模式对 central 无效；支付默认为 disabled，真实使用的字段仍完整解析。
func TestLoadCentralNeedsNoUserCredentialsOrStream(t *testing.T) {
	clearConfigEnvironment(t)
	centralEnvironment(t)
	t.Setenv("LOGIN_MODE", "invalid-user-login-mode")
	c, err := LoadCentral()
	if err != nil {
		t.Fatalf("central should ignore user-only configuration: %v", err)
	}
	if c.DatabaseURL != "mysql://fixture:fixture@localhost/central_db" || c.RedisCacheURL != "redis://localhost:6379/0" || c.ServiceToken != "fixture-service-token" || c.WeChatAppID != "wx_fixture" || c.PaymentMode != "disabled" || c.HTTPAddr != ":8082" || c.GatewayInternalURL != "http://gateway:8083" {
		t.Fatal("central required values or defaults changed")
	}
}

// TestLoadCentralIgnoresUserLoginMode 验证遗留部署环境中的用户登录模式不参与 central 校验。
// 同时提供旧版必填的密钥与事件流占位值，使原实现的失败明确来自登录校验而非缺失变量。
func TestLoadCentralIgnoresUserLoginMode(t *testing.T) {
	for _, mode := range []string{"invalid-user-login-mode", "development"} {
		t.Run(mode, func(t *testing.T) {
			clearConfigEnvironment(t)
			centralEnvironment(t)
			t.Setenv("WECHAT_SECRET", "unused-fixture-secret")
			t.Setenv("REDIS_STREAM_URL", "unused-fixture-stream")
			t.Setenv("LOGIN_MODE", mode)
			t.Setenv("PAYMENT_MODE", "disabled")
			c, err := LoadCentral()
			if err != nil || c.PaymentMode != "disabled" {
				t.Fatalf("central must ignore user login mode, error=%v", err)
			}
		})
	}
}

// TestLoadCentralKeepsRequiredInputs 验证移除无消费者字段后，实际使用的四项基础配置仍为必填。
func TestLoadCentralKeepsRequiredInputs(t *testing.T) {
	for _, key := range []string{"DATABASE_URL_CENTRAL", "REDIS_CACHE_URL", "SERVICE_TOKEN", "WECHAT_APPID"} {
		t.Run(key, func(t *testing.T) {
			clearConfigEnvironment(t)
			centralEnvironment(t)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCentral(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("central missing %s error=%v", key, err)
			}
		})
	}
}

// TestLoadCentralKeepsPaymentValidation 验证 central 保持原支付模式、五项商户参数和 APIv3 密钥长度校验。
// 测试仅执行配置加载，直连成功用例不代表私钥文件可读或微信渠道已验收。
func TestLoadCentralKeepsPaymentValidation(t *testing.T) {
	tests := []struct {
		name      string // 子测试名称，标识所验证的配置边界。
		mode      string // 设置的支付模式；空值表示未配置，沿用默认 disabled。
		wechat    bool   // 是否先提供全部通过原校验的直连参数。
		missing   string // 待移除的商户参数名称；空值表示不移除。
		shortKey  bool   // 是否将 APIv3 密钥改为 31 字节，验证原长度约束。
		wantError string // 预期错误文本片段；空值表示应成功加载。
	}{
		{name: "disabled_default"},
		{name: "simulation", mode: "simulation"},
		{name: "invalid_mode", mode: "unknown", wantError: "central config: invalid PAYMENT_MODE"},
		{name: "missing_all_wechat", mode: "wechat_direct", wantError: "central config: incomplete WeChat Pay credentials"},
		{name: "complete_wechat", wechat: true},
		{name: "missing_merchant", wechat: true, missing: "WECHAT_MCH_ID", wantError: "incomplete WeChat Pay credentials"},
		{name: "missing_serial", wechat: true, missing: "WECHAT_CERT_SERIAL", wantError: "incomplete WeChat Pay credentials"},
		{name: "missing_key", wechat: true, missing: "WECHAT_APIV3_KEY", wantError: "incomplete WeChat Pay credentials"},
		{name: "short_key", wechat: true, shortKey: true, wantError: "incomplete WeChat Pay credentials"},
		{name: "missing_private_key", wechat: true, missing: "WECHAT_PRIVATE_KEY_PATH", wantError: "incomplete WeChat Pay credentials"},
		{name: "missing_notify", wechat: true, missing: "WECHAT_NOTIFY_URL", wantError: "incomplete WeChat Pay credentials"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			centralEnvironment(t)
			if tt.mode != "" {
				t.Setenv("PAYMENT_MODE", tt.mode)
			}
			if tt.wechat {
				wechatPaymentEnvironment(t)
			}
			if tt.missing != "" {
				t.Setenv(tt.missing, "")
			}
			if tt.shortKey {
				t.Setenv("WECHAT_APIV3_KEY", strings.Repeat("k", 31))
			}
			c, err := LoadCentral()
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("LoadCentral error=%v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadCentral: %v", err)
			}
			wantMode := tt.mode
			if tt.wechat {
				wantMode = "wechat_direct"
			} else if wantMode == "" {
				wantMode = "disabled"
			}
			if c.PaymentMode != wantMode {
				t.Fatalf("payment mode=%q, want %q", c.PaymentMode, wantMode)
			}
		})
	}
}

// TestLoadWorkerNeedsOnlyItsOwnConfiguration 验证 worker 无用户登录、支付、JWT 与 Cache 参数仍加载自己的三库及事件流。
func TestLoadWorkerNeedsOnlyItsOwnConfiguration(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("DATABASE_URL_CENTRAL", "central-fixture")
	t.Setenv("DATABASE_URL_GATEWAY", "gateway-fixture")
	t.Setenv("DATABASE_URL_WORKER", "worker-fixture")
	t.Setenv("REDIS_STREAM_URL", "stream-fixture")
	t.Setenv("SERVICE_TOKEN", "fixture-service-token")
	c, err := LoadWorker()
	if err != nil || c.CentralDatabaseURL != "central-fixture" || c.GatewayDatabaseURL != "gateway-fixture" || c.DatabaseURL != "worker-fixture" || c.RedisStreamURL != "stream-fixture" || c.HTTPAddr != ":8085" {
		t.Fatal("worker own configuration or defaults changed")
	}
}

// TestLoadAdminNeedsNoPaymentOrStream 验证 admin 不依赖登录、支付或事件流配置，且保持 JWT 校验与后台引导字段。
func TestLoadAdminNeedsNoPaymentOrStream(t *testing.T) {
	clearConfigEnvironment(t)
	centralEnvironment(t)
	if err := os.Unsetenv("WECHAT_APPID"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_SECRET", strings.Repeat("j", 32))
	t.Setenv("ADMIN_BOOTSTRAP_USER", "fixture-admin")
	t.Setenv("ADMIN_BOOTSTRAP_PASSWORD", "fixture-password")
	c, err := LoadAdmin()
	if err != nil || c.HTTPAddr != ":8081" || c.AdminBootstrapUser != "fixture-admin" || c.AdminBootstrapPassword != "fixture-password" {
		t.Fatalf("admin own configuration changed: %v", err)
	}
	t.Setenv("JWT_SECRET", strings.Repeat("j", 31))
	if _, err := LoadAdmin(); err == nil || !strings.Contains(err.Error(), "JWT_SECRET must have at least 32 bytes") {
		t.Fatalf("admin short JWT error=%v", err)
	}
}

// TestLoadUserKeepsLoginSecretRequirement 验证 user 仍要求微信登录密钥，即使选择开发登录与模拟支付也不能缺失该配置。
// 提供密钥后能加载；非法登录模式仍被原校验拒绝。
func TestLoadUserKeepsLoginSecretRequirement(t *testing.T) {
	clearConfigEnvironment(t)
	centralEnvironment(t)
	t.Setenv("JWT_SECRET", strings.Repeat("j", 32))
	t.Setenv("LOGIN_MODE", "development")
	t.Setenv("PAYMENT_MODE", "simulation")
	if _, err := LoadUser(); err == nil || !strings.Contains(err.Error(), "WECHAT_SECRET") {
		t.Fatalf("user must still require login secret, error=%v", err)
	}
	t.Setenv("WECHAT_SECRET", "fixture-login-secret")
	c, err := LoadUser()
	if err != nil || c.LoginMode != "development" || c.PaymentMode != "simulation" || c.HTTPAddr != ":8080" {
		t.Fatalf("user valid configuration changed: %v", err)
	}
	t.Setenv("LOGIN_MODE", "unknown")
	if _, err := LoadUser(); err == nil || !strings.Contains(err.Error(), "invalid LOGIN_MODE") {
		t.Fatalf("user invalid login mode error=%v", err)
	}
}

func TestDatabasePoolEnvironment(t *testing.T) {
	clearConfigEnvironment(t)
	centralEnvironment(t)
	c, err := LoadCentral()
	if err != nil || c.MaxOpen != 20 || c.MaxIdle != 5 || c.Lifetime != 30*time.Minute || c.IdleTime != 5*time.Minute {
		t.Fatalf("pool defaults: %+v %v", c.DatabasePool, err)
	}
	t.Setenv("DB_MAX_OPEN_CONNS", "12")
	t.Setenv("DB_MAX_IDLE_CONNS", "3")
	t.Setenv("DB_CONN_MAX_LIFETIME", "12m")
	t.Setenv("DB_CONN_MAX_IDLE_TIME", "2m")
	c, err = LoadCentral()
	if err != nil || c.MaxOpen != 12 || c.MaxIdle != 3 || c.Lifetime != 12*time.Minute || c.IdleTime != 2*time.Minute {
		t.Fatalf("pool overrides: %+v %v", c.DatabasePool, err)
	}
}

func TestRejectInvalidDatabasePool(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DB_MAX_OPEN_CONNS", "-1"}, {"DB_MAX_IDLE_CONNS", "-1"},
		{"DB_CONN_MAX_LIFETIME", "-1m"}, {"DB_CONN_MAX_IDLE_TIME", "-1m"},
		{"DB_MAX_IDLE_CONNS", "21"}, {"DB_CONN_MAX_IDLE_TIME", "invalid"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			clearConfigEnvironment(t)
			centralEnvironment(t)
			t.Setenv(tc.key, tc.value)
			if _, err := LoadCentral(); err == nil {
				t.Fatal("invalid pool accepted")
			}
		})
	}
}
