package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

// Gateway 配置设备接入进程的监听、数据库及 central 授权客户端。
type Gateway struct {
	// HTTPAddr 本进程 HTTP 监听地址；未配置时使用对应服务的默认端口。
	HTTPAddr string `env:"GATEWAY_HTTP_ADDR" envDefault:":8083"`
	// DC589Addr DC589 TCP 监听地址，默认 :9100。
	DC589Addr string `env:"DC589_ADDR" envDefault:":9100"`
	// DatabaseURL 本进程所属业务数据库的 MySQL URL；加载时必须提供，连接由资源装配负责。
	DatabaseURL string `env:"DATABASE_URL,required"`
	// ServiceToken 服务间请求认证令牌；加载时必须提供，不得写入日志。
	ServiceToken string `env:"SERVICE_TOKEN,required"`
	// CentralInternalURL central 内部 HTTP 基址；默认使用容器网络中的 central:8082。
	CentralInternalURL string `env:"CENTRAL_INTERNAL_URL" envDefault:"http://central:8082"`
	// MaxConnections 设备 TCP 连接数量上限；默认 10000，必须为正整数。
	MaxConnections int `env:"DEVICE_MAX_CONNECTIONS" envDefault:"10000"`
	// DebugHeartbeat 是否输出设备心跳诊断日志；默认关闭。
	DebugHeartbeat bool `env:"GATEWAY_DEBUG_HEARTBEAT" envDefault:"false"`
}

// Central 是对内进程的配置。它不签发也不校验用户会话——internal 端点一律走
// 服务令牌，因此不取 JWT_SECRET：密钥只留在真正需要它的 user 与 admin 两个对外进程。
//
// 它仍需要微信 AppID 与商户凭据，因为退款派发由 worker 回调本进程执行。
type Central struct {
	// DatabaseURL 本进程所属业务数据库的 MySQL URL；加载时必须提供，连接由资源装配负责。
	DatabaseURL string `env:"DATABASE_URL_CENTRAL,required"`
	// HTTPAddr 本进程 HTTP 监听地址；未配置时使用对应服务的默认端口。
	HTTPAddr string `env:"CENTRAL_HTTP_ADDR" envDefault:":8082"`
	// RedisCacheURL central 健康检查使用的 Redis 缓存 URL；加载时必须提供。
	RedisCacheURL string `env:"REDIS_CACHE_URL,required"`
	// ServiceToken 服务间请求认证令牌；加载时必须提供，不得写入日志。
	ServiceToken string `env:"SERVICE_TOKEN,required"`
	// GatewayInternalURL gateway 内部 HTTP 基址；默认使用容器网络中的 gateway:8083。
	GatewayInternalURL string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	// WeChatAppID 微信应用 ID；支付渠道及模拟回调校验使用，加载时必须提供。
	WeChatAppID string `env:"WECHAT_APPID,required"`
	// PaymentMode 支付渠道模式：disabled、simulation 或 wechat_direct；默认 disabled。
	PaymentMode string `env:"PAYMENT_MODE" envDefault:"disabled"`
	// WechatMchID 微信支付商户号；wechat_direct 模式下不可为空。
	WechatMchID string `env:"WECHAT_MCH_ID"`
	// WechatCertSerial 微信支付商户证书序列号；wechat_direct 模式下不可为空。
	WechatCertSerial string `env:"WECHAT_CERT_SERIAL"`
	// WechatAPIv3Key 微信支付 APIv3 通知解密密钥；wechat_direct 模式要求长度为 32 字节。
	WechatAPIv3Key string `env:"WECHAT_APIV3_KEY"`
	// WechatPrivateKey 容器内可读的微信支付商户私钥文件路径；wechat_direct 模式下不可为空，渠道初始化负责读取。
	WechatPrivateKey string `env:"WECHAT_PRIVATE_KEY_PATH"`
	// WechatNotifyURL 微信支付通知 URL；wechat_direct 模式下不可为空。
	WechatNotifyURL string `env:"WECHAT_NOTIFY_URL"`
}

// User 是 C 端进程的配置。它取微信与支付凭据（登录、手机号授权、支付回调都在
// 这一侧），但不取 Redis 流——事件流由 worker 消费，不经过对外进程。
//
// 充电中的遥测与历史曲线经 GATEWAY_INTERNAL_URL 回调 gateway，不经 central。
type User struct {
	// DatabaseURL 本进程所属业务数据库的 MySQL URL；加载时必须提供，连接由资源装配负责。
	DatabaseURL string `env:"DATABASE_URL_CENTRAL,required"`
	// HTTPAddr 本进程 HTTP 监听地址；未配置时使用对应服务的默认端口。
	HTTPAddr string `env:"USER_HTTP_ADDR" envDefault:":8080"`
	// RedisCacheURL 用户或后台会话使用的 Redis 缓存 URL；加载时必须提供。
	RedisCacheURL string `env:"REDIS_CACHE_URL,required"`
	// JWTSecret 用户或后台会话签名密钥；加载时必须提供，且至少 32 字节。
	JWTSecret string `env:"JWT_SECRET,required"`
	// ServiceToken 服务间请求认证令牌；加载时必须提供，不得写入日志。
	ServiceToken string `env:"SERVICE_TOKEN,required"`
	// GatewayInternalURL gateway 内部 HTTP 基址；默认使用容器网络中的 gateway:8083。
	GatewayInternalURL string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	// WeChatAppID 微信登录、手机号授权、支付渠道及支付回调校验使用的应用 ID；加载时必须提供。
	WeChatAppID string `env:"WECHAT_APPID,required"`
	// WeChatAppSecret 微信登录及手机号授权使用的应用密钥；仅 user 使用，加载时必须提供。
	WeChatAppSecret string `env:"WECHAT_SECRET,required"`
	// PaymentMode 支付渠道模式：disabled、simulation 或 wechat_direct；默认 disabled。
	PaymentMode string `env:"PAYMENT_MODE" envDefault:"disabled"`
	// LoginMode 用户登录模式：wechat 或 development；默认 wechat，开发登录要求 simulation 支付模式。
	LoginMode string `env:"LOGIN_MODE" envDefault:"wechat"`
	// WechatMchID 微信支付商户号；wechat_direct 模式下不可为空。
	WechatMchID string `env:"WECHAT_MCH_ID"`
	// WechatCertSerial 微信支付商户证书序列号；wechat_direct 模式下不可为空。
	WechatCertSerial string `env:"WECHAT_CERT_SERIAL"`
	// WechatAPIv3Key 微信支付 APIv3 通知解密密钥；wechat_direct 模式要求长度为 32 字节。
	WechatAPIv3Key string `env:"WECHAT_APIV3_KEY"`
	// WechatPrivateKey 容器内可读的微信支付商户私钥文件路径；wechat_direct 模式下不可为空，渠道初始化负责读取。
	WechatPrivateKey string `env:"WECHAT_PRIVATE_KEY_PATH"`
	// WechatNotifyURL 微信支付通知 URL；wechat_direct 模式下不可为空。
	WechatNotifyURL string `env:"WECHAT_NOTIFY_URL"`
}

// Admin 是运营后台进程的配置。
//
// 与 Central 共用同一个 central_db，但不碰微信与支付配置——后台不发起支付，
// 也不替用户登录。分开定义而不是复用 Central，是为了让"这个进程需要什么
// 凭据"在类型上就是清楚的：多给一份 WECHAT_SECRET 就等于多一处泄露面。
type Admin struct {
	// DatabaseURL 本进程所属业务数据库的 MySQL URL；加载时必须提供，连接由资源装配负责。
	DatabaseURL string `env:"DATABASE_URL_CENTRAL,required"`
	// HTTPAddr 本进程 HTTP 监听地址；未配置时使用对应服务的默认端口。
	HTTPAddr string `env:"ADMIN_HTTP_ADDR" envDefault:":8081"`
	// RedisCacheURL 用户或后台会话使用的 Redis 缓存 URL；加载时必须提供。
	RedisCacheURL string `env:"REDIS_CACHE_URL,required"`
	// JWTSecret 用户或后台会话签名密钥；加载时必须提供，且至少 32 字节。
	JWTSecret string `env:"JWT_SECRET,required"`
	// ServiceToken 服务间请求认证令牌；加载时必须提供，不得写入日志。
	ServiceToken string `env:"SERVICE_TOKEN,required"`
	// AdminBootstrapUser 首次初始化后台账户时使用的用户名；与密码同时为空时跳过引导。
	AdminBootstrapUser string `env:"ADMIN_BOOTSTRAP_USER"`
	// AdminBootstrapPassword 首次初始化后台账户时使用的明文密码；仅装配时交给 Bootstrap 哈希，不得写入日志。
	AdminBootstrapPassword string `env:"ADMIN_BOOTSTRAP_PASSWORD"`
	// GatewayInternalURL gateway 内部 HTTP 基址；默认使用容器网络中的 gateway:8083。
	GatewayInternalURL string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	// ExportDir 后台导出文件目录；空值使用导出服务的默认目录。
	ExportDir string `env:"EXPORT_DIR"`
}

// Worker 配置受管理的后台任务、三库连接及事件流客户端，不包含用户登录或支付凭据。
type Worker struct {
	// CentralDatabaseURL worker 发布 central 业务事件所用的 central_db MySQL URL；加载时必须提供。
	CentralDatabaseURL string `env:"DATABASE_URL_CENTRAL,required"`
	// HTTPAddr 本进程 HTTP 监听地址；未配置时使用对应服务的默认端口。
	HTTPAddr string `env:"WORKER_HTTP_ADDR" envDefault:":8085"`
	// DatabaseURL 本进程所属业务数据库的 MySQL URL；加载时必须提供，连接由资源装配负责。
	DatabaseURL string `env:"DATABASE_URL_WORKER,required"`
	// GatewayDatabaseURL worker 读取设备事件并发布 gateway 事件所用的 gateway_db MySQL URL；加载时必须提供。
	GatewayDatabaseURL string `env:"DATABASE_URL_GATEWAY,required"`
	// RedisStreamURL worker 发布事件使用的 Redis 流 URL；加载时必须提供。
	RedisStreamURL string `env:"REDIS_STREAM_URL,required"`
	// ServiceToken 服务间请求认证令牌；加载时必须提供，不得写入日志。
	ServiceToken string `env:"SERVICE_TOKEN,required"`
	// CentralInternalURL central 内部 HTTP 基址；默认使用容器网络中的 central:8082。
	CentralInternalURL string `env:"CENTRAL_INTERNAL_URL" envDefault:"http://central:8082"`
	// GatewayInternalURL gateway 内部 HTTP 基址；默认使用容器网络中的 gateway:8083。
	GatewayInternalURL string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
}

// LoadGateway 加载环境变量并校验设备连接上限，返回已解析配置或解析／校验错误；不打开连接或启动监听。
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

// LoadCentral 加载 central 所需环境变量并校验支付模式与直连凭据，返回配置或解析／校验错误；不读取用户登录模式、微信登录密钥或事件流配置，不打开资源。
func LoadCentral() (Central, error) {
	var c Central
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("central config: %w", err)
	}
	if c.PaymentMode != "disabled" && c.PaymentMode != "simulation" && c.PaymentMode != "wechat_direct" {
		return c, fmt.Errorf("central config: invalid PAYMENT_MODE")
	}
	if c.PaymentMode == "wechat_direct" && (c.WechatMchID == "" || c.WechatCertSerial == "" || len(c.WechatAPIv3Key) != 32 || c.WechatPrivateKey == "" || c.WechatNotifyURL == "") {
		return c, fmt.Errorf("central config: incomplete WeChat Pay credentials")
	}
	return c, nil
}

// LoadWorker 加载 worker 所需环境变量，返回配置或解析错误；不打开数据库、Redis 或启动后台任务。
func LoadWorker() (Worker, error) {
	var c Worker
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("worker config: %w", err)
	}
	return c, nil
}

// LoadAdmin 加载后台环境变量并校验 JWT 密钥长度，返回配置或解析／校验错误；不初始化后台账户或打开资源。
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

// LoadUser 加载用户端环境变量并校验 JWT、登录模式及支付配置，返回配置或解析／校验错误；不登录用户、构造支付渠道或打开资源。
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
