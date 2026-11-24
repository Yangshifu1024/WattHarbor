# 运行与数据维护

以下命令从仓库根目录执行。快速启动及开发检查见 [README](../README.md)，业务恢复规则见 [业务与接口契约](business-contracts.md)。

## 配置

以 [config.go](../backend/internal/platform/config/config.go)、Compose 和前端构建配置为准。开发 Compose 提供本地值；生产从 `.env` 和显式环境传入。密钥、私钥和真实配置不提交。

| 变量 | 使用方及规则 |
| --- | --- |
| `DATABASE_URL` | 应用的设备库 URL；单库迁移使用此变量 |
| `DATABASE_URL_CENTRAL`、`DATABASE_URL_GATEWAY`、`DATABASE_URL_WORKER` | 角色数据库连接。生产指向同一个 `wattharbor` 单库，开发分别指向三库 |
| `APP_UNIFIED_DATABASE` | 生产设为 `true`，使用单库中独立的设备 Outbox；三库模式为 `false` |
| `APP_HTTP_ADDR`、`DC589_ADDR` | 单体 HTTP 和设备 TCP 监听，默认 `:8080`、`:9100` |
| `REDIS_CACHE_URL`、`REDIS_STREAM_URL` | 分别用于会话缓存和事件流 |
| `DB_PASSWORD`、`DB_APP_PASSWORD` | MySQL 维护账号和应用运行账号密码 |
| `JWT_SECRET` | 用户与后台会话签名，至少 32 字节 |
| `SERVICE_TOKEN` | 进程内接口共享令牌 |
| `DEVICE_MAX_CONNECTIONS` | gateway，正整数，默认 10000 |
| `GATEWAY_DEBUG_HEARTBEAT` | gateway 心跳日志，默认 false；开发 Compose 固定 true |
| `ADMIN_BOOTSTRAP_USER`、`ADMIN_BOOTSTRAP_PASSWORD` | 仅 admin，空后台账号表的引导凭证 |
| `ADMIN_TRUSTED_PROXIES` | 仅 admin，逗号分隔的可信代理 IP／CIDR；默认空，非法值在路由装配时阻止启动。开发直连保留空值 |
| `ADMIN_PROXY_SUBNET`、`CADDY_ADMIN_PROXY_IP` | 生产 Compose 的专用代理网段及 Caddy 固定 IPv4，默认 `172.30.82.0/24` 与 `172.30.82.2`；后台仅信任这个单一地址 |
| `LOGIN_MODE` | 仅 user，默认 `wechat`；`development` 要求模拟支付 |
| `WECHAT_APPID`、`WECHAT_SECRET` | AppID 为 user / central 必填；Secret 仅 user 登录与手机号授权使用 |
| `PAYMENT_MODE` | user / central，`disabled/simulation/wechat_direct`，默认 disabled；生产 Compose 显式注入 |
| `WECHAT_MCH_ID`、`WECHAT_CERT_SERIAL`、`WECHAT_APIV3_KEY` | user / central 的微信直连商户 ID、证书序列号及 32 字节 API v3 密钥 |
| `WECHAT_PRIVATE_KEY_PATH`、`WECHAT_NOTIFY_URL` | 应用容器内私钥路径及对外支付回调地址；路径变量不代替私钥挂载 |
| `REGULATORY_MODE` | 预留模板／Compose 变量；当前 config 与 worker 不读取，修改值不会启用监管任务 |
| `REGULATORY_ENDPOINT`、`REGULATORY_SIGNING_SECRET` | 预留对接参数；当前 worker 未装配发送适配器，尚无已接线的平台字段、签名或响应契约 |
| `EXPORT_DIR` | admin 导出目录，生产已配置持久卷 |
| `VITE_API_BASE` | 后台 API 地址，构建时使用 |
| `TARO_APP_API_BASE` | 用户 API 前缀，默认 `/api/v1`；微信发布需绝对 HTTPS 地址 |
| `TARO_APP_LOGIN_MODE` | 用户构建登录模式，默认 wechat |
| `DEV_API_PROXY_TARGET` | 后台和 H5 的开发代理均指向单体 HTTP |

`.env.example` 还含未被当前实现或根 Compose 消费的 `TAG`、`ADMIN_BOOTSTRAP_PHONE`、`WECHAT_REFUND_NOTIFY_URL`、`WECHAT_CUSTOMER_SERVICE_CORP_ID`、`OTEL_EXPORTER_OTLP_ENDPOINT`、`DEFAULT_TIME_OF_USE_JSON`，修改这些值不会启用相应功能。

## 单体部署

开发和生产均运行 `app`，对应单体命令 `wattharbor`。生产 [compose.prod.yaml](../docker/compose.prod.yaml) 构建内嵌后台与 H5 的镜像。复制 `.env.example` 为 `.env`，配置数据库维护和运行密码、独立 Redis 密码、JWT、服务令牌、微信配置及公开域名。可选的 `ADMIN_BOOTSTRAP_USER` 和 `ADMIN_BOOTSTRAP_PASSWORD` 用于空库首次创建后台管理员。

```sh
node scripts/ops/check-deploy.mjs
docker compose --env-file .env -f docker/compose.prod.yaml up -d --build --wait
docker compose --env-file .env -f docker/compose.prod.yaml exec app wget -qO- http://127.0.0.1:8080/health/ready
```

MySQL 就绪后，Goose 初始化 `wattharbor` 单库，随后配置运行账号。MySQL、运行账号配置和 Redis 就绪后，应用装配模块，绑定 HTTP 与 DC589 TCP 并启动后台任务。首次成功完成同步轮次且依赖检查通过后，`/health/ready` 返回 200。收到 SIGTERM 时先停止接收 HTTP 请求与设备连接，等待已接收请求后关闭设备会话和后台任务。应用关停上限为 15 秒，Compose 留出 20 秒。

Caddy 代理后台、H5、用户 API 和公开通知。默认固定代理地址为 `172.30.82.2`，调整时同时设置 `ADMIN_PROXY_SUBNET` 与 `CADDY_ADMIN_PROXY_IP`。内网从 `/metrics/{admin,user,central,gateway,worker}` 抓取各角色指标，前缀为 `wattharbor_`。配置检查核对数据库、Redis、网络及 Caddy 分流，并在临时容器中验证 Caddy 配置。传入 `--config-only` 可在示例配置上检查结构。

开发项目沿用 `wattharbor-dev` 和已有三库数据卷。`dev-up` 会移除该 Compose 项目的旧进程容器并保留数据卷。生产项目保留既有单体项目名 `wattharbor-monolith` 及其数据卷。从旧生产拓扑切换前，需停旧 Go 进程，核对备份、数据卷、应用运行账号和 Redis Stream。已有三库可用 `node tasks.mjs build wattharbor` 构建单体，配置原三库 URL 运行，其中 `DATABASE_URL` 指向 gateway 库，`APP_UNIFIED_DATABASE=false`。需要合库时先在备份环境核对业务表、两路 Outbox 和 Redis Stream。

### 微信直连支付

生产默认 `PAYMENT_MODE=disabled`。微信直连需要 `PAYMENT_MODE=wechat_direct`、有效商户参数、`WECHAT_NOTIFY_URL` 以及 `WECHAT_PRIVATE_KEY_PATH=/run/secrets/wechat-merchant.pem`。为应用容器提供受控的只读挂载：

```yaml
services:
  app:
    volumes:
      - /etc/wattharbor/wechat-merchant.pem:/run/secrets/wechat-merchant.pem:ro
```

将部署侧挂载保存到 `compose.payment.yaml`，启动及维护时在生产 Compose 后追加此文件。商户私钥权限须允许容器 UID 10001 读取，API v3 密钥为 32 字节。正式用户端使用真实 AppID、已配置的 API 域名和微信发布环境。

### 上线验收

验证公网 HTTPS、内部 API 隔离、真实登录和手机号授权、真实商户支付及重复回调、原路退款、实机启动及未知结果、停机、断线恢复、刷卡重复行为、最终计量、权限、审计、故障告警、外部投递及备份恢复。模拟链路不能代替真实资金或实机验证。

REGULATORY_* 当前仅为预留变量，config 与 worker 未消费，设置 http 也不会启动监管投递。仓库保留可复用的发送适配器；目标平台的字段、签名、认证、响应与任务装配尚未接线。没有外部合规认证或可据此保证的 SLA、RPO、RTO。

## 规则层检查

```bash
node tasks.mjs check-rules
node --test scripts/rules/check-rules.test.mjs
```

规则层检查只读文档与迁移 schema，不连数据库、不连网络，秒级完成，是 `node tasks.mjs check` 的第一步。六条不变量的断言位置登记在 [check-rules.mjs](../scripts/rules/check-rules.mjs)，尚未实现的在报告里显式标为「尚未实现」并单独计数，**不算作已验证**——退出码 0 表示当前无违反，不表示六条都已断言化。**当前只有「冻结文档顶部带冻结声明并指向新层」一条有断言**，其余五条逐条报「尚未实现」；哪几条已实现以实际输出为准，见 [验证入口](rules/verification.md)。依赖方向、表归属与接口契约机器判不了，不由本检查判定，仍按 [仓库维护规则](../AGENTS.md) 由人工核对。

`.mjs` 检查器与门禁编排的测试不进 `node tasks.mjs check`，按需单独执行；Go 检查仍在同一门禁内。

Git 提交钩子也跑这一项，但**钩子默认未启用**，克隆后需显式安装一次：

```bash
node scripts/git/install-hooks.mjs
```

安装后每次提交依次执行 `check-rules`、`go vet ./...`、`go test ./...`，任一步失败即拒绝提交。格式化不在钩子内，仍需先执行 `node tasks.mjs fmt` 再暂存。卸载执行 `git config --unset core.hooksPath`。

## 数据库

| Schema | 初始化定义 |
| --- | --- |
| `central_db` | [0001_init.sql](../backend/migrations/central_db/0001_init.sql) |
| `gateway_db` | [0001_init.sql](../backend/migrations/gateway_db/0001_init.sql) |
| `worker_db` | [0001_init.sql](../backend/migrations/worker_db/0001_init.sql) |

另有各库的 Goose 版本表。表和字段 COMMENT 描述职责、单位、状态、快照及幂等用途。部分表按月分区，查询、更新及唯一性检查须使用实际联合键。表存在不表示对应功能已在主程序接线。

初始化空库需配置 `DATABASE_URL_GATEWAY`、`DATABASE_URL_CENTRAL`、`DATABASE_URL_WORKER`，然后执行：

```sh
go run ./cmd/migrate -schema all
```

MySQL init 脚本仅在空数据卷创建三库和授权，表由 Goose 初始化。项目尚未发布，初始化定义和已登记的增量按现有版本顺序执行，发布前结构调整直接修改相应定义，不新增发布前递增迁移，也不自动建表。已执行版本不会因文件修改重新应用；既有数据需要兼容迁移，不能只删除 Goose 版本表。MySQL DDL 无事务回滚。

本次设计兼容更新使用 [upgrade-design.mjs](../scripts/db/upgrade-design.mjs)，仅连接本项目开发编排。先执行无参数预检，再停止业务写入，以 `--apply` 备份至 `.tmp/db-design-backups/` 并补字段、索引与约束。工具发现有效身份冲突、非法金额或月首归一后的唯一键碰撞即停止，不删除或合并数据，也不修改 Goose 版本。DDL 中断后可重复执行补齐缺项；生产数据先在恢复副本验证，再按变更窗口执行经过审核的同等 DDL。

```sh
node scripts/db/upgrade-design.mjs
node scripts/db/upgrade-design.mjs --apply
node scripts/db/audit-design.mjs
```

仅应用 2026-10-09 审查修复结构时使用 `node scripts/db/upgrade-design.mjs --review-fixes` 预检，再执行 `node scripts/db/upgrade-design.mjs --review-fixes --apply`。该范围只补 `central_db.admin_user.mfa_secret_version/mfa_last_step`、`charge_billing_job.generation` 与 `gateway_db.sync_delivery_retry`；执行前仍备份三库，不删除数据或改变 Goose 版本。已有列默认保留原值，新消费记录为空；上线时先补结构，再部署使用新字段及退避接口的 gateway、admin／central 与 worker，避免混用接口版本。生产使用审核后的同等 DDL，而非直接调用仅面向开发编排的脚本。

投递异常可按 `sync_delivery_retry.kind/delivery_key` 查询 `state/attempts/last_reason/next_attempt_at`。`state` 取值见迁移 SQL 的 `gateway_db.sync_delivery_retry`，其中待核实那一档表示尚未确认、不代表独立人工审核页面已装配。分类只存固定值，核对原命令／事件、冻结请求和端口占用后再处理；禁止通过标记已上报／已处理掩盖未知结果。成功项退避与源记录标记同事务删除；迟到失败不会重建已完成项。gateway 在启动时及每小时清理30天前已完成或缺源的历史，每轮最多500条短事务，取消后等待任务退出再关闭资源。未完成的 `review/retry` 即使超过30天也保留；持续增加表示待处理业务异常积压，不能通过TTL删除来隐藏。清理错误记录为 `sync delivery history cleanup`，下轮继续。

结束候选使用 `device_event.idx_pending_type(event_type,processed_at,id)`；退避关联键显式使用 `ascii_bin`，避免连接字符集导致仅利用复合主键的kind前缀。旧开发库以全量兼容预检／`--apply` 补这两项索引（另一项为退避的 `idx_updated`）；`--review-fixes` 只补退避表索引，结束候选索引沿用全量入口。两条同步查询仍有跨表到期排序，EXPLAIN通过不构成规模吞吐验收。

[audit-design.mjs](../scripts/db/audit-design.mjs) 在同一只读一致性快照核对钱包余额与流水、冻结额与未结分摊、支付已退与成功退款、资金来源归属及跨月业务号。零差异返回 0、存在差异返回 2、执行故障返回 1；只输出类别和数量。导入余额缺少期初流水、已归档流水、外部渠道尚未对账，都需要进一步核实，不能据此直接改余额或判定渠道账实一致。

[maintain-partitions.mjs](../scripts/db/maintain-partitions.mjs) 默认预览未来 12 个月的分区补齐；`--through=YYYY-MM` 指定覆盖到该月，`--apply` 才执行。它在同一连接持有维护锁，按白名单将 `p_max` 重组为新月分区并保留 `p_max`，不删除历史分区。现有季度分区保持原状；DDL 可能搬移 `p_max` 中的数据，执行前备份并避开业务高峰。可由运维按月运行；当前 worker 未装配此任务，运行账号也无 DDL 权限。

```sh
node scripts/db/maintain-partitions.mjs
node scripts/db/maintain-partitions.mjs --through=2027-12 --apply
```

上述对账与分区工具默认连接本项目开发 MySQL。其它环境使用宿主机 MySQL 客户端，设置 `MYSQL_HOST/MYSQL_PORT/MYSQL_USER/MYSQL_PWD`；容器客户端可设置 `MYSQL_CLIENT_CONTAINER` 并传入 `MYSQL_PWD`。对账账号需要相关表的 SELECT；分区维护需由维护账号执行，不向业务账号授予 ALTER。

订单快照和曲线读取 `charge_process`。计费、估费和自动停机读取 `device_event` 中的心跳及结束计量。初始化 SQL 已移除独立遥测及聚合表。归档和删除期须同时核对订单曲线、计量证据、审计、未完结任务与恢复要求；付款、退款、启动、结束的回执摘要和幂等身份必须与其资金链共同保留，不能在 30 天后独立删除。当前工具不执行归档或删除。

后台订单时间过滤分为实际启动时间、未启动回退创建时间两条范围分支，保留原 `COALESCE(started_at,created_at)` 口径；时间过滤索引、用户进行中查询复合索引已补齐。列表仍保留原页码、总数及排序契约。开发库 EXPLAIN 只能核对访问计划，生产吞吐与延迟收益须以真实数据量及负载验证；连接池默认每池最多 20 个连接，worker 持有三池，配置预算须按所有进程副本汇总。

### 迁移与运行账号

`wattharbor` 是迁移与维护账号。应用使用 `wattharbor_app`，由 `DB_APP_PASSWORD` 配置。部署任务通过本地 MySQL socket 创建账号，按布局仅授予指定 schema 的 SELECT、INSERT、UPDATE 和 DELETE。生产授予 `wattharbor`，开发授予 `central_db`、`gateway_db` 和 `worker_db`。账号配置容器通过 `network_mode: none` 隔离网络，应用容器仅持有运行账号凭据。数据库 URL 中的密码需符合 URL 编码规则。

`provision-runtime` 在迁移成功后由 Compose 执行。已有单体数据卷可继续使用，切换时保留挂载并在维护窗口执行账号配置。备份恢复到其他 MySQL 实例后须重新配置相应权限。测试、种子及数据库维护工具继续使用维护连接。

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
node scripts/db/reset-dev.mjs --reset-development-data
node scripts/dev/prepare.mjs
```

该脚本核验本地项目、停止后端、先备份到 `.tmp/`，再重建三库并清空本项目两套 Redis，会清除业务、会话及事件。需要保留数据时不得执行。

## 备份与恢复

[backup.mjs](../scripts/db/backup.mjs) 和 [restore.mjs](../scripts/db/restore.mjs) 使用 Node.js 24 与 MySQL CLI。先创建输出目录。指定 `MYSQL_CLIENT_CONTAINER` 时使用容器客户端，凭据通过 `MYSQL_PWD` 传入。直连可配置 `MYSQL_HOST`、`MYSQL_PORT`、`MYSQL_USER` 和 `MYSQL_PWD`。生产数据库端口保持在 Docker 内网。

```bash
mkdir -p .tmp/backups
node scripts/db/backup.mjs --unified .tmp/backups
```

不传 `--unified` 时备份开发三库。归档包含相应的 SQL、例程、触发器、事件和 Goose 历史。三库逐库建立一致性快照，备份前协调停止业务写入和 DDL。生产单库的 [对账](../scripts/db/audit-design.mjs) 与 [分区维护](../scripts/db/maintain-partitions.mjs) 也使用 `--unified`。Redis AOF、导出文件、Caddy 状态和部署密钥需单独备份。

恢复会替换归档声明的 schema。停止应用、核对目标数据库及归档后，生产设置 `RESTORE_CONFIRM=wattharbor`，开发设置 `RESTORE_CONFIRM="gateway_db central_db worker_db"`：

```bash
RESTORE_CONFIRM=wattharbor node scripts/db/restore.mjs "$BACKUP_ARCHIVE"
```

恢复后核对 Goose 版本、业务数据和运行权限，再用相应 Compose 配置启动 `app`。生产使用 `migrate -schema unified`，三库使用 `migrate -schema all`。

## 监控与故障定位

单体提供 `/health/live`、`/health/ready` 和 `/metrics/{role}`。live 检查进程，ready 核对依赖和同步状态。HTTP 指标按已注册路由统计，不能作为 TCP 连接数或设备在线率。

每池增加 `wattharbor_db_open_connections/in_use_connections/idle_connections/max_open_connections`，以及 `wait_total/wait_seconds_total/max_idle_closed_total/max_idle_time_closed_total/max_lifetime_closed_total` 累计指标。标签仅固定 `service/schema`；等待持续增长时结合在用数、慢查询和锁等待诊断。所有新 MySQL 物理会话显式设置 UTC 和严格 SQL 模式，避免宿主机时区影响数据库时间。

| 现象 | 核对项 |
| --- | --- |
| 服务未就绪 | 对应单库或三库 URL、Goose 版本、数据库/Redis 凭证、依赖连通性与日志 |
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

控制接口只允许 loopback 监听。`power_deciwatts=2500` 表示 250 W；刷卡使用 `{"type":"card","port":1,"card":100001}`，移卡使用 `{"type":"remove-card","port":1}`。完整事件字段见 [Input](../backend/internal/gateway/simulator/dc589/behavior.go)，场景和参数见 `go run ./cmd/simulator-dc589 -h`。

充电心跳 15 秒，空闲心跳 60 秒，网关读超时为相应周期的三倍。模拟器每秒更新计量。当前使用通用五档参数，不支持特殊双口八档扩展、心跳分片合并和 OTA；完整协议规范仍保留这些内容。

## 全链路场景验收

本节是把充电、计量、计费与退款主流程做成可重复验收的操作清单。验收对象是开发栈的单体应用和宿主机 TCP 模拟器；模拟支付与模拟器结果不构成真实微信商户、实机设备或监管投递的验收，这些仍须按 [上线验收](#上线验收) 的外部条件单独执行。

### 环境准备

1. 一键启动：`node tasks.mjs dev-up` 启动开发栈（`docker/compose.dev.yaml`），再执行 `node scripts/dev/prepare.mjs` 建立厂商、站点、两台设备与方案。单体 HTTP 8080 与设备 TCP 9100 映射到宿主机环回地址。
2. 场景断言直接落在开发库，统一取库中最新一笔订单。注意集成测试的显式高位主键会把 `central_db.charge_order` 自增抬到超出 16 位十进制 BCD 编码的范围，gateway 启动受理随之持续返回 409（业务码 2000）；这不是业务缺陷，用 `node scripts/db/reset-dev.mjs --reset-development-data` 重建开发数据即消失。
3. 模拟器经真实 TCP 9100 接入宿主机映射端口，场景用 `-scenario` 选择（`by-energy`、`by-time`、`stop-on-command`、`reject-start`、`fault`、`silent`、`reconnect`），运行中调功率、插拔、刷卡见上一节控制接口。注意 `-power` 等功率参数单位是分瓦：`1500` 是 150 W，不是 1500 W。
4. 部分场景需要把站点生效方案换成特定模式（如 s4 的 10 分金额套餐），`scenarios.mjs budget` 已自动化该步骤；其余模式切换经后台执行，应用走乐观锁，`expected_version` 须读站点当前生效版本。

### 场景矩阵

每场景结束后按下一节清单逐表断言；金额、电量、事件链任何一格不符都按缺陷处理，不以最终状态相近豁免。

| # | 场景 | 操作 | 期望持久事实 |
| --- | --- | --- | --- |
| 1 | 电量/时长套餐自然结束 | `by-energy` 或 `by-time` 充至套餐耗尽 | 计费任务 `done`；预付与实收相等无退款；回执分段合计等于设备自报电量 |
| 2 | 用户主动停机 | `stop-on-command` 后用户端停机 | 计费 `done` 加差额自动退；首段起点与 `charge_order.started_at` 逐毫秒对齐 |
| 3 | 拒绝起充 | `reject-start` | 启动回执 `success=0`；订单转 `refunding`；全额退款 `success`；不建立端口占用 |
| 4 | 预算封顶 | 小额金额套餐加高功率持续充电 | `charge_billing_cutoff` 落 `budget_exhausted`；电费加服务费精确等于套餐价；无人工复核 |
| 5 | 掉电零功率 | 拔出或 `fault` 后等待满一分钟 | 截止原因 `automatic_stop`；电量为零全额退；不把断线当零功率提前停机 |
| 6 | 刷卡全流程 | 绑卡、刷卡首购、移卡重刷续购、**不移开重复上报**、用户停机 | 扣款、累计时长、操作凭据同事务；**不移开的第二次上报不产生第二笔扣款**；停机后结算与余额退款到账 |
| 7 | 单体中断恢复 | 充电中停止应用，启动后等待设备重连并发起停机 | 中断期间订单和设备占用保留；重连后停机、回执与计费完成 |
| 8 | 断线重连 | `reconnect` 场景断网后恢复 | 恢复后证据连续，不因缺口误判零功率停机 |

**场景 6 含一票否决项**：[ADR-0001](adr/0001-card-swipe-duplicate-report.md) 记录平台有意不引入服务端去重，代价是「同一张卡不移开而被重复上报」只能由设备侧保证不发生。该条因此是上线前的阻断条件：向模拟器控制口连发两次 `{"type":"card","port":<端口>,"card":<同一卡号>}` 且中间不发 `remove-card`，**第二笔不得产生扣款**。该断言不通过即视为阻断上线，不接受「最终余额相符」这类近似豁免——重复扣款会先发生，再被别的路径掩盖。

### 逐表断言清单

```sql
-- 订单与回执主链（按业务号或订单 ID 过滤）
SELECT status, started_at, charged_kwh, charged_seconds FROM central_db.charge_order;
SELECT success, result_code FROM central_db.charge_start_receipt;
SELECT port_no FROM central_db.active_port_charge;                -- 占用与释放
SELECT reason, cutoff_at, electric_cents, service_cents FROM central_db.charge_billing_cutoff;
SELECT meter_json FROM central_db.charge_end_receipt;              -- 段数、合计 Wh、首段起点、每段功率
SELECT status, attempts FROM central_db.charge_billing_job;
SELECT result_json FROM central_db.charge_fee_receipt;             -- 费用在 Fee 序列化 JSON 内
-- 资金
SELECT total_cents, refunded_cents, status FROM central_db.payment_order;
SELECT refund_cents, status FROM central_db.refund_record;         -- 经 biz_type/biz_id 关联订单
-- 事件与发布
SELECT event FROM central_db.charge_event_log ORDER BY id;         -- payment_confirmed → start_acked → charge_ended → fee_calculated → bill_issued
```

`meter_json` 的核对要点：分段首尾相接无空洞、合计等于设备自报总电量、首段起点等于 `charge_order.started_at`、功率分档模式下每段都带可验证功率。最后一跳超过 60 秒的尾段按已接受的精度妥协处理：电量仍归属到段，功率缺失时整单进计量复核，不得沿用旧功率或从电量反推。

### 脚本化执行

开发栈就绪后，场景驱动在 `scripts/dev/e2e/scenarios.mjs`：`scenarios.mjs <budget|s3|s1|s2|s5|s7|s4|s8>` 驱动单个场景并自动完成逐表断言，`budget` 子命令创建并应用 10 分金额套餐，仅 s4 前执行一次；s7 对应用容器执行 SIGKILL 并重新拉起，测试同一订单能否在设备重连后完成。断言统一取库中最新订单，不得并行运行多个场景；模拟器仍须按各场景以 `-scenario` 手工启动。S6（刷卡全流程）暂未脚本化，按矩阵手工执行。

模拟器须按场景逐个切换，启动命令取自下表（板号是 `prepare.mjs` 建立的 1 号设备，在 `backend` 目录下执行）：

| 场景 | 模拟器启动命令 |
| --- | --- |
| budget | 无需启动模拟器，只经后台应用方案 |
| s1 | `go run ./cmd/simulator-dc589 -scenario by-time -board-id 5348240514082652` |
| s2 | `go run ./cmd/simulator-dc589 -scenario stop-on-command -board-id 5348240514082652` |
| s3 | `go run ./cmd/simulator-dc589 -scenario reject-start -board-id 5348240514082652` |
| s4 | `go run ./cmd/simulator-dc589 -scenario stop-on-command -board-id 5348240514082652` |
| s5 | `go run ./cmd/simulator-dc589 -scenario stop-on-command -board-id 5348240514082652` |
| s7 | `go run ./cmd/simulator-dc589 -scenario stop-on-command -board-id 5348240514082652` |
| s8 | `go run ./cmd/simulator-dc589 -scenario reconnect -board-id 5348240514082652` |

s1、s2、s4、s5、s7 共用 `stop-on-command`，差异全在驱动步骤：s2 调用户端停机，s4 起充后把功率推到 1500 W（`power_deciwatts: 15000`）尽快耗尽 10 分预算，s5 发 `unplug`，s7 请求停机前先中断并重启单体，等待设备重连。`-power` 只决定起充前的初始功率，默认 150 W 通常无需覆盖。

s5 起充后由驱动向模拟器控制口发 `unplug`（即拔枪，端口不再连接）模拟掉电，模拟器保持会话，平台才会在连续一分钟零功率后执行自动停机并落 `automatic_stop` 截止点。这里不能用 `-scenario fault`：`fault` 在起充后按 `-fault-after`（默认 5 秒）主动上报故障，设备自行结束充电并上报 `charge_ended`，而 `AutoStopper` 只处理仍处于 `charging` 的订单，于是不会出现 `automatic_stop`，表现为 s5 断言失败。矩阵场景 5 的「拔出或 `fault` 后等待满一分钟」是手工验收口径，脚本化路径固定为「`stop-on-command` 加驱动发 `unplug`」；`fault` 用于验证设备主动报故障的另一条路径。

各场景串行执行，不得并行；换场景前先停掉上一个模拟器进程，同一板号重复接入会与残留会话冲突。

### 退出

停掉模拟器（前台 Ctrl+C 或退出命令）。开发栈可保留供后续联调复用；如需回收，执行 `node tasks.mjs dev-down`（容器与数据卷保留），核对 9100、9190 和 8080 的监听符合预期。保留场景日志与 SQL 导出作为验收证据。
