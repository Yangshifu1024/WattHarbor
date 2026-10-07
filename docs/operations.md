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

[compose.prod.yaml](../docker/compose.prod.yaml) 定义生产拓扑。复制 `.env.example` 为 `.env` 并填写 `MYSQL_ROOT_PASSWORD/DB_PASSWORD/REDIS_PASSWORD/REDIS_STREAM_PASSWORD/JWT_SECRET/SERVICE_TOKEN/WECHAT_APPID/WECHAT_SECRET/PUBLIC_DOMAIN`。数据库和 Redis 仅内网访问，Cache 为 allkeys-lru，Stream 为 noeviction 且启用 AOF。镜像使用非 root distroless；MySQL、事件 Redis、导出和 Caddy 状态使用持久卷。

Caddy 公网暴露 `80/443`，`/api/v1/admin/*` 代理 admin，`/api/v1/user/*` 与 `/api/v1/public/*` 代理 user；内部及其余 API 路径返回 404。`/admin/*` 提供后台静态文件，根路径转向后台。gateway 另暴露 `9100/TCP`，gateway/worker HTTP 不公开。后台静态产物必须先构建，H5 和小程序发布另行配置。

```bash
node scripts/ops/check-deploy.mjs
docker compose -f docker/compose.prod.yaml up -d --build
docker compose -f docker/compose.prod.yaml ps
docker compose -f docker/compose.prod.yaml logs --tail=100 chargepilot-central chargepilot-gateway chargepilot-worker chargepilot-caddy
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

保存部署侧文件为 `compose.payment.yaml` 后，使用 `docker compose -f docker/compose.prod.yaml -f compose.payment.yaml config --quiet` 检查合并配置，启动及后续维护也使用这两个 `-f` 文件。`check-deploy.mjs` 只检查生产 Compose。商户私钥文件权限须允许容器非 root UID 65532 读取；API v3 密钥为 32 字节。配置加载及 Compose 展开通过不证明私钥可读取、渠道可达或真实微信支付已验收。worker 通过 central 的同一支付提供方执行退款和查询。正式用户端须使用真实 AppID、已配置的 API 域名和微信发布环境。

### 上线验收

验证公网 HTTPS、内部 API 隔离、真实登录和手机号授权、真实商户支付及重复回调、原路退款、实机启动及未知结果、停机、断线恢复、刷卡重复行为、最终计量、权限、审计、故障告警、外部投递及备份恢复。模拟链路不能代替真实资金或实机验证。

REGULATORY_* 当前仅为预留变量，config 与 worker 未消费，设置 http 也不会启动监管投递。仓库保留可复用的发送适配器；目标平台的字段、签名、认证、响应与任务装配尚未接线。没有外部合规认证或可据此保证的 SLA、RPO、RTO。

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

遥测的原有「1 月保留」表注释没有对应清理任务，已改为明确无自动清理。归档和删除期须同时核对历史曲线需求、订单计量证据、审计、未完结任务与恢复要求；付款、退款、启动、结束的回执摘要和幂等身份必须与其资金链共同保留，不能在 30 天后独立删除。当前工具不执行归档或删除。

后台订单时间过滤分为实际启动时间、未启动回退创建时间两条范围分支，保留原 `COALESCE(started_at,created_at)` 口径；时间过滤索引、用户进行中查询复合索引以及聚合月份条件已补齐。列表仍保留原页码、总数及排序契约。开发库 EXPLAIN 只能核对访问计划，生产吞吐与延迟收益须以真实数据量及负载验证；连接池默认每池最多 20 个连接，worker 持有三池，配置预算须按所有进程副本汇总。

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

[backup.mjs](../scripts/db/backup.mjs) 和 [restore.mjs](../scripts/db/restore.mjs) 依赖 Node.js 24（gzip 由内置 zlib 提供）与 MySQL 客户端。输出目录须预先存在；容器客户端回退需设置 `MYSQL_CLIENT_CONTAINER`。连接参数为 `MYSQL_HOST/MYSQL_PORT/MYSQL_USER`；原生客户端用 `MYSQL_PWD`，容器回退从 `MYSQL_PASSWORD` 传入密码。凭证从安全环境加载，避免写入命令历史。

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

每池增加 `chargepilot_db_open_connections/in_use_connections/idle_connections/max_open_connections`，以及 `wait_total/wait_seconds_total/max_idle_closed_total/max_idle_time_closed_total/max_lifetime_closed_total` 累计指标。标签仅固定 `service/schema`；等待持续增长时结合在用数、慢查询和锁等待诊断。所有新 MySQL 物理会话显式设置 UTC 和严格 SQL 模式，避免宿主机时区影响数据库时间。

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

1. 一键启动：`make dev-up` 启动开发栈（`docker/compose.dev.yaml`，五个服务以 `go run` 在容器内运行），再执行 `node scripts/dev/prepare.mjs` 建立厂商、站点、两台设备与方案。user/admin/worker/gateway 的 HTTP 与 gateway 的 9100/TCP 映射到宿主机 loopback；central 8082 仅容器网络可达。
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
| 6 | 刷卡全流程 | 绑卡、刷卡首购、移卡重刷续购、用户停机 | 扣款、累计时长、操作凭据同事务；停机后结算与余额退款到账 |
| 7 | worker 中断恢复 | 充电中 SIGKILL worker，再用户停机，最后重启 worker | worker 缺席期间中央订单保持充电中且无结束回执；重启后数秒内补齐回执、订单完成、事件与 Outbox 同步补发 |
| 8 | 断线重连 | `reconnect` 场景断网后恢复 | 恢复后证据连续，不因缺口误判零功率停机 |

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

### 退出

停掉模拟器（前台 Ctrl+C 或退出命令）。开发栈可保留供后续联调复用；如需回收，执行 `make dev-down`（容器与数据卷保留），核对 9100、9190 及 8080/8081/8083/8085 的监听符合预期。保留场景日志与 SQL 导出作为验收证据。

