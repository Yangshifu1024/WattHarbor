# 运行与数据维护

以下命令从仓库根目录执行。快速启动及开发检查见 [README](../README.md)，业务恢复规则见 [业务与接口契约](business-contracts.md)。

## 配置

以 [config.go](../backend/internal/platform/config/config.go)、Compose 和前端构建配置为准。开发 Compose 提供本地值；生产从 `.env` 和显式环境传入。密钥、私钥和真实配置不提交。

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
| `ADMIN_TRUSTED_PROXIES` | 仅 admin，逗号分隔的可信代理 IP／CIDR；默认空，非法值在路由装配时阻止启动。开发直连保留空值 |
| `ADMIN_PROXY_SUBNET`、`CADDY_ADMIN_PROXY_IP` | 生产 Compose 的专用代理网段及 Caddy 固定 IPv4，默认 `172.30.81.0/29` 与 `172.30.81.2`；后台仅信任这个单一地址 |
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

## 单体部署

构建命令为 `node tasks.mjs build wattharbor`，产物为 `bin/wattharbor`。日志与 Prometheus 指标使用 WattHarbor 名称，指标以 `wattharbor_` 为前缀。后台和用户端继续读取原浏览器存储键，保留会话和待重试请求的幂等号。监管协议的签名请求头沿用现有契约。

三个 Compose 文件分别使用 `wattharbor-dev`、`wattharbor` 和 `wattharbor-monolith` 项目名。已有本地环境时，启动新项目会使用新项目的数据卷。已有环境切换前需核对原有卷、MySQL 账号及导出挂载路径。

[compose.monolith.yaml](../docker/compose.monolith.yaml) 构建一个含后台和 H5 的应用镜像。填写 `MYSQL_ROOT_PASSWORD`、`DB_PASSWORD`、`REDIS_PASSWORD`、`REDIS_STREAM_PASSWORD`、`JWT_SECRET`、`SERVICE_TOKEN`、`WECHAT_APPID`、`WECHAT_SECRET` 和 `PUBLIC_DOMAIN`。连接 URL 中的密码需要符合 URL 转义规则。首个后台账号由 `ADMIN_BOOTSTRAP_USER` 和 `ADMIN_BOOTSTRAP_PASSWORD` 配置。

```sh
docker compose -f docker/compose.monolith.yaml config --quiet
docker compose -f docker/compose.monolith.yaml up -d --build
docker compose -f docker/compose.monolith.yaml exec app wget -qO- http://127.0.0.1:8080/health/ready
```

MySQL 和 Redis 就绪后，Goose 先初始化 `wattharbor`。应用装配五个角色，绑定 DC589 和 HTTP 后启动后台任务。首次成功完成同步轮次且依赖检查通过后，`/health/ready` 返回 200。`/health/live` 供进程存活探测。收到 SIGTERM 时停止接收 HTTP 请求及设备连接，等待已接收请求，随后取消设备会话和后台任务，最后关闭资源。应用总关停时限为 15 秒，Compose 留出 20 秒。

Caddy 仅代理后台、H5 和公开 API。其固定代理地址使用 `172.30.82.2`，部署前核对代理子网。内网可在 `/metrics/{admin,user,central,gateway,worker}` 分别抓取各角色的指标。拥有 `audit.read` 权限的后台用户可以在「系统管理 → 运行诊断」查看任务与积压，每次访问记录审计。

已有数据时可用 `node tasks.mjs build wattharbor` 构建单体，并继续使用原三库。按配置表填写原三库 URL，由 `DATABASE_URL` 指向 gateway，`APP_UNIFIED_DATABASE` 留空。切换前停旧进程，避免两套进程同时监听设备和运行任务，并复用原事件 Redis。单库初始化命令为 `cd backend && go run ./cmd/migrate -schema unified`，读取 `DATABASE_URL` 并要求库名为 `wattharbor`。该命令只初始化单库。已有部署如果需要合库，应在备份环境单独校对业务表、两路 Outbox 和 Redis Stream 再切换。

单体部署使用微信直连时，将下节的商户私钥只读挂载加在 `app` 容器，容器进程 UID 为 10001。设备入口仍为 `9100/TCP`。

## 五进程生产部署

[compose.prod.yaml](../docker/compose.prod.yaml) 定义生产拓扑。复制 `.env.example` 为 `.env` 并填写 `MYSQL_ROOT_PASSWORD/DB_PASSWORD/DB_USER_PASSWORD/DB_ADMIN_PASSWORD/DB_CENTRAL_PASSWORD/DB_GATEWAY_PASSWORD/DB_WORKER_PASSWORD/REDIS_PASSWORD/REDIS_STREAM_PASSWORD/JWT_SECRET/SERVICE_TOKEN/WECHAT_APPID/WECHAT_SECRET/PUBLIC_DOMAIN`。数据库和 Redis 仅内网访问，Cache 为 allkeys-lru，Stream 为 noeviction 且启用 AOF。镜像使用非 root distroless；MySQL、事件 Redis、导出和 Caddy 状态使用持久卷。

Caddy 公网暴露 `80/443`，`/api/v1/admin/*` 代理 admin，`/api/v1/user/*` 与 `/api/v1/public/*` 代理 user；内部及其余 API 路径返回 404。`/admin/*` 提供后台静态文件，根路径转向后台。gateway 另暴露 `9100/TCP`，gateway/worker HTTP 不公开。后台静态产物必须先构建，H5 和小程序发布另行配置。

后台反代使用 `admin-proxy-upstream:8081`，该别名仅在 Caddy 与 admin 的 `admin-proxy` 专用内部网络中注册，避免走默认业务网络时来源地址改变。`ADMIN_TRUSTED_PROXIES` 在生产编排直接取 `CADDY_ADMIN_PROXY_IP`，不能配置成整个 Docker 网段。部署前核对代理子网不与已有网络冲突，固定地址属于该子网；调整时同时设置上述两个变量。部署检查器核对单地址信任、网络成员、别名和内部网络属性；实际客户端 IP、限流分桶与审计仍须部署后验证。

```bash
node scripts/ops/check-deploy.mjs
docker compose -f docker/compose.prod.yaml up -d --build
docker compose -f docker/compose.prod.yaml ps
docker compose -f docker/compose.prod.yaml logs --tail=100 wattharbor-central wattharbor-gateway wattharbor-worker wattharbor-caddy
```

部署检查核对五个应用服务的配置归属、共享业务库、内部端口隔离、Redis 策略与 Caddy 分流，再用无网络临时容器验证 Caddy 语法；不会启动生产应用。`--config-only` 仅跳过凭据值检查，Compose 的必填变量及挂载路径仍须有效。检查器回归使用 `node --test scripts/ops/check-deploy.test.mjs`。模板检查通过不代表真实凭据、支付私钥或生产连通性已经验收。

应用容器当前没有 Compose healthcheck，应主动检查 `/health/ready`。

### 微信直连支付

根 Compose 已将支付模式及商户参数显式注入 user 与 central；模板默认 `PAYMENT_MODE=disabled`。user 发起支付并接收对外通知，central 执行内部退款。微信登录密钥 `WECHAT_SECRET` 只注入 user，worker 不持有商户凭据。

真实直连部署须在部署侧设置 `PAYMENT_MODE=wechat_direct`、有效商户参数、`WECHAT_NOTIFY_URL` 和容器内 `WECHAT_PRIVATE_KEY_PATH=/run/secrets/wechat-merchant.pem`。根 Compose 未挂载商户私钥；仅传入路径变量不会创建文件。为 user 与 central 增加受控只读挂载 override，例如：

```yaml
services:
  wattharbor-user:
    volumes:
      - /etc/wattharbor/wechat-merchant.pem:/run/secrets/wechat-merchant.pem:ro
  wattharbor-central:
    volumes:
      - /etc/wattharbor/wechat-merchant.pem:/run/secrets/wechat-merchant.pem:ro
```

保存部署侧文件为 `compose.payment.yaml` 后，使用 `docker compose -f docker/compose.prod.yaml -f compose.payment.yaml config --quiet` 检查合并配置，启动及后续维护也使用这两个 `-f` 文件。`check-deploy.mjs` 只检查生产 Compose。商户私钥文件权限须允许容器非 root UID 65532 读取；API v3 密钥为 32 字节。配置加载及 Compose 展开通过不证明私钥可读取、渠道可达或真实微信支付已验收。worker 通过 central 的同一支付提供方执行退款和查询。正式用户端须使用真实 AppID、已配置的 API 域名和微信发布环境。

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

MySQL init 脚本仅在空数据卷创建三库和授权，表由 Goose 初始化。项目尚未发布，各 schema 仅保留 `0001_init.sql`，结构调整直接修改定义，不新增发布前递增迁移，也不自动建表。已执行版本不会因文件修改重新应用；既有数据需要兼容迁移，不能只删除 Goose 版本表。MySQL DDL 无事务回滚。

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

`wattharbor` 保留三库 DDL 与数据权限，仅用于 migrate、测试、devseed、重置与备份恢复。五个服务使用独立账号；密码分别由 `DB_USER_PASSWORD`、`DB_ADMIN_PASSWORD`、`DB_CENTRAL_PASSWORD`、`DB_GATEWAY_PASSWORD`、`DB_WORKER_PASSWORD` 配置。运行账号没有 CREATE、ALTER、DROP、TRUNCATE、GRANT 权限。

| 账号 | 数据权限 |
| --- | --- |
| `wattharbor_user`、`wattharbor_admin`、`wattharbor_central` | `central_db.*` 的 SELECT / INSERT / UPDATE / DELETE |
| `wattharbor_gateway` | `gateway_db.*` 的 SELECT / INSERT / UPDATE / DELETE |
| `wattharbor_worker` | `worker_db.*` 的 SELECT / INSERT / UPDATE / DELETE；`gateway_db.event_outbox`、`central_db.event_outbox`、`central_db.admin_event_outbox` 的 SELECT / UPDATE |

[provision-runtime.mjs](../scripts/db/provision-runtime.mjs) 以 root 创建/更新运行账号并重置其直接权限；不改变 `wattharbor` 权限。脚本使用 Node.js 24 和 MySQL CLI，不依赖 shell、sed 或临时 SQL 文件；Compose 的专用镜像提供两项运行依赖，宿主机仅需 Docker Compose。两套 Compose 均按 MySQL 就绪 → migrate 完成 → provision-runtime 完成 → 服务启动排列依赖。root 凭据只注入 MySQL 与一次性账号配置容器，运行服务不持有它。密码通过环境传入，SQL 经标准输入交给客户端，账号配置容器不输出密码或原始客户端诊断。Compose 将运行密码原值拼入 MySQL URL，须使用 URL 安全字符，可用 Node.js 的 `crypto.randomBytes(32).toString('hex')` 分别生成。开发默认密码仅供本机使用。

MySQL 与账号配置容器共享 `mysql-socket` 卷，后者只读挂载 Unix socket 并连接 `root@localhost`，且设置 `network_mode: none`；新实例显式设置 `MYSQL_ROOT_HOST=localhost`，不需要远程 root。共享 socket 仅挂载到这两个容器。

已有卷不会重新执行 MySQL init 脚本。兼容升级先核对三库和 Goose 版本、确认所需表已迁移，在维护窗口重建 MySQL 容器以挂载 socket 卷，保留原 `mysql-data` 卷，再执行以下账号配置步骤。MySQL 会短暂停机；后续在计划的部署窗口重建业务容器才会采用新账号：

```sh
# 开发栈：保留数据卷，先挂载共享 socket，再配置账号。
docker compose -f docker/compose.dev.yaml up -d --no-deps --wait mysql
docker compose -f docker/compose.dev.yaml run --rm --no-deps provision-runtime
# 生产栈：先填写五个运行密码；有 override 时仍使用完整 -f 列表。
docker compose -f docker/compose.prod.yaml up -d --no-deps --wait wattharbor-mysql
docker compose -f docker/compose.prod.yaml run --rm --no-deps wattharbor-provision-runtime
```

已有卷的账号不会因 `MYSQL_ROOT_HOST` 变化而重建。若原实例已有远程 root 账号，确认 socket 的 `root@localhost` 登录及维护工具可用后，由管理员在维护窗口撤销不再需要的远程 root 账号；配置脚本不自动删除历史管理员账号。

常规新部署由 Compose 自动执行迁移与账号配置；不要跳过迁移完成条件。修改密码时同步运行密码环境与账号配置，并在计划窗口重建对应服务，避免旧连接回收后继续使用旧密码。devseed、integration 测试、reset 与备份工具仍使用其原有维护连接，不能替换成运行账号；业务服务不可使用维护账号绕过权限限制。运行授权不包含管理账号或其密码哈希；备份恢复后的目标实例仍须重新执行账号配置。

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

[backup.mjs](../scripts/db/backup.mjs) 和 [restore.mjs](../scripts/db/restore.mjs) 依赖 Node.js 24（gzip 由内置 zlib 提供）与 MySQL 客户端。输出目录须预先存在；容器客户端回退需设置 `MYSQL_CLIENT_CONTAINER`。连接参数为 `MYSQL_HOST/MYSQL_PORT/MYSQL_USER`；原生客户端优先用非空 `MYSQL_PWD`，只提供 `MYSQL_PASSWORD` 时映射到 `MYSQL_PWD`，探库和数据读写使用同一环境。容器回退从 `MYSQL_PASSWORD` 传入密码。凭证从安全环境加载，避免写入命令历史。

```bash
mkdir -p .tmp/backups
node scripts/db/backup.mjs .tmp/backups
```

备份包含三库的 SQL、例程、触发器、事件及 Goose 历史，逐库使用 single-transaction。跨库不保证同一时点，协调停止写入后备份；DDL 与备份也须避免并行。脚本检查压缩完整性和最低大小，不证明可恢复；定期在隔离环境恢复并核对资金、订单和幂等记录。

SQL 备份不包含 Redis AOF、导出文件、Caddy 状态、密钥和部署配置，须另行备份。已发布 Outbox 不会自动重发事件 Redis 中丢失的全部记录，不能直接清空 Stream 卷后宣称恢复完成。

恢复会先删除归档声明的三库，再重建并导入，失败时可能只完成部分数据。停止业务写入，核对归档及目标环境后执行：

```bash
RESTORE_CONFIRM="gateway_db central_db worker_db" node scripts/db/restore.mjs "$BACKUP_ARCHIVE"
```

`BACKUP_ARCHIVE` 须设置为已核对的归档路径。恢复后核对三库、Goose 版本和关键台账；若执行 migrate，必须配置全部三库 URL。开发服务用 `docker compose -f docker/compose.dev.yaml restart user admin central gateway worker`；生产使用实际部署的全部 Compose 文件重启对应服务。更新或恢复不得使用 `docker compose down -v`。

## 监控与故障定位

五个 Go 服务均提供 `/health/live`、`/health/ready` 和 `/metrics`。live 只说明进程存活，ready 检查服务依赖。HTTP 指标按已注册路由统计，不能作为 TCP 连接数或设备在线率。

每池增加 `wattharbor_db_open_connections/in_use_connections/idle_connections/max_open_connections`，以及 `wait_total/wait_seconds_total/max_idle_closed_total/max_idle_time_closed_total/max_lifetime_closed_total` 累计指标。标签仅固定 `service/schema`；等待持续增长时结合在用数、慢查询和锁等待诊断。所有新 MySQL 物理会话显式设置 UTC 和严格 SQL 模式，避免宿主机时区影响数据库时间。

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

控制接口只允许 loopback 监听。`power_deciwatts=2500` 表示 250 W；刷卡使用 `{"type":"card","port":1,"card":100001}`，移卡使用 `{"type":"remove-card","port":1}`。完整事件字段见 [Input](../backend/internal/gateway/simulator/dc589/behavior.go)，场景和参数见 `go run ./cmd/simulator-dc589 -h`。

充电心跳 15 秒，空闲心跳 60 秒，网关读超时为相应周期的三倍。模拟器每秒更新计量。当前使用通用五档参数，不支持特殊双口八档扩展、心跳分片合并和 OTA；完整协议规范仍保留这些内容。

## 全链路场景验收

本节是把充电、计量、计费与退款主流程做成可重复验收的操作清单。验收对象是开发栈的五个服务加宿主机真实 TCP 模拟器；模拟支付与模拟器结果不构成真实微信商户、实机设备或监管投递的验收，这些仍须按 [上线验收](#上线验收) 的外部条件单独执行。

### 环境准备

1. 一键启动：`node tasks.mjs dev-up` 启动开发栈（`docker/compose.dev.yaml`，五个服务以 `go run` 在容器内运行），再执行 `node scripts/dev/prepare.mjs` 建立厂商、站点、两台设备与方案。user/admin/worker/gateway 的 HTTP 与 gateway 的 9100/TCP 映射到宿主机 loopback；central 8082 仅容器网络可达。
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
| 7 | worker 中断恢复 | 充电中 SIGKILL worker，再用户停机，最后重启 worker | worker 缺席期间中央订单保持充电中且无结束回执；重启后数秒内补齐回执、订单完成、事件与 Outbox 同步补发 |
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

开发栈就绪后，场景驱动在 `scripts/dev/e2e/scenarios.mjs`：`scenarios.mjs <budget|s3|s1|s2|s5|s7|s4|s8>` 驱动单个场景并自动完成逐表断言，`budget` 子命令创建并应用 10 分金额套餐，仅 s4 前执行一次；s7 对 worker 容器执行 SIGKILL 并重新拉起，与进程级中断等价。断言统一取库中最新订单，不得并行运行多个场景；模拟器仍须按各场景以 `-scenario` 手工启动。S6（刷卡全流程）暂未脚本化，按矩阵手工执行。

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

s1、s2、s4、s5、s7 共用 `stop-on-command`，差异全在驱动步骤：s2 调用户端停机，s4 起充后把功率推到 1500 W（`power_deciwatts: 15000`）尽快耗尽 10 分预算，s5 发 `unplug`，s7 请求停机前先 SIGKILL worker。`-power` 只决定起充前的初始功率，默认 150 W 通常无需覆盖。

s5 起充后由驱动向模拟器控制口发 `unplug`（即拔枪，端口不再连接）模拟掉电，模拟器保持会话，平台才会在连续一分钟零功率后执行自动停机并落 `automatic_stop` 截止点。这里不能用 `-scenario fault`：`fault` 在起充后按 `-fault-after`（默认 5 秒）主动上报故障，设备自行结束充电并上报 `charge_ended`，而 `AutoStopper` 只处理仍处于 `charging` 的订单，于是不会出现 `automatic_stop`，表现为 s5 断言失败。矩阵场景 5 的「拔出或 `fault` 后等待满一分钟」是手工验收口径，脚本化路径固定为「`stop-on-command` 加驱动发 `unplug`」；`fault` 用于验证设备主动报故障的另一条路径。

各场景串行执行，不得并行；换场景前先停掉上一个模拟器进程，同一板号重复接入会与残留会话冲突。

### 退出

停掉模拟器（前台 Ctrl+C 或退出命令）。开发栈可保留供后续联调复用；如需回收，执行 `node tasks.mjs dev-down`（容器与数据卷保留），核对 9100、9190 及 8080/8081/8083/8085 的监听符合预期。保留场景日志与 SQL 导出作为验收证据。
