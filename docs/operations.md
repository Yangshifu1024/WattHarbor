# 运行与数据维护

以下命令从仓库根目录执行。快速启动及开发检查见 [README](../README.md)，业务恢复规则见 [业务与接口契约](business-contracts.md)。

## 配置

以 [config.go](../internal/platform/config/config.go)、Compose 和前端构建配置为准。开发 Compose 提供本地值；生产从 `.env` 和显式环境传入。密钥、私钥和真实配置不提交。

| 变量 | 使用方及规则 |
| --- | --- |
| `DATABASE_URL` | gateway 的 MySQL URL |
| `DATABASE_URL_CENTRAL` | user / admin / central / worker / migrate，central schema |
| `DATABASE_URL_GATEWAY` | worker / migrate，gateway schema |
| `DATABASE_URL_WORKER` | worker / migrate，worker schema；migrate 同时读取另两库 URL |
| `USER_HTTP_ADDR`、`ADMIN_HTTP_ADDR`、`CENTRAL_HTTP_ADDR`、`WORKER_HTTP_ADDR` | 对应进程默认 `:8080`、`:8081`、`:8082`、`:8085` |
| `GATEWAY_HTTP_ADDR`、`DC589_ADDR` | gateway，默认 `:8083`、`:9100` |
| `REDIS_CACHE_URL`、`REDIS_STREAM_URL` | user / admin 使用 Cache 保存会话，central 使用 Cache 做健康检查；worker 使用 Stream 发布 Outbox。gateway 不使用 Redis |
| `JWT_SECRET` | 仅 user / admin，至少 32 字节 |
| `SERVICE_TOKEN` | 五个 Go 服务内部 API 共享令牌 |
| `GATEWAY_INTERNAL_URL`、`CENTRAL_INTERNAL_URL` | 服务间地址，按 Compose 服务名与内网端口配置 |
| `DEVICE_MAX_CONNECTIONS` | gateway，正整数，默认 10000 |
| `GATEWAY_DEBUG_HEARTBEAT` | gateway 心跳日志，默认 false；开发 Compose 固定 true |
| `ADMIN_BOOTSTRAP_USER`、`ADMIN_BOOTSTRAP_PASSWORD` | 仅 admin，空后台账号表的引导凭证 |
| `LOGIN_MODE` | 仅 user，默认 `wechat`；`development` 要求模拟支付 |
| `WECHAT_APPID`、`WECHAT_SECRET` | AppID 为 user / central 必填；Secret 仅 user 登录与手机号授权使用 |
| `PAYMENT_MODE` | user / central，`disabled/simulation/wechat_direct`，默认 disabled；生产 Compose 显式注入 |
| `WECHAT_MCH_ID`、`WECHAT_CERT_SERIAL`、`WECHAT_APIV3_KEY` | user / central 的微信直连商户 ID、证书序列号及 32 字节 API v3 密钥 |
| `WECHAT_PRIVATE_KEY_PATH`、`WECHAT_NOTIFY_URL` | user / central 容器内私钥路径及对外支付回调地址；路径变量不代替私钥挂载 |
| `REGULATORY_MODE` | 预留模板／Compose 变量；当前 config 与 worker 不读取，修改值不会启用监管任务 |
| `REGULATORY_ENDPOINT`、`REGULATORY_SIGNING_SECRET` | 预留对接参数；当前 worker 未装配发送适配器，尚无已接线的平台字段、签名或响应契约 |
| `EXPORT_DIR` | admin 导出目录，生产已配置持久卷 |
| `VITE_API_BASE` | 后台 API 地址，构建时使用 |
| `TARO_APP_API_BASE` | 用户 API 前缀，默认 `/api/v1`；微信发布需绝对 HTTPS 地址 |
| `TARO_APP_LOGIN_MODE` | 用户构建登录模式，默认 wechat |
| `DEV_API_PROXY_TARGET` | 后台开发代理指向 admin，H5 开发代理指向 user |

`.env.example` 还含未被当前实现或根 Compose 消费的 `TAG`、`ADMIN_BOOTSTRAP_PHONE`、`WECHAT_REFUND_NOTIFY_URL`、`WECHAT_CUSTOMER_SERVICE_CORP_ID`、`OTEL_EXPORTER_OTLP_ENDPOINT`、`DEFAULT_TIME_OF_USE_JSON`，修改这些值不会启用相应功能。

## 生产部署

根 [docker-compose.yml](../docker-compose.yml) 定义生产拓扑。复制 `.env.example` 为 `.env` 并填写 `MYSQL_ROOT_PASSWORD/DB_PASSWORD/REDIS_PASSWORD/REDIS_STREAM_PASSWORD/JWT_SECRET/SERVICE_TOKEN/WECHAT_APPID/WECHAT_SECRET/PUBLIC_DOMAIN`。数据库和 Redis 仅内网访问，Cache 为 allkeys-lru，Stream 为 noeviction 且启用 AOF。镜像使用非 root distroless；MySQL、事件 Redis、导出和 Caddy 状态使用持久卷。

Caddy 公网暴露 `80/443`，`/api/v1/admin/*` 代理 admin，`/api/v1/user/*` 与 `/api/v1/public/*` 代理 user；内部及其余 API 路径返回 404。`/admin/*` 提供后台静态文件，根路径转向后台。gateway 另暴露 `9100/TCP`，gateway/worker HTTP 不公开。后台静态产物必须先构建，H5 和小程序发布另行配置。

```bash
node scripts/ops/check-deploy.mjs
docker compose -f docker-compose.yml up -d --build
docker compose -f docker-compose.yml ps
docker compose -f docker-compose.yml logs --tail=100 chargepilot-central chargepilot-gateway chargepilot-worker chargepilot-caddy
```

部署检查核对五个应用服务的配置归属、共享业务库、内部端口隔离、Redis 策略与 Caddy 分流，再用无网络临时容器验证 Caddy 语法；不会启动生产应用。`--config-only` 仅跳过凭据值检查，Compose 的必填变量及挂载路径仍须有效。检查器回归使用 `node --test scripts/ops/check-deploy.test.mjs`。模板检查通过不代表真实凭据、支付私钥或生产连通性已经验收。

应用容器当前没有 Compose healthcheck，应主动检查 `/health/ready`。

### 微信直连支付

根 Compose 已将支付模式及商户参数显式注入 user 与 central；模板默认 `PAYMENT_MODE=disabled`。user 发起支付并接收对外通知，central 执行内部退款。微信登录密钥 `WECHAT_SECRET` 只注入 user，worker 不持有商户凭据。

真实直连部署须在部署侧设置 `PAYMENT_MODE=wechat_direct`、有效商户参数、`WECHAT_NOTIFY_URL` 和容器内 `WECHAT_PRIVATE_KEY_PATH=/run/secrets/wechat-merchant.pem`。根 Compose 未挂载商户私钥；仅传入路径变量不会创建文件。为 user 与 central 增加受控只读挂载 override，例如：

```yaml
services:
  chargepilot-user:
    volumes:
      - /etc/chargepilot/wechat-merchant.pem:/run/secrets/wechat-merchant.pem:ro
  chargepilot-central:
    volumes:
      - /etc/chargepilot/wechat-merchant.pem:/run/secrets/wechat-merchant.pem:ro
```

保存部署侧文件为 `compose.payment.yaml` 后，使用 `docker compose -f docker-compose.yml -f compose.payment.yaml config --quiet` 检查合并配置，启动及后续维护也使用这两个 `-f` 文件。`check-deploy.mjs` 只检查根生产 Compose。商户私钥文件权限须允许容器非 root UID 65532 读取；API v3 密钥为 32 字节。配置加载及 Compose 展开通过不证明私钥可读取、渠道可达或真实微信支付已验收。worker 通过 central 的同一支付提供方执行退款和查询。正式用户端须使用真实 AppID、已配置的 API 域名和微信发布环境。

### 上线验收

验证公网 HTTPS、内部 API 隔离、真实登录和手机号授权、真实商户支付及重复回调、原路退款、实机启动及未知结果、停机、断线恢复、刷卡重复行为、最终计量、权限、审计、故障告警、外部投递及备份恢复。模拟链路不能代替真实资金或实机验证。

REGULATORY_* 当前仅为预留变量，config 与 worker 未消费，设置 http 也不会启动监管投递。仓库保留可复用的发送适配器；目标平台的字段、签名、认证、响应与任务装配尚未接线。没有外部合规认证或可据此保证的 SLA、RPO、RTO。

## 数据库

| Schema | 初始化定义 |
| --- | --- |
| `central_db` | [0001_init.sql](../migrations/central_db/0001_init.sql) |
| `gateway_db` | [0001_init.sql](../migrations/gateway_db/0001_init.sql) |
| `worker_db` | [0001_init.sql](../migrations/worker_db/0001_init.sql) |

另有各库的 Goose 版本表。表和字段 COMMENT 描述职责、单位、状态、快照及幂等用途。部分表按月分区，查询、更新及唯一性检查须使用实际联合键。表存在不表示对应功能已在主程序接线。

初始化空库需配置 `DATABASE_URL_GATEWAY`、`DATABASE_URL_CENTRAL`、`DATABASE_URL_WORKER`，然后执行：

```sh
go run ./cmd/migrate -schema all
```

MySQL init 脚本仅在空数据卷创建三库和授权，表由 Goose 初始化。项目尚未发布，各 schema 仅保留 `0001_init.sql`，结构调整直接修改定义，不新增发布前递增迁移，也不自动建表。已执行版本不会因文件修改重新应用；既有数据需要兼容迁移，不能只删除 Goose 版本表。MySQL DDL 无事务回滚。

充值申请的 `idx_payment_order(payment_order_id)` 是非唯一索引，供支付回调锁定所属申请，避免扫描无关申请。已执行旧版初始化的数据库须先备份该表并核对 `SHOW INDEX FROM central_db.wallet_recharge_request`；没有此索引时执行兼容增量更新，无需重建数据：

```sql
SET SESSION lock_wait_timeout = 10;
ALTER TABLE central_db.wallet_recharge_request
  ADD INDEX idx_payment_order (payment_order_id)
    COMMENT '支付回调定位并锁定所属充值申请，避免扫描无关申请',
  ALGORITHM=INPLACE, LOCK=NONE;
```

更新后核对索引为非唯一、列为 `payment_order_id`；保留已有 NULL 和重复关联，不去重或改为 UNIQUE。索引减少无关申请的扫描争用，同一钱包仍按原行锁串行入账。

明确可丢弃的开发数据可重建：

```powershell
./scripts/db/reset-dev.ps1 -ResetDevelopmentData
node scripts/dev/prepare.mjs
```

该脚本核验本地项目、停止后端、先备份到 `.tmp/`，再重建三库并清空本项目两套 Redis，会清除业务、会话及事件。需要保留数据时不得执行。

## 备份与恢复

[backup.sh](../scripts/db/backup.sh) 和 [restore.sh](../scripts/db/restore.sh) 依赖 Bash、gzip 与 MySQL 客户端。输出目录须预先存在；容器客户端回退需设置 `MYSQL_CLIENT_CONTAINER`。连接参数为 `MYSQL_HOST/MYSQL_PORT/MYSQL_USER`；原生客户端用 `MYSQL_PWD`，容器回退从 `MYSQL_PASSWORD` 传入密码。凭证从安全环境加载，避免写入命令历史。

```bash
mkdir -p .tmp/backups
bash scripts/db/backup.sh .tmp/backups
```

备份包含三库的 SQL、例程、触发器、事件及 Goose 历史，逐库使用 single-transaction。跨库不保证同一时点，协调停止写入后备份；DDL 与备份也须避免并行。脚本检查压缩完整性和最低大小，不证明可恢复；定期在隔离环境恢复并核对资金、订单和幂等记录。

SQL 备份不包含 Redis AOF、导出文件、Caddy 状态、密钥和部署配置，须另行备份。已发布 Outbox 不会自动重发事件 Redis 中丢失的全部记录，不能直接清空 Stream 卷后宣称恢复完成。

恢复会先删除归档声明的三库，再重建并导入，失败时可能只完成部分数据。停止业务写入，核对归档及目标环境后执行：

```bash
RESTORE_CONFIRM="gateway_db central_db worker_db" bash scripts/db/restore.sh "$BACKUP_ARCHIVE"
```

`BACKUP_ARCHIVE` 须设置为已核对的归档路径。恢复后核对三库、Goose 版本和关键台账；若执行 migrate，必须配置全部三库 URL。开发服务用 `docker compose -f compose.dev.yaml restart user admin central gateway worker`；生产使用实际部署的全部 Compose 文件重启对应服务。更新或恢复不得使用 `docker compose down -v`。

## 监控与故障定位

五个 Go 服务均提供 `/health/live`、`/health/ready` 和 `/metrics`。live 只说明进程存活，ready 检查服务依赖。HTTP 指标按已注册路由统计，不能作为 TCP 连接数或设备在线率。

```bash
docker compose -f compose.dev.yaml -f compose.monitoring.yaml up -d prometheus
```

当前 Prometheus 面向开发内网，端口 9090，抓取间隔 15 秒、保留 7 天。抓取失败持续 1 分钟告警；近 5 分钟至少 20 次请求且 5xx 超 5%，持续 5 分钟告警；排除 `/metrics` 后同样流量条件下 p95 超 2 秒，持续 5 分钟告警。配置和规则测试见 [monitoring](../monitoring)，当前没有 Alertmanager 通知链路。

| 现象 | 核对项 |
| --- | --- |
| 服务未就绪 | 对应三库 URL、Goose 版本、数据库/Redis 凭证、依赖连通性与日志 |
| 管理员无法登录 | 引导仅空表执行、账号状态、MFA、Redis 会话、实时权限及限速 |
| 设备离线 | TCP 9100、设备编号/端口数、开通状态、连接会话与心跳；断线不代表端口已停机 |
| 已付款未充电 | 支付意图、端口占用、启动命令及回执；先确定结果，不能重复启动或直接退款 |
| 已停机未结算 | 结束投递、central 回执、计费任务、计量复核和冻结快照 |
| 退款未到账 | 同一退款号的渠道结果、预留额度、执行策略及双人审核，processing 不等于成功 |
| 任务或事件积压 | 数据库任务、Outbox 发布标记、持久回执及 Redis 连通性；当前没有 Webhook 消费或通用 DLQ |
| HTTPS 异常 | 域名解析、80/443、服务器时钟、Caddy 日志及持久数据卷 |

## 模拟器操作

编号为 16 位十进制数字，须与后台 DC589 设备一致。默认两端口，`-Ports` 支持 1～20，须与后台端口数一致。各实例使用独立编号、控制端口和状态文件。

TUI 支持调功率、插拔、刷卡与移卡、余额查询、设备参数、故障、温度、电压、烟雾、校时、断线重连和重启。`Tab` 切页，`m` 打开操作菜单，方向键选择，`Enter` 提交，`Esc` 返回，`q` 退出。卡持续贴住只触发一次刷卡事件，再次刷卡前需移卡。状态文件保存进行中的充电和待确认报告，支持进程重启恢复。

TUI 的启动信息、控制服务日志与设备通信日志统一显示在“日志”页；无界面模式继续写入 stdout。退出前恢复终端并等待设备循环停止，异常退出原因由命令输出到 stderr。

无界面运行及控制示例：

```powershell
go run ./cmd/simulator-dc589 -board-id 5348240514082652 -ports 2 -tui=false -control 127.0.0.1:9190 -state-file .tmp/simulator-headless.json
Invoke-RestMethod http://127.0.0.1:9190/state
Invoke-RestMethod http://127.0.0.1:9190/events -Method Post -ContentType application/json -Body '{"type":"power","port":1,"power_deciwatts":2500}'
```

控制接口只允许 loopback 监听。`power_deciwatts=2500` 表示 250 W；刷卡使用 `{"type":"card","port":1,"card":100001}`，移卡使用 `{"type":"remove-card","port":1}`。完整事件字段见 [Input](../internal/gateway/simulator/dc589/behavior.go)，场景和参数见 `go run ./cmd/simulator-dc589 -h`。

充电心跳 15 秒，空闲心跳 60 秒，网关读超时为相应周期的三倍。模拟器每秒更新计量。当前使用通用五档参数，不支持特殊双口八档扩展、心跳分片合并和 OTA；完整协议规范仍保留这些内容。
