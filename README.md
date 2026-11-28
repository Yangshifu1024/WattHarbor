# WattHarbor · 电港

WattHarbor（电港）是单客户独立部署的二轮车充电运营平台，支持扫码充电、钱包与在线卡、计费退款及运营管理。后端使用 Go，后台使用 React，用户端使用 Taro 构建 H5 和微信小程序，设备通过 DC589/TCP 接入。

## 功能概览

| 业务 | 当前能力 |
| --- | --- |
| 扫码充电 | 匿名识别设备与端口、查询套餐，支付前登录；付款确认后启动、查看订单采样与预估费用、主动停止 |
| 方案与计费 | 站点默认方案、设备整体覆盖；金额、固定时长、固定电量套餐；下单冻结价格，按有效计量结算 |
| 在线卡 | 运营绑定、用户挂失与解绑；刷卡扣钱包、同卡重刷购买时长、未用差额退回钱包 |
| 钱包与退款 | 充值到账、余额与流水、原充值来源退款、频次风控、审核及独立解冻 |
| 设备与站点 | 厂商、站点、设备开通与运营状态；DC589 指令、心跳、采样、历史曲线和故障记录 |
| 运营与财务 | 订单与账单、计量复核、人工退款双人审核、发票申请审核、公告、优惠券及数据导出 |
| 异步恢复 | 持久启动任务、设备结果同步、自动停止、计费与退款派发、Outbox 发布及幂等回执 |

这些能力按当前代码归纳；真实支付、实机与外部平台的接入边界见[当前边界](#当前边界)。

阅读入口：[本地运行](#本地运行) · [服务与代码](#服务与代码) · [核心业务流程](#核心业务流程) · [接口与业务约定](#接口与业务约定) · [开发与验证](#开发与验证) · [生产部署与维护](#生产部署与维护)。

## 仓库结构

- `backend/`：后端 Go 模块 `wattharbor`（`cmd/`、`internal/`、`migrations/`、`go.mod`），单体镜像定义为 [Dockerfile.monolith](deploy/Dockerfile.monolith)。后端路径约定见 [AGENTS.md](AGENTS.md)。
- `frontend/admin-web/`、`frontend/miniprogram/`：运营后台与用户端前端。
- `deploy/`：Compose 编排（`compose.dev.yaml`、`compose.prod.yaml`）、生产 Caddyfile 与 MySQL 初始化 SQL。
- 仓库根：`scripts/`（跨平台 Node 脚本）、`docs/` 与根 `tasks.mjs`。

## 本地运行

需要 Docker Desktop（或 Docker Engine）、Compose v2 及 Node.js 24。脚本均为跨平台 Node 脚本，以下命令在任意系统均从仓库根目录直接执行：

```sh
node scripts/dev/start.mjs
# 等待服务就绪后准备厂商、站点、两台设备和测试方案
node scripts/dev/prepare.mjs
```

也可直接使用 `docker compose -f deploy/compose.dev.yaml up -d --wait --remove-orphans`。首次启动会安装前端依赖、编译后端并初始化空库；启动和数据准备均保留已有数据及配置。

| 入口 | 本地地址 |
| --- | --- |
| 运营后台 | http://127.0.0.1:5173/admin/ |
| 用户 H5 | http://127.0.0.1:5174/ |
| 单体 HTTP | `127.0.0.1:8080`（用户、后台、支付回调、健康检查） |
| DC589/TCP | `127.0.0.1:9100` |
| MySQL | `127.0.0.1:3306` |
| Redis Cache / Stream | `127.0.0.1:6379` / `6380` |

开发后台默认账号为 `admin / ChangeMe!Admin2026`，H5 测试账号为 `tester-1`。后台引导只在账号表为空时执行。准备脚本读取当前进程的 `ADMIN_BOOTSTRAP_USER`、`ADMIN_BOOTSTRAP_PASSWORD`，不自动加载 `.env`；已有账号改密后须传入有效凭证。

开发栈使用开发登录和模拟支付，模拟支付仍走订单、设备、结算和退款流程。Go 服务启动时编译。修改源码后重新创建应用容器：

```sh
docker compose -f deploy/compose.dev.yaml up -d --wait --no-deps --force-recreate app
```

修改用户端 `app.config.ts` 后，须重启 `user-h5` 容器才能重建路由表：`docker compose -f deploy/compose.dev.yaml restart user-h5`。

### 模拟器

准备设备后，在两个终端分别执行：

```sh
node scripts/dev/simulator.mjs --number 1
node scripts/dev/simulator.mjs --number 2
```

两个实例对应设备 `5348240514082652`、`5348240514082653`，控制端口为 `9190`、`9191`，状态保存在各自的 `.tmp/` 文件。模拟器只连接已开通设备；端口数须与后台一致。

联调顺序：确认设备在线 → 用户选择端口和套餐 → 模拟付款 → 调整功率或刷卡 → 停止充电 → 核对计量、账单、退款及钱包流水。TUI 用 `m` 打开菜单、`Tab` 切页、`q` 退出；无界面控制和重启恢复说明见 [模拟器操作](docs/operations.md#模拟器操作)。

## 应用与代码

[wattharbor](backend/cmd/wattharbor/main.go) 是唯一业务入口。HTTP 8080 接收用户、后台与支付通知，DC589 TCP 9100 接收设备连接，任务循环在同一进程执行。

| 模块 | 职责 |
| --- | --- |
| `user` | 登录、扫码、订单、支付、钱包与卡券 |
| `admin` | 站点、设备、订单、资金、审计与导出 |
| `central` | 内部用例编排、授权、回执、计费与退款 |
| `gateway` | 设备接入、指令、心跳与端口占用 |
| `worker` | 持久任务扫描、回执同步及 Outbox 发布 |

内部模块调用在进程内保留请求契约与服务令牌。生产通过 Caddy 提供公开 API、后台和 H5。应用内网提供 `/health/live`、`/health/ready` 和各模块的 `/metrics/{role}`。生产默认使用 `wattharbor` 单库，开发沿用同一个 MySQL 实例的三库。

```mermaid
flowchart LR
    HTTP["管理后台 / H5 / 小程序 / 支付通知"] --> Caddy["Caddy · HTTPS"]
    Caddy -->|"HTTP 8080"| App["WattHarbor · API、设备、后台任务"]
    Device["DC589 设备"] <-->|"TCP 9100"| App
    App --> Database[("MySQL")]
    App --> Cache[("Redis Cache")]
    App --> Stream[("Redis Stream")]
```

业务用例位于 `backend/internal/`，应用装配位于 `internal/application/`，HTTP 适配位于 `internal/http/`，纯业务规则位于 `internal/domain/`。维护规则见 [AGENTS.md](AGENTS.md)。

## 核心业务流程

以下 Mermaid 图可在支持 Mermaid 的 Markdown 阅读器中渲染，描述当前已装配的业务链路。流程图展示条件分支，泳道图和时序图展示责任归属；图中的受理、确认、结算和到账各有独立证据。具体计量、金额、权限及重试条件以[业务与接口契约](docs/business-contracts.md)为准。

### 1. 运营准备与方案发布

运营人员先开通设备和端口，再发布兼容该协议的方案。模板应用时复制配置；设备独立方案整体覆盖站点默认方案，撤销覆盖后恢复继承。发布新版本只影响后续购买，历史订单继续使用冻结快照。

```mermaid
flowchart TD
    Setup["维护厂商、站点、设备及端口"] --> Scheme["选择模板或编写完整方案"]
    Scheme --> Target["选择站点默认或设备独立配置"]
    Target --> Check{"权限、数据范围、版本与协议能力通过？"}
    Check -->|"否"| Correct["修正配置或处理版本冲突"]
    Correct --> Scheme
    Check -->|"是"| Publish["同事务保存规则版本、发布记录和审计"]
    Publish --> Effective{"设备有独立方案？"}
    Effective -->|"有"| Override["使用设备完整方案"]
    Effective -->|"无"| Inherit["使用站点默认方案"]
    Override --> Offers["生成可购买套餐"]
    Inherit --> Offers
    Offers --> Snapshot["新付款意图冻结套餐与价格"]
    Snapshot --> History["订单结算使用原快照"]
```

实现入口：[方案发布](backend/internal/pricing/publication.go)、[扫码支付准备](backend/internal/payment/checkout.go)。发布方案不下发充电指令；设备禁用阻止新购买和加时，已有订单仍可停止、结算及退款。

### 2. 用户扫码到完成

用户可匿名扫码、选端口及浏览套餐，支付前才要求登录。设备码进入端口选择，端口码直接定位插口；金额和套餐由服务端确定。

```mermaid
flowchart TD
    Scan["扫码：设备码 / 端口码 / HTTPS 二维码"] --> Kind{"识别结果"}
    Kind -->|"设备"| Port["选择端口"]
    Kind -->|"端口"| Offers["匿名查询有效套餐"]
    Port --> Offers
    Offers --> Select["选择金额、时长或电量套餐"]
    Select --> Login{"已登录？"}
    Login -->|"否"| Auth["登录后继续原选择"]
    Auth --> Checkout["重新校验端口与套餐，创建付款意图"]
    Login -->|"是"| Checkout
    Checkout --> Pay["发起支付，等待可信付款确认"]
    Pay --> Start["创建已支付订单，异步派发启动"]
    Start --> Result{"设备启动结果"}
    Result -->|"明确成功"| Charging["充电中：订单曲线与预估费用"]
    Result -->|"明确拒绝"| Refund["按原支付来源退款"]
    Result -->|"未知"| Pending["保留占用，等待回执或补偿确认"]
    Charging --> Stop["主动停止 / 自动停止 / 设备自行结束"]
    Stop --> End["保存结束回执与最终计量"]
    End --> Billing["按冻结方案结算；证据不足转人工复核"]
    Billing --> View["查看确认费用、账单及退款状态"]
```

支付取消、失败或尚未确认时不启动设备；付款意图过期后收到付款则登记退款。启动结果未知时不会直接认定失败或再次发送新的启动指令。实时费用是预估值，最终费用来自有效结算回执。

### 3. 支付确认与设备启动时序

此图以微信支付的正常路径为例；开发模拟支付复用业务回调逻辑。`scan/start` 的响应是付款准备结果，客户端支付成功仍须等待服务端确认。

```mermaid
sequenceDiagram
    autonumber
    participant App as 用户端
    participant U as user
    participant Pay as 支付渠道
    participant W as worker
    participant C as central
    participant G as gateway
    participant D as 设备
    App->>U: 提交请求 UUID、端口与套餐
    U->>U: 保存付款意图、价格快照及端口预占
    U->>Pay: 准备预支付参数
    Pay-->>U: 返回支付参数
    U-->>App: 返回付款准备结果
    App->>Pay: 确认付款
    Pay->>U: 服务端付款通知
    U->>U: 验签解密，核对身份、单号与金额
    alt 付款意图有效
        U->>U: 同事务确认支付、创建订单、保存快照与 Outbox
        U-->>Pay: 确认通知已处理
        W->>C: 读取持久的已支付待启动订单
        C-->>W: 返回待启动订单
        W->>G: 按原订单号请求启动
        G->>C: 核对订单启动授权
        C-->>G: 返回授权与冻结参数
        G->>G: 先保存 START 命令与物理端口占用
        G->>D: 下发启动指令
        D-->>G: 明确启动回执
        G->>G: 持久化启动结果
        W->>G: 读取启动结果
        W->>C: 提交原命令对应的回执
        C->>C: 幂等确认 charging 或登记拒绝退款
        App->>U: 查询订单及充电状态
        U-->>App: 返回已确认状态
    else 付款已过期
        U->>U: 确认到账并登记自动退款，不创建启动订单
        U-->>Pay: 确认通知已处理
    end
```

实现入口：[付款回调](backend/internal/payment/payment_callback_repo.go)、[付款后启动派发](backend/internal/worker/charge/paid_start.go)、[网关启动](backend/internal/gateway/control/start.go)、[启动结果](backend/internal/charging/start/result_repo.go)。启动受理及 TCP 写入成功均不代表设备已开始充电。

### 4. 停止、结束同步与计费

用户停止经 user 调用 gateway；自动停止由 worker 触发 central 判断，再请求 gateway。预算、时长或主动停止首次确定计费截止点，迟到的物理停机不增加截止点之后的费用。设备也可自行结束并上报。

```mermaid
sequenceDiagram
    autonumber
    participant U as user / 用户端
    participant W as worker
    participant C as central
    participant G as gateway
    participant D as 设备
    alt 用户主动停止
        U->>G: 请求停止指定订单
        G->>C: 校验归属、状态并冻结计费截止
        C-->>G: 返回停止授权
    else 自动停止条件满足
        W->>C: 触发自动停止检查
        C->>G: 获取订单观测证据
        G-->>C: 返回设备观测
        C->>C: 核对预算、时长或持续零功率
        C->>G: 请求停止指定订单
        G->>C: 核对停止授权与计费截止
        C-->>G: 返回停止授权
    end
    G->>G: 保存 STOP 命令及重试状态
    G->>D: 下发停止指令
    D-->>G: 结束事件与计量
    G->>G: 持久化结束事件
    W->>G: 读取事件、核对原 START 并冻结结束请求
    W->>C: 提交结束回执及最终计量
    C->>C: 同事务保存回执、结束状态及计费任务
    C-->>W: 确认结束请求已保存
    W->>G: 释放该订单占用并标记事件
    W->>C: 派发持久计费任务
    alt 计量与价格证据有效
        C->>C: 计算费用、幂等入账、保存回执与差额退款
        C->>C: 根据已入账回执生成账单
    else 计量不足、矛盾或价格无效
        C->>C: 转人工复核，保留来源与原因
    end
    Note over U,C: 用户查询订单，分别展示确认费用与退款进度
```

实现入口：[自动停止](backend/internal/charging/stop/auto_stop.go)、[结束同步](backend/internal/worker/charge/end_sync.go)、[计费调度](backend/internal/settlement/billing.go)。持续零功率须至少 1 分钟且最新观测在 30 秒内，离线和缺失读数不作为停止证据。结束同步失败保留事件与占用；账单生成失败由补发任务恢复，已确认费用不重复入账。

### 5. 差额退款与人工退款

未用差额按原支付来源返还：在线卡余额付款退回钱包，微信付款走渠道退款。人工订单退款限定已结束的微信付款订单，申请后须双人审核；审核通过后才进入自动执行。钱包充值退款也复用渠道执行链路。

```mermaid
flowchart TD
    Surplus["结算差额 / 明确启动拒绝"] --> Source{"原支付来源"}
    Source -->|"钱包余额"| Balance["同事务退回钱包，更新流水与资金状态"]
    Source -->|"微信"| Auto["保存自动退款，预留可退额度"]
    Late["过期付款到账"] --> Auto
    Manual["人工订单退款申请"] --> Review["双人审核：冻结内容并核对实时资格"]
    Review -->|"驳回"| Rejected["保存理由，不发起退款"]
    Review -->|"通过"| Auto
    Wallet["钱包充值来源退款已获准并预留"] --> Auto
    Auto --> Dispatch["worker 触发 central 执行到期退款"]
    Dispatch --> Query["按原 refund_no 查询渠道"]
    Query -->|"查无此单"| Create["使用同一退款号创建退款"]
    Query -->|"查到结果"| Result{"核对单号、交易与金额后的结果"}
    Create --> Result
    Query -->|"超时 / 异常"| Unknown["保持 processing 与预留，退避后再查"]
    Result -->|"处理中"| Unknown
    Unknown --> Dispatch
    Result -->|"明确成功"| Success["同事务确认退款回执、累计已退金额及资金状态"]
    Result -->|"明确失败"| Failed["记录失败，释放相应预留"]
    Success --> Done["显示已到账 / 部分退款"]
    Balance --> Done
```

实现入口：[结算资金入账](backend/internal/settlement/billing_job_repo.go)、[余额退款](backend/internal/settlement/balance_refund_repo.go)、[人工审核](backend/internal/settlement/refund_review_repo.go)、[渠道退款执行](backend/internal/settlement/refund_executor_repo.go)。在途预留和已成功退款共同占用可退额度；请求受理、审核通过、`processing` 均不表示款项已退。渠道创建请求超时也进入未知结果恢复，继续查询同一退款号。

### 6. 在线卡刷卡与加时泳道

在线卡绑定用户钱包，购买方案中的时长套餐。首次刷卡生成已支付订单并走统一启动链路；同卡在充电中重刷沿用原快照追加付款和分钟数，整次加时受保护上限约束。

```mermaid
flowchart TB
    subgraph DeviceLane["用户 / 设备"]
        Swipe["放卡，设备报告刷卡"]
        Start["设备接收首次启动指令"]
        Denied["显示刷卡拒绝或余额不足"]
    end
    subgraph GatewayLane["gateway / worker"]
        Event["新报文保存事件 UUID；投递重试沿用原号"]
        Deliver["worker 提交 central 刷卡用例"]
        Send["统一付款后启动派发"]
        Reply["冻结处理结果并确认投递"]
    end
    subgraph BusinessLane["central · card / wallet"]
        Replay{"事件已处理？"}
        Original["返回原操作，不再次扣款"]
        Validate["校验事件时效、卡状态、设备及钱包"]
        Session{"端口会话与购买条件"}
        First["首次：同事务扣钱包、建支付与订单、冻结方案"]
        Extend["同卡充电中：同事务扣钱包并追加分钟数"]
        Refuse["异卡占用、余额不足、启动未确认或已到截止等：拒绝"]
    end
    Swipe --> Event --> Deliver --> Replay
    Replay -->|"是"| Original --> Reply
    Replay -->|"否"| Validate --> Session
    Session -->|"空闲且可购买"| First --> Send --> Start
    Session -->|"同卡且可加时"| Extend --> Reply
    Session -->|"不满足"| Refuse --> Denied
```

实现入口：[刷卡投递](backend/internal/worker/charge/cards.go)、[刷卡用例](backend/internal/card/swipe.go)、[加时判断](backend/internal/card/extension.go)。新事件超过 30 秒不再扣款，已处理事件可重放原结果。加时不下发新的 B7；原生报文无事件序号，实机须保证一次放卡只上报一次，移开重刷才产生新事件。

### 7. 钱包充值与原路退款

充值由服务端可信付款回调入账；钱包退款按原充值支付单拆分，并同时预留钱包可用余额与各支付单可退额度。审核和解冻是独立操作。

```mermaid
flowchart TD
    subgraph Recharge["充值入账"]
        Pay["创建充值申请与支付单"] --> Callback["渠道付款通知，验签及业务核对"]
        Callback --> Credit["同事务确认支付、增加余额、写流水与 Outbox"]
    end
    Credit --> Use["余额可用于在线卡购买"]
    Credit --> Request["用户请求钱包退款"]
    Request --> Check{"状态、可用余额与幂等请求通过？"}
    Check -->|"否"| Reject["拒绝或返回原结果"]
    Check -->|"是"| Risk{"5 分钟内第 3 次及以后？"}
    Risk -->|"是"| Frozen["登记频次冻结，转人工风控审核"]
    Frozen --> Decision{"审核结果"}
    Decision -->|"驳回"| NoRefund["保留审核结果，不派发本次退款"]
    Decision -->|"批准"| Reserve["按充值来源拆单，冻结余额并预留可退额度"]
    Risk -->|"否"| Reserve
    Decision -.->|"另需独立解冻权限"| Release["记录解冻；其他冻结仍独立生效"]
    Reserve --> Execute["复用渠道退款查询与执行链路"]
    Execute --> Result{"渠道确认结果"}
    Result -->|"成功"| Debit["扣减对应余额，释放预留并更新退款与流水"]
    Result -->|"明确失败"| Unreserve["释放对应预留"]
    Result -->|"未知 / 处理中"| Keep["保持预留，沿原退款号查询"]
    Keep --> Execute
```

实现入口：[充值回调入账](backend/internal/payment/payment_callback_repo.go)、[钱包退款](backend/internal/wallet/refund.go)、[风控审核与解冻](backend/internal/wallet/risk.go)。可用余额为 `balance_cents - frozen_cents`；风控审核批准不自动解除钱包冻结，解除一项冻结不自动解除其他冻结。

### 8. 持久任务、Outbox 与故障恢复

数据库任务推进核心业务，Outbox 负责发布已提交的事实。付款后启动、设备结束、计费与退款不依赖 Redis 通知流推进；worker 调用所属用例，不复制计费算法或资金逻辑。

```mermaid
flowchart TD
    Business["支付 / 订单 / 设备 / 资金用例"] --> Commit["同事务保存业务状态、任务或回执及自身 Outbox"]
    Commit --> Tasks["持久业务任务 / 未同步设备结果"]
    Commit --> Outbox["未发布 Outbox"]
    Tasks --> Scan["worker 扫描与派发"]
    Scan --> Execute["调用 central / gateway 所属用例"]
    Execute --> Result{"执行或回执同步结果"}
    Result -->|"已确认"| Confirm["幂等保存回执并推进状态"]
    Result -->|"依赖失败 / 结果未知"| Retry["保留原任务、单号与预留，按策略恢复"]
    Retry --> Scan
    Result -->|"证据不足"| Review["进入人工复核"]
    Outbox --> Publish["worker 发布稳定 event_id 到 Redis Stream"]
    Publish --> Mark["发布成功后更新数据库标记"]
    Mark -->|"标记提交失败"| Outbox
    Mark -->|"提交成功"| Done["发布完成"]
```

实现入口：[worker 装配](backend/internal/application/worker/app.go)、[轮询运行时](backend/internal/worker/runtime/runtime.go)、[Outbox 发布器](backend/internal/worker/outbox/publisher.go)。单体在付款确认或关键设备事件提交后立即唤醒同步，并保留每秒补扫。同步包括刷卡、待启动、启动及结束结果和 Outbox 发布；每 10 秒依次触发自动停止、计费及退款。Redis 已写入但发布标记未提交时可能重复发布，事件处理须按稳定 ID 去重。当前调度器处理器表为空，Webhook、通用 DLQ 和监管投递未装配；任务周期、Stream 用途与恢复边界见[异步任务](docs/business-contracts.md#异步任务)。

## 接口与业务约定

- 后台接口文档：`GET /api/docs/admin.openapi.json`，源文件为 [admin.openapi.json](backend/internal/http/auth/admin.openapi.json)。该文档端点未由生产 Caddy 对外代理。
- 用户接口文档：[user.openapi.json](backend/internal/http/user/user.openapi.json)，目前是静态契约文件。两份 OpenAPI 随接口变更维护，测试核对文档中的路由是否注册；实际路由见 [user](backend/internal/application/user/routes.go)、[admin](backend/internal/application/admin/routes.go)、[central](backend/internal/application/central/routes.go)。
- 普通 JSON 响应的字段构成与 `code=0` 约定见[业务与接口契约](docs/business-contracts.md#接口与权限)；微信通知、文件、指标和 OpenAPI 使用各自格式。用户与后台会话分开，内部接口使用 `X-Service-Token`。
- 金额使用整数分；用户 Snowflake ID 为十进制字符串，前端须保留精度。数据库时间按 UTC，计费日界及 DC589 本地时间按北京时间。
- 下单冻结方案与价格；启动受理、设备启动、充电结束、结算及退款到账分别确认。未知设备结果保留占用，渠道结果未知复用原退款号查询。

计价、钱包、卡、审核、权限和异步恢复的维护要求见 [AGENTS.md](AGENTS.md#数据与业务安全)，细节参考冻结的 [业务与接口契约](docs/business-contracts.md)；设备报文见 [DC589 协议](docs/protocols/dc589.md)。

## 开发与验证

宿主机需要 Go 1.27、Node.js 24 和 npm。开发 Compose 提供运行环境；后端检查与构建：

```sh
node scripts/git/install-hooks.mjs
node tasks.mjs check
node tasks.mjs build-all
# 直连开发环境（3306/6379），迁移三库并执行集成测试
node scripts/db/test-integration.mjs
```

`node tasks.mjs check` 先运行规则层检查 `node tasks.mjs check-rules`（只读文档与迁移 schema），再依次运行 `go fmt ./...`、`go vet ./...` 和 `go test ./...`（go 命令由 tasks.mjs 进入 `backend/` 执行）；pre-commit 钩子依次运行规则层检查、vet 和默认测试（经 scripts/git/pre-commit.mjs，不依赖 Make），**钩子默认未启用，克隆后须先执行 `node scripts/git/install-hooks.mjs`**。任务路径以入口文件定位，可从任意工作目录调用其绝对路径；`node tasks.mjs --help` 查看全部任务。编译产物在 `bin/`，可用 `node tasks.mjs build wattharbor` 单独构建。

任务入口覆盖 `fmt`、`lint`、`test`、`test-integration`、`build`、`build-all`、`check-rules`、`check`、`clean`、`dev-up` 和 `dev-down`。`clean` 只清理 `bin/`，`dev-down` 停止开发容器并保留数据卷。任务编排与跨平台路径测试使用 `node --test scripts/tasks.test.mjs`。

真库测试由 `integration` tag 隔离，不包含在 `node tasks.mjs check` 中。按 2026-10-06 的决定，集成测试直接使用开发环境（3306/6379/6380）：`node scripts/db/test-integration.mjs` 会起依赖服务、补 [锁观测只读授权](deploy/mysql/test-lock-observer.sql)、迁移三库并注入连接串。注意集成测试的显式主键会抬高开发库自增序号，必要时用 reset-dev 重建；直接执行 `node tasks.mjs test-integration` 须自行配置 `TEST_MYSQL_URL` 和 `TEST_REDIS_URL`。

前端命令在对应目录执行：

| 目录 | 安装 / 开发 | 检查 / 构建 |
| --- | --- | --- |
| `frontend/admin-web` | `npm ci`、`npm run dev` | `npm test`、`npm run lint`、`npm run build` |
| `frontend/miniprogram` | `npm ci`、`npm run dev:h5` 或 `npm run dev:weapp` | `npm run typecheck`、`npm test`、`npm run build:h5`、`npm run build:weapp` |

产物为 `frontend/admin-web/dist`、`frontend/miniprogram/dist/h5` 和 `frontend/miniprogram/dist/weapp`。微信开发工具导入 `dist/weapp` 并配置真实 AppID。CI workflow 已于 2026-10-06 移除：提交前在本地执行 `node tasks.mjs check`（pre-commit 钩子含规则层检查、vet 与默认测试）与 `node scripts/db/test-integration.mjs`，用户端检查需单独执行。用户端后续计划见[小程序后续工作](docs/feats/miniprogram-follow-up/spec.md)。

## 生产部署与维护

[compose.prod.yaml](deploy/compose.prod.yaml) 构建单体镜像，内嵌后台 `/admin/` 和用户 H5 `/h5/`。复制 [.env.example](.env.example) 为 `.env`，填写凭据及域名：

```sh
node scripts/ops/check-deploy.mjs
docker compose --env-file .env -f deploy/compose.prod.yaml up -d --build --wait
```

首次启动会由 Goose 初始化 `wattharbor` 单库，并为业务进程配置运行账号。微信直连需设置有效商户参数，并为 `app` 容器挂载只读私钥。已有三库、运行维护及数据备份恢复见 [运行与数据维护](docs/operations.md#单体部署)。

首次发布微信小程序前，按[微信小程序与支付上线申请](docs/operations.md#微信小程序与支付上线申请)核对主体认证、备案、经营类目、商户进件、验签模式和实机验收。

## 当前边界

- 本地模拟链路不能代替真实微信商户、实机或外部平台验收。H5 支持开发登录和模拟支付；正式微信支付与手机号授权通过微信小程序。
- 内部 Outbox 和 Redis 发布已运行；Webhook、通用 DLQ、监管投递及其管理端点尚未装配。`REGULATORY_*` 是预留变量，修改值不会启用任务。
- 微信订阅消息、电子发票签发、汇付天下、自动银行出款、自动渠道账单下载、MQTT、OTA、月卡/年卡及混合支付未接入。多主体分账、提现与对账已移除。
- 公告列表只返回全局公告；站点详情另包含本站生效公告，城市定向尚未接线。

商业授权以签订合同为准。
