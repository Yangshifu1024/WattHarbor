package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

type Gateway struct {
	HTTPAddr           string `env:"GATEWAY_HTTP_ADDR" envDefault:":8083"`
	DC589Addr          string `env:"DC589_ADDR" envDefault:":9100"`
	DatabaseURL        string `env:"DATABASE_URL,required"`
	ServiceToken       string `env:"SERVICE_TOKEN,required"`
	CentralInternalURL string `env:"CENTRAL_INTERNAL_URL" envDefault:"http://central:8082"`
	MaxConnections     int    `env:"DEVICE_MAX_CONNECTIONS" envDefault:"10000"`
	DebugHeartbeat     bool   `env:"GATEWAY_DEBUG_HEARTBEAT" envDefault:"false"`
}

// Central 是对内进程的配置。它不签发也不校验用户会话——internal 端点一律走
// 服务令牌，因此不取 JWT_SECRET：密钥只留在真正需要它的 user 与 admin 两个对外进程。
//
// 它仍需要微信 AppID 与商户凭据，因为退款派发由 worker 回调本进程执行。
type Central struct {
	DatabaseURL            string `env:"DATABASE_URL_CENTRAL,required"`
	AdminBootstrapUser     string `env:"ADMIN_BOOTSTRAP_USER"`
	AdminBootstrapPassword string `env:"ADMIN_BOOTSTRAP_PASSWORD"`
	HTTPAddr               string `env:"CENTRAL_HTTP_ADDR" envDefault:":8082"`
	RedisCacheURL          string `env:"REDIS_CACHE_URL,required"`
	RedisStreamURL         string `env:"REDIS_STREAM_URL,required"`
	ServiceToken           string `env:"SERVICE_TOKEN,required"`
	GatewayInternalURL     string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	WeChatAppID            string `env:"WECHAT_APPID,required"`
	WeChatAppSecret        string `env:"WECHAT_SECRET,required"`
	PaymentMode            string `env:"PAYMENT_MODE" envDefault:"disabled"`
	LoginMode              string `env:"LOGIN_MODE" envDefault:"wechat"`
	WechatMchID            string `env:"WECHAT_MCH_ID"`
	WechatCertSerial       string `env:"WECHAT_CERT_SERIAL"`
	WechatAPIv3Key         string `env:"WECHAT_APIV3_KEY"`
	WechatPrivateKey       string `env:"WECHAT_PRIVATE_KEY_PATH"`
	WechatNotifyURL        string `env:"WECHAT_NOTIFY_URL"`
}

// User 是 C 端进程的配置。它取微信与支付凭据（登录、手机号授权、支付回调都在
// 这一侧），但不取 Redis 流——事件流由 worker 消费，不经过对外进程。
//
// 充电中的遥测与历史曲线经 GATEWAY_INTERNAL_URL 回调 gateway，不经 central。
type User struct {
	DatabaseURL        string `env:"DATABASE_URL_CENTRAL,required"`
	HTTPAddr           string `env:"USER_HTTP_ADDR" envDefault:":8080"`
	RedisCacheURL      string `env:"REDIS_CACHE_URL,required"`
	JWTSecret          string `env:"JWT_SECRET,required"`
	ServiceToken       string `env:"SERVICE_TOKEN,required"`
	GatewayInternalURL string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	WeChatAppID        string `env:"WECHAT_APPID,required"`
	WeChatAppSecret    string `env:"WECHAT_SECRET,required"`
	PaymentMode        string `env:"PAYMENT_MODE" envDefault:"disabled"`
	LoginMode          string `env:"LOGIN_MODE" envDefault:"wechat"`
	WechatMchID        string `env:"WECHAT_MCH_ID"`
	WechatCertSerial   string `env:"WECHAT_CERT_SERIAL"`
	WechatAPIv3Key     string `env:"WECHAT_APIV3_KEY"`
	WechatPrivateKey   string `env:"WECHAT_PRIVATE_KEY_PATH"`
	WechatNotifyURL    string `env:"WECHAT_NOTIFY_URL"`
}

// Admin 是运营后台进程的配置。
//
// 与 Central 共用同一个 central_db，但不碰微信与支付配置——后台不发起支付，
// 也不替用户登录。分开定义而不是复用 Central，是为了让"这个进程需要什么
// 凭据"在类型上就是清楚的：多给一份 WECHAT_SECRET 就等于多一处泄露面。
type Admin struct {
	DatabaseURL            string `env:"DATABASE_URL_CENTRAL,required"`
	HTTPAddr               string `env:"ADMIN_HTTP_ADDR" envDefault:":8081"`
	RedisCacheURL          string `env:"REDIS_CACHE_URL,required"`
	JWTSecret              string `env:"JWT_SECRET,required"`
	ServiceToken           string `env:"SERVICE_TOKEN,required"`
	AdminBootstrapUser     string `env:"ADMIN_BOOTSTRAP_USER"`
	AdminBootstrapPassword string `env:"ADMIN_BOOTSTRAP_PASSWORD"`
	GatewayInternalURL     string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	ExportDir              string `env:"EXPORT_DIR"`
}

type Worker struct {
	CentralDatabaseURL string `env:"DATABASE_URL_CENTRAL,required"`
	HTTPAddr           string `env:"WORKER_HTTP_ADDR" envDefault:":8085"`
	DatabaseURL        string `env:"DATABASE_URL_WORKER,required"`
	GatewayDatabaseURL string `env:"DATABASE_URL_GATEWAY,required"`
	RedisStreamURL     string `env:"REDIS_STREAM_URL,required"`
	ServiceToken       string `env:"SERVICE_TOKEN,required"`
	CentralInternalURL string `env:"CENTRAL_INTERNAL_URL" envDefault:"http://central:8082"`
	GatewayInternalURL string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	RegulatoryMode     string `env:"REGULATORY_MODE" envDefault:"disabled"`
	RegulatoryEndpoint string `env:"REGULATORY_ENDPOINT"`
	RegulatorySecret   string `env:"REGULATORY_SIGNING_SECRET"`
}

func LoadGateway() (Gateway, error) {
	var c Gateway
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("gateway config: %w", err)
	}
	if c.MaxConnections < 1 {
		return c, fmt.Errorf("gateway config: DEVICE_MAX_CONNECTIONS must be positive")
	}
	return c, nil
}

func LoadCentral() (Central, error) {
	var c Central
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("central config: %w", err)
	}
	if c.PaymentMode != "disabled" && c.PaymentMode != "simulation" && c.PaymentMode != "wechat_direct" {
		return c, fmt.Errorf("central config: invalid PAYMENT_MODE")
	}
	if c.LoginMode != "wechat" && c.LoginMode != "development" {
		return c, fmt.Errorf("central config: invalid LOGIN_MODE")
	}
	if c.LoginMode == "development" && c.PaymentMode != "simulation" {
		return c, fmt.Errorf("development login requires PAYMENT_MODE=simulation")
	}
	if c.PaymentMode == "wechat_direct" && (c.WechatMchID == "" || c.WechatCertSerial == "" || len(c.WechatAPIv3Key) != 32 || c.WechatPrivateKey == "" || c.WechatNotifyURL == "") {
		return c, fmt.Errorf("central config: incomplete WeChat Pay credentials")
	}
	return c, nil
}

func LoadWorker() (Worker, error) {
	var c Worker
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("worker config: %w", err)
	}
	if c.RegulatoryMode != "disabled" && c.RegulatoryMode != "simulation" && c.RegulatoryMode != "http" {
		return c, fmt.Errorf("worker config: invalid REGULATORY_MODE")
	}
	if c.RegulatoryMode == "http" && (c.RegulatoryEndpoint == "" || len(c.RegulatorySecret) < 16) {
		return c, fmt.Errorf("worker config: REGULATORY_ENDPOINT and REGULATORY_SIGNING_SECRET required for http mode")
	}
	return c, nil
}

func LoadAdmin() (Admin, error) {
	var c Admin
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("admin config: %w", err)
	}
	if len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("admin config: JWT_SECRET must have at least 32 bytes")
	}
	return c, nil
}

func LoadUser() (User, error) {
	var c User
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("user config: %w", err)
	}
	if len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("user config: JWT_SECRET must have at least 32 bytes")
	}
	switch c.PaymentMode {
	case "disabled", "simulation", "wechat_direct":
	default:
		return c, fmt.Errorf("user config: invalid PAYMENT_MODE")
	}
	switch c.LoginMode {
	case "wechat", "development":
	default:
		return c, fmt.Errorf("user config: invalid LOGIN_MODE")
	}
	if c.LoginMode == "development" && c.PaymentMode != "simulation" {
		return c, fmt.Errorf("user config: development login requires PAYMENT_MODE=simulation")
	}
	if c.PaymentMode == "wechat_direct" && (c.WechatMchID == "" || c.WechatCertSerial == "" || len(c.WechatAPIv3Key) != 32 || c.WechatPrivateKey == "" || c.WechatNotifyURL == "") {
		return c, fmt.Errorf("user config: incomplete WeChat Pay credentials")
	}
	return c, nil
}
