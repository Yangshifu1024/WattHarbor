# ChargePilot

ChargePilot 是单客户独立部署的二轮车充电运营平台，支持扫码充电、钱包与在线卡、计费退款及运营管理。后端使用 Go，后台使用 React，用户端使用 Taro 构建 H5 和微信小程序，设备通过 DC589/TCP 接入。

## 仓库结构

- `backend/`：后端 Go 模块（`cmd/`、`internal/`、`migrations/`、`go.mod`），服务镜像定义为 `backend/Dockerfile`，构建上下文即该目录。后端路径约定见 [AGENTS.md](AGENTS.md)。
- `admin-web/`、`miniprogram/`：运营后台与用户端前端。
- `docker/`：Compose 编排（`compose.dev.yaml`、`compose.prod.yaml`）、生产 Caddyfile 与 MySQL 初始化 SQL。
- 仓库根：`scripts/`（跨平台 Node 脚本）、`docs/` 与根 `Makefile`。

## 本地运行

需要 Docker Desktop（或 Docker Engine）、Compose v2 及 Node.js 24。脚本均为跨平台 Node 脚本，以下命令在任意系统均从仓库根目录直接执行：

```sh
node scripts/dev/start.mjs
# 等待服务就绪后准备厂商、站点、两台设备和测试方案
node scripts/dev/prepare.mjs
```

其他系统可使用 `docker compose -f docker/compose.dev.yaml up -d`。首次启动会安装前端依赖、编译后端并初始化空库；启动和数据准备均保留已有数据及配置。

| 入口 | 本地地址 |
| --- | --- |
| 运营后台 | http://127.0.0.1:5173/admin/ |
| 用户 H5 | http://127.0.0.1:5174/ |
| user / admin API | `127.0.0.1:8080` / `8081` |
| gateway / worker HTTP | `127.0.0.1:8083` / `8085` |
| DC589/TCP | `127.0.0.1:9100` |
| MySQL | `127.0.0.1:3306` |
| Redis Cache / Stream | `127.0.0.1:6379` / `6380` |

开发后台默认账号为 `admin / ChangeMe!Admin2026`，H5 测试账号为 `tester-1`。后台引导只在账号表为空时执行。准备脚本读取当前进程的 `ADMIN_BOOTSTRAP_USER`、`ADMIN_BOOTSTRAP_PASSWORD`，不自动加载 `.env`；已有账号改密后须传入有效凭证。

开发栈使用开发登录和模拟支付，模拟支付仍走订单、设备、结算和退款流程。Go 服务没有热重载；修改源码后重建对应进程，例如：

```powershell
docker compose -f docker/compose.dev.yaml up -d --no-deps --force-recreate user admin central gateway worker
```

### 模拟器

准备设备后，在两个终端分别执行：

```sh
node scripts/dev/simulator.mjs --number 1
node scripts/dev/simulator.mjs --number 2
```

两个实例对应设备 `5348240514082652`、`5348240514082653`，控制端口为 `9190`、`9191`，状态保存在各自的 `.tmp/` 文件。模拟器只连接已开通设备；端口数须与后台一致。

联调顺序：确认设备在线 → 用户选择端口和套餐 → 模拟付款 → 调整功率或刷卡 → 停止充电 → 核对计量、账单、退款及钱包流水。TUI 用 `m` 打开菜单、`Tab` 切页、`q` 退出；无界面控制和重启恢复说明见 [模拟器操作](docs/operations.md#模拟器操作)。

## 服务与代码

| 进程 | 职责 | 数据库 | HTTP |
| --- | --- | --- | --- |
| `user` | 用户登录、扫码、订单、支付、钱包与卡券 | `central_db` | `8080` |
| `admin` | 站点、设备、订单、资金、审计与导出 | `central_db` | `8081` |
| `central` | 内部用例编排、授权、回执、计费与退款 | `central_db` | `8082` |
| `gateway` | TCP 接入、设备指令、遥测与端口占用 | `gateway_db` | `8083` |
| `worker` | 持久任务扫描、结果同步与 Outbox 发布 | 三库；执行状态在 `worker_db` | `8085` |

central 的 `8082` 在开发和生产编排中都不映射到宿主机，只能从容器网络访问。生产 HTTP 经 Caddy 分流至 user/admin，gateway/worker HTTP 仅内网可达；设备另走 `9100/TCP`。五个服务均提供 `/health/live`、`/health/ready` 和 `/metrics`。

业务按 `backend/internal/` 下十二个 use case 组织；`backend/cmd/` 负责装配与生命周期，`backend/internal/http/` 负责 HTTP 适配，`backend/internal/domain/` 保存纯规则。服务间 DTO 在 `contracts/`，具名客户端在 `integration/`；请求以 `Request` 结尾，响应以 `Reply` 结尾。完整职责和维护规则见 [AGENTS.md](AGENTS.md) 与 [架构边界](docs/architecture-boundaries.md)。

## 接口与业务约定

- 后台接口文档：`GET /api/docs/admin.openapi.json`，源文件为 [admin.openapi.json](backend/internal/http/auth/admin.openapi.json)。该文档端点未由生产 Caddy 对外代理。
- 用户接口文档：[user.openapi.json](backend/internal/http/user/user.openapi.json)，目前是静态契约文件。两份 OpenAPI 随接口变更维护，测试核对文档中的路由是否注册；实际路由见 [user](backend/cmd/user/routes.go)、[admin](backend/cmd/admin/routes.go)、[central](backend/cmd/central/routes.go)。
- 普通 JSON 响应使用 `code/message/data/request_id/trace_id`，成功 `code=0`；微信通知、文件、指标和 OpenAPI 使用各自格式。用户与后台会话分开，内部接口使用 `X-Service-Token`。
- 金额使用整数分；用户 Snowflake ID 为十进制字符串，前端须保留精度。数据库时间按 UTC，计费日界及 DC589 本地时间按北京时间。
- 下单冻结方案与价格；启动受理、设备启动、充电结束、结算及退款到账分别确认。未知设备结果保留占用，渠道结果未知复用原退款号查询。

计价、钱包、卡、审核、权限和异步恢复的完整规则见 [业务与接口契约](docs/business-contracts.md)；设备报文见 [DC589 协议](protocols/dc589.md)。

## 开发与验证

宿主机需要 Go 1.27、Node.js 24、npm 和 Make。开发 Compose 提供运行环境；后端检查与构建：

```sh
node scripts/git/install-hooks.mjs
make check
make build-all
# 直连开发环境（3306/6379），迁移三库并执行集成测试
node scripts/db/test-integration.mjs
```

`make check` 顺序运行 `go fmt ./...`、`go vet ./...` 和 `go test ./...`（go 命令由 Makefile 进入 `backend/` 执行）；pre-commit 直接运行 vet 和默认测试（经 scripts/git/pre-commit.mjs，不依赖 make）。缺少 Make 时在 `backend/` 下直接运行对应 Go 命令。编译产物在 `bin/`，可用 `make build SERVICE=central` 单独构建。

真库测试由 `integration` tag 隔离，不包含在 `make check` 中。按 2026-10-06 的决定，集成测试直接使用开发环境（3306/6379/6380）：`node scripts/db/test-integration.mjs` 会起依赖服务、补 [锁观测只读授权](docker/mysql/test-lock-observer.sql)、迁移三库并注入连接串。注意集成测试的显式主键会抬高开发库自增序号，必要时用 reset-dev 重建；直接执行 `make test-integration` 须自行配置 `TEST_MYSQL_URL` 和 `TEST_REDIS_URL`。

前端命令在对应目录执行：

| 目录 | 安装 / 开发 | 检查 / 构建 |
| --- | --- | --- |
| `admin-web` | `npm ci`、`npm run dev` | `npm test`、`npm run lint`、`npm run build` |
| `miniprogram` | `npm ci`、`npm run dev:h5` 或 `npm run dev:weapp` | `npm run typecheck`、`npm test`、`npm run build:h5`、`npm run build:weapp` |

产物为 `admin-web/dist`、`miniprogram/dist/h5` 和 `miniprogram/dist/weapp`。微信开发工具导入 `dist/weapp` 并配置真实 AppID。CI workflow 已于 2026-10-06 移除：提交前在本地执行 `make check`（pre-commit 钩子含 vet 与默认测试）与 `node scripts/db/test-integration.mjs`，用户端检查需单独执行。用户端后续开发与交互优化计划见 [miniprogram-roadmap.md](docs/miniprogram-roadmap.md)。

## 生产部署与维护

生产入口为 [compose.prod.yaml](docker/compose.prod.yaml)。复制 [.env.example](.env.example) 为 `.env`，填写实际凭据和域名，先构建后台静态文件，再检查并启动：

```powershell
npm --prefix admin-web ci
npm --prefix admin-web run build
node scripts/ops/check-deploy.mjs
docker compose -f docker/compose.prod.yaml up -d --build
```

默认支付模式为 `disabled`。微信直连需要有效商户参数，并在 user 和 central 容器中只读挂载私钥；仅设置路径变量不会创建文件。生产配置、支付挂载、监控及故障定位见 [运行与数据维护](docs/operations.md)。

三库由 Goose 初始化，定义见 [central](backend/migrations/central_db/0001_init.sql)、[gateway](backend/migrations/gateway_db/0001_init.sql)、[worker](backend/migrations/worker_db/0001_init.sql)。已执行的初始化 SQL 修改后不会自动重跑；保留数据时先备份再做兼容更新。开发重建会清空三库及项目 Redis，操作条件见 [数据库维护](docs/operations.md#数据库)。备份恢复说明见 [运行与数据维护](docs/operations.md#备份与恢复)，恢复前须核对归档和目标环境。

## 当前边界

- 本地模拟链路不能代替真实微信商户、实机或外部平台验收。H5 支持开发登录和模拟支付；正式微信支付与手机号授权通过微信小程序。
- 内部 Outbox 和 Redis 发布已运行；Webhook、通用 DLQ、监管投递及其管理端点尚未装配。`REGULATORY_*` 是预留变量，修改值不会启用任务。
- 微信订阅消息、电子发票签发、汇付天下、自动银行出款、自动渠道账单下载、MQTT、OTA、月卡/年卡及混合支付未接入。多主体分账、提现与对账已移除。
- 公告列表只返回全局公告；站点详情另包含本站生效公告，城市定向尚未接线。

重构进度与后续验收见 [refactor-roadmap.md](docs/refactor-roadmap.md#9-进度与交付记录)。许可见 [LICENSE](LICENSE)，商业授权以签订合同为准。
