# charge-pilot 分层重构方案

> 状态：提案，2026-10-02 起执行。采纳后的结构、依赖规则与维护口径已并入
> [AGENTS.md](AGENTS.md)；本文件只记录**为什么这样改**、**分几步改**、**还欠什么**。
> 执行过程中每完成一个阶段即更新本文件的进度段。

---

## 第一部分：为什么推翻上一版结构

### 1.1 上一版做成了什么

第一轮重构（REFACTOR-PLAN.md 原版本，2026-10-02 完成）按**表前缀家族**把
`internal/central/charge` 拆成九个包，并按文件后缀分层。成果本身有价值：

- **表所有权确立**——每个包独占自己的表，事务边界跟着表走，不跟着"用户侧 / 后台侧"走。
- **worker 跨库直写全部切断**——16 处直写改走 HTTP，资金写入收敛到 central 单库事务内。
- **命名归一**——`admin_user_role → admin_user`、`charge_user.read → user.read`、
  `admin/charge-users → admin/users`、worker_db 环境变量统一。
- **20 个 bug 修完**——含 DLQ 语义、跨月重复入账、错误码一码三义、SSRF 放行条等。

### 1.2 为什么不继续用它

按表前缀拆包是**技术架构的内核视角**。改一次业务要在 3–5 个包之间跳：

| 一个充电订单生命周期 | 在哪 |
|---|---|
| 订单状态推进 | `central/order/` |
| 支付意图与回调 | `central/payment/` |
| 账单出具 | `central/settlement/` |
| 退款派发 | `central/refund/` |
| 用户侧编排 | `central/charge/` |

叠加文件后缀分层后，每包内部再切一层（`*_http.go` / `*_store.go` / 领域文件），
于是**要维护两套坐标系**：既是若干个子域，每个子域内又分三层。实测数据：

```
charge 包  触及 21 张表
admin  包  触及 45 张表
```

`admin` 直查 45 张表意味着后台绕过家族包直接写 SQL——表所有权名义上确立了，
实际上没落地。这是"看不懂"的根因，不是命名不好。

### 1.3 改用什么

改为**分层坐标全局唯一**：分四层，层与层之间的依赖方向一次定死，
任何人找代码只有四个落点，不再有第二套坐标系。

```
找业务规则   →  internal/domain/
找用例编排   →  internal/application/
找 SQL 与外部系统 →  internal/infrastructure/
找路由与鉴权 →  internal/interface/
```

分层方案参考 [go-ddd-blueprint](https://github.com/patrickkdev/go-ddd-blueprint)
的 layer 划分，但其 domain 层假设的是单体小项目（单包十几个文件）。本仓库有
81 张表、五个部署单元，直接照搬会退化成第二个 `admin`。因此 domain 层内部
**按业务子域分子目录**——既保住 DDD 的子域边界，又不让 domain 变成大杂烩。

---

## 第二部分：结构定稿

### 2.1 五个子域

| 子域 | 聚合 | 业务范围 |
|---|---|---|
| `identity` | user / session / auth / phone | C 端账号、会话、登录、手机号 |
| `charging` | order / session / billing / stop / scheme | 充电订单、充电过程、计量、停机、计费方案 |
| `money` | payment / wallet / refund / coupon / card / settlement / channel | 支付、余额、退款、券、在线卡、分账、渠道 |
| `devices` | station / device / vendor / fault / telemetry | 站点、设备资料、厂商、故障、遥测（协议无关） |
| `operations` | role / audit / announcement / export / webhook / alert / whitelabel | 运营权限、审计、公告、导出、订阅、告警、白标 |

子域目录名用业务概念，**不是表名也不是集群名**。`money` 而不是 `billing_db`，
`devices` 而不是 `device_cluster`。

### 2.2 四个决定

| 问题 | 决定 | 理由 |
|---|---|---|
| domain 分子域还是平铺 | **分子域** | 81 张表平铺必然退化成第二个 admin；blueprint 的小项目假设不适用 |
| application 平铺还是分子域 | **平铺单包** | 用例高度互联，分子目录易生环——blueprint 的经验在本仓库成立 |
| protocol 放在哪 | **与 domain 平级的独立层** | 被 gateway 用于编解码、被 central 用于语义翻译，且不依赖任何业务代码 |
| config 放在哪 | **提为 `config/`，不入 platform** | 配置是进程装配的一部分，不是通用能力 |

完整目录树（展开到文件级，含 worker / delivery / gateway / platform 各层）
见 [AGENTS.md](AGENTS.md#工具与代码风格)。本文件不重复维护，避免出现两份结构图。

### 2.3 protocol 独立成层的理由

设备协议是**跨进程的一等语言**，不是"外部系统适配"：

- `gateway` 用它做帧编解码与会话管理
- `central` 用它把故障码、单位换算翻译成领域语义
- 它不依赖任何业务代码，被 `domain` 与 `infrastructure` 同时视为上游约束

`protocol.Adapter` 已经是为多协议设计的抽象（`Name()` + `ServeConn`），
`protocol.Event` 也已协议无关。**加一个新协议只需在 `protocol/<name>/` 下实现
`Adapter`，上层一行不改。**

当前唯一的泄漏点：`internal/central/order/authorization.go:79` 直接调用
`dc589.CardBalanceUnits(cents)`。换第二个协议这行就要改，而它在一个业务文件里。
阶段 0 修掉。

### 2.4 目标目录结构（强制）

新增或迁移任何文件都必须落在这张图里。图中没有的位置，先改这张图再改代码——
它与 [AGENTS.md](AGENTS.md#工具与代码风格) 中的同一份保持一致，两处同步维护。

```text
charge-pilot/
│
├── cmd/                                        部署单元
│   ├── user/main.go                            C 端接口进程      :8080
│   ├── admin/main.go                           后台接口进程      :8081
│   ├── central/main.go                         用例编排进程      :8082
│   ├── gateway/main.go                         设备接入进程      :8083 + TCP :9100
│   ├── worker/main.go                          消费调度进程      :8085
│   ├── migrate/main.go  devseed/main.go  simulator-dc589/main.go
│
├── config/                                     配置装载（由 platform/config 提上来）
│   ├── user.go  admin.go  central.go  gateway.go  worker.go
│
├── internal/
│   │
│   │ ═══ 业务核心 ═══════════════════════════════════════════
│   │
│   ├── domain/                                 零 gorm、零 gin、零 Redis
│   │   │
│   │   ├── identity/
│   │   │   ├── user.go                         User / Profile / ErrUserFrozen
│   │   │   │                                   UserRepository 接口
│   │   │   ├── session.go                      Session / ErrInvalidRefresh
│   │   │   │                                   SessionRepository 接口
│   │   │   ├── auth.go                         登录授权规则、令牌签发
│   │   │   └── phone.go                        手机号绑定规则、号码占用
│   │   │
│   │   ├── charging/
│   │   │   ├── order.go                        ChargeOrder：状态机、方案冻结
│   │   │   ├── session.go                      充电过程：启动/结束/回执
│   │   │   ├── billing.go                      计量、截止点、账单出具
│   │   │   ├── stop.go                         停机判定
│   │   │   └── scheme.go                       计费方案、计量证据校验
│   │   │
│   │   ├── money/
│   │   │   ├── payment.go                      支付单、意图、回调核验
│   │   │   ├── wallet.go                       余额、流水、冻结
│   │   │   ├── refund.go                       退款单、双人复核、到账
│   │   │   ├── coupon.go                       券、活动规则、核销
│   │   │   ├── card.go                         在线卡、刷卡、扣款
│   │   │   ├── settlement.go                   分账、结算、提现
│   │   │   └── channel.go                      支付渠道契约
│   │   │
│   │   ├── devices/                            协议无关
│   │   │   ├── station.go  device.go  vendor.go
│   │   │   ├── fault.go                        故障语义（烟雾/高温/离线）
│   │   │   └── telemetry.go                    遥测（功率、电量、时长）
│   │   │
│   │   └── operations/
│   │       ├── role.go  audit.go  announcement.go
│   │       ├── export.go  webhook.go  alert.go  whitelabel.go
│   │
│   │ ═══ 用例编排 ═══════════════════════════════════════════
│   │
│   ├── application/                            扁平单包，跨聚合事务在此
│   │   ├── user_service.go                     建档、冻结、档案
│   │   ├── auth_service.go                     登录、刷新、登出
│   │   ├── recharge_service.go                 充值入账
│   │   ├── charge_service.go                   下单、扫码、刷卡
│   │   ├── start_service.go                    启动判定、失败退余额
│   │   ├── end_service.go                      结束计量、费用核算
│   │   ├── refund_service.go                   自动退款、人工复核
│   │   ├── settlement_service.go               分账、提现
│   │   └── *_test.go
│   │
│   │ ═══ 外部依赖实现 ═══════════════════════════════════════
│   │
│   ├── infrastructure/
│   │   ├── database/                           gorm 只在这里
│   │   │   ├── client.go                       连接池
│   │   │   ├── user_repository.go              user / user_login_identity
│   │   │   ├── order_repository.go             charge_order 家族
│   │   │   ├── payment_repository.go           payment_order 家族
│   │   │   ├── wallet_repository.go            wallet_* 家族
│   │   │   ├── refund_repository.go            refund_* 家族
│   │   │   ├── settlement_repository.go        settlement / split / withdraw
│   │   │   ├── station_repository.go           station
│   │   │   ├── device_repository.go            device_meta / device_import
│   │   │   └── admin_repository.go             role / permission / audit_log
│   │   ├── redis/
│   │   │   ├── session_store.go                user:session: / admin:session:
│   │   │   ├── stream.go                       event_outbox 消费
│   │   │   └── idempotent.go                   幂等凭据
│   │   ├── wechat/
│   │   │   ├── client.go  pay.go  refund.go
│   │   └── gateway_client/                     内部 HTTP 客户端
│   │       └── client.go
│   │
│   │ ═══ 协议适配 ═══════════════════════════════════════════
│   │
│   ├── interface/
│   │   ├── http/
│   │   │   ├── user_api.go                     /api/v1/user/*      → cmd/user
│   │   │   ├── public_api.go                   /api/v1/user/auth/* → cmd/user
│   │   │   ├── admin_api.go                    /api/v1/admin/*     → cmd/admin
│   │   │   ├── internal_api.go                 /api/v1/internal/* → cmd/central
│   │   │   ├── device_api.go                   设备端点            → cmd/gateway
│   │   │   ├── worker_api.go                   运维端点            → cmd/worker
│   │   │   ├── middleware.go                   鉴权、权限、数据范围
│   │   │   └── error_mapper.go                 领域错误 → HTTP
│   │
│   │ ═══ 协议契约（独立层，三进程共享）═══════════════════════
│   │
│   ├── protocol/
│   │   ├── adapter.go                          Adapter / Sink / Event / Command
│   │   ├── registry.go                         会话注册表
│   │   ├── listener.go                         监听与协议分派
│   │   ├── session_audit.go                    收发字节审计
│   │   ├── dc589/
│   │   │   ├── adapter.go                      实现 protocol.Adapter
│   │   │   ├── frame.go  codec.go              帧编解码、校验
│   │   │   ├── command.go                      启动/停止/刷卡指令
│   │   │   ├── measurements.go                 电量、功率、时长解析
│   │   │   ├── log_data.go                     日志帧解析
│   │   │   ├── fault.go                        0xBB 烟雾 → 领域语义
│   │   │   └── units.go                        分 → 协议余额单位
│   │   └── <新协议>/                            将来只加目录
│   │
│   │ ═══ 进程特有编排 ═══════════════════════════════════════
│   │
│   ├── worker/                                 事件消费、定时、投递编排
│   │   ├── consumer/
│   │   │   ├── charge_events.go                消费 charge_events_stream
│   │   │   ├── charge_ended.go                 消费 charge_ended_stream
│   │   │   ├── device_event.go                 消费 device_event_stream
│   │   │   ├── refund_required.go              消费 refund_required_stream
│   │   │   └── refund_succeeded.go             消费 refund_succeeded_stream
│   │   │
│   │   ├── sync/                               命令与结果同步
│   │   │   ├── start_sync.go                   启动回执同步（192 行）
│   │   │   ├── end_sync.go                     结束事件同步（187 行）
│   │   │   ├── card_sync.go                    刷卡决策同步（237 行）
│   │   │   ├── paid_start.go                   已支付订单启动（109 行）
│   │   │   ├── meter_segments.go               计量分段上报（82 行）
│   │   │   └── client.go                       gatewaySyncAPI 封装（64 行）
│   │   │
│   │   ├── trigger/                            薄触发器，只发意图
│   │   │   ├── auto_stop.go                    触发停机扫描（36 行）
│   │   │   ├── alert_sync.go                   触发告警同步（37 行）
│   │   │   ├── billing_dispatch.go             触发账单下发（16 行）
│   │   │   └── refund_dispatch.go              触发退款派发（16 行）
│   │   │
│   │   ├── outbox/
│   │   │   ├── publisher.go                    事件写入 outbox（88 行）
│   │   │   └── dlq.go                          死信与重放（310 行）
│   │   │
│   │   ├── schedule/
│   │   │   ├── scheduler.go                    cron 解析与执行（206 行）
│   │   │   └── http.go                         调度运维端点（121 行）
│   │   │
│   │   └── ops/                                worker 自身运维
│   │       ├── ops_api.go                      积压、死信、重放（106 行）
│   │       └── http.go                         内部 HTTP 出口（47 行）
│   │
│   ├── delivery/                               外部投递实现
│   │   ├── events.go                           Event 契约与校验（129 行）
│   │   ├── webhook.go                          订阅匹配、签名、外投（332 行）
│   │   ├── regulatory.go                       监管报送签名与发送（90 行）
│   │   └── senders.go                          HTTP 发送器（76 行）
│   │
│   ├── gateway/                                设备接入运行时
│   │   ├── listener/
│   │   │   └── server.go                       监听 endpoint、连接分派
│   │   │
│   │   ├── session/
│   │   │   ├── session.go                      连接会话（144 行）
│   │   │   └── registry.go                     设备到会话的索引
│   │   │
│   │   ├── command/
│   │   │   ├── start.go                        启动指令（68 行）
│   │   │   ├── authorize.go                    启动授权（116 行）
│   │   │   ├── scan.go                         扫码（51 行）
│   │   │   ├── cards.go                        刷卡（49 行）
│   │   │   ├── user_stop.go                    用户主动停机（112 行）
│   │   │   └── charge_process.go               充电进程（107 行）
│   │   │
│   │   ├── telemetry/
│   │   │   ├── telemetry.go                    遥测入口（252 行）
│   │   │   ├── charging_samples.go             充电采样（161 行）
│   │   │   ├── device_faults.go                故障原始载荷（146 行）
│   │   │   ├── device_summary.go                设备摘要（78 行）
│   │   │   ├── historical_curve.go             历史曲线（90 行）
│   │   │   └── worker_sync.go                  worker 同步端点（574 行）
│   │   │
│   │   ├── compensate/
│   │   │   ├── compensate.go                   迟到回执补偿（91 行）
│   │   │   └── backfill.go                     历史数据回填（276 行）
│   │   │
│   │   ├── store/                              gateway_db 持久化
│   │   │   ├── mysql.go                        连接池（328 行）
│   │   │   ├── gorm_models.go                  记录类型（99 行）
│   │   │   ├── session.go                      会话落库（85 行）
│   │   │   ├── charge_start.go                 启动与回执（197 行）
│   │   │   ├── charge_process.go               充电进程（121 行）
│   │   │   ├── user_stop.go                    停机落库（125 行）
│   │   │   ├── scan.go                         扫码会话（79 行）
│   │   │   ├── aggregate.go                    聚合读（68 行）
│   │   │   └── compensation.go                 补偿台账（107 行）
│   │   │
│   │   ├── provision/                          开通与厂商（gateway 侧）
│   │   │   ├── http.go                         开通端点（131 行）
│   │   │   └── vendors.go                      厂商同步（339 行）
│   │   │
│   │   └── simulator/                          联调工具，不参与生产
│   │       └── dc589/
│   │           ├── simulator.go                 模拟器主体（859 行）
│   │           ├── behavior.go                  行为脚本（646 行）
│   │           └── tui.go                       终端界面（462 行）
│   │
│   └── platform/                               无业务语义的通用能力
│       ├── config/config.go                    100 行 → 移到 config/
│       ├── dbconn/mysql.go                     83 行  连接池与 DSN
│       ├── dbutil/dbutil.go                    34 行  UTCDate / 重复键判定
│       ├── httpapi/
│       │   ├── gin.go                          113 行  引擎、信封、ReadPaging
│       │   ├── codes.go                        36 行   错误码常量
│       │   ├── decimal_id.go                   42 行   Snowflake 序列化
│       │   └── metrics.go                      72 行   指标中间件
│       ├── auth/jwt.go                         98 行   签发与校验
│       ├── snowflake/snowflake.go              88 行   ID 分配
│       ├── netguard/guard.go                   92 行   出站 SSRF 防护
│       ├── phone/phone.go                      24 行   规范化、脱敏
│       └── serviceclient/client.go             115 行  内部 HTTP 客户端
│
├── migrations/{central_db,gateway_db,worker_db}/
├── bin/                                        编译产物（已忽略）
├── admin-web/  miniprogram/
└── Makefile  go.mod  AGENTS.md  README.md
```

---

## 第三部分：依赖规则

```
domain          →  无
protocol        →  无（契约自足）
application     →  domain
infrastructure  →  domain
interface       →  domain + application
worker/gateway  →  protocol + domain + application + infrastructure
cmd/*           →  interface + application + infrastructure + platform
```

三条禁止，由 `scripts/check/layers.sh` 机检：

1. `domain` 与 `protocol` 内不得出现 gorm / gin / redis
2. `domain` 与 `protocol` 互不 import（平行层）
3. 除 `worker/` 与 `gateway/` 外任何包不得 import `protocol`

**护栏的教训**（第一轮写护栏时踩的，写下来免得重犯）：

- 表名清单用 `sed` 提取而非 `grep -oE '[a-z_]+$'`——后者在 BSD grep 上锚定行为与
  Linux 不一致，曾产出只有一行的清单，规则等于没生效却一直"绿着"。
- 表名只认 SQL 构造位置（`Table(` / `FROM` / `JOIN` / `INTO` / `UPDATE` 后面），
  否则 `StopUser = "user"` 这类枚举值会被误判成表访问。
- **护栏没被证伪过就等于没有护栏。** 新规则上线必须拿真实代码跑一遍，
  确认它既报得出真问题、也不误报。

---

## 第四部分：进程拆分

| 进程 | 暴露面 | 监听 | 注册的 interface 路由 |
|---|---|---|---|
| `user` | 对外 | `:8080` | `/api/v1/user/*`、`/api/v1/user/auth/*` |
| `admin` | 对外 | `:8081` | `/api/v1/admin/*` |
| `central` | 对内 | `:8082` | `/api/v1/internal/*` |
| `gateway` | 对内 + 设备 | `:8083`、DC589/TCP `:9100` | 设备端点 |
| `worker` | 对内 | `:8085` | 运维端点 |

五个进程**共享同一套 `domain` / `application` / `infrastructure`**，差别只在
各自注册哪些 `interface` 路由。进程拆开不等于代码复制一份。

C 端登录从 central 移到 user，路径由 `/api/v1/public/auth/*` 统一为
`/api/v1/user/auth/*`；微信支付回调同步移入 user。

**为什么 central / gateway 拆出来还不够**：`admin` 与 `user` 有三个 gateway
内部端点重叠（`device-summaries`、`devices/{id}`、`charge-orders/{no}`）。
拆分前后都存在，暂不处理——它是既有的数据范围问题，不是分层问题。

---

## 第五部分：执行阶段

每阶段独立可发布、可回滚，结束跑 `make check`（秒级，可随时运行）。
**新代码只往新结构写，旧包只允许删不允许改。**

### 阶段 0：protocol 提升 + 修泄漏

- `gateway/listener`、`gateway/session/registry` 从 `protocol` 下沉到 `gateway`——
  连接分派与设备索引是 gateway 的事，不是跨进程契约
- 修 `central/order/authorization.go:79` 的 `dc589.CardBalanceUnits` 调用：
  余额单位换算提升为协议层通用概念，业务侧只传分
- `make layers` 改写为能识别分层结构

规模 14 文件，风险低。**这一阶段做完能立刻验证结构判断是否成立**，后续阶段
的可行性都取决于它。

### 阶段 1：platform 补齐

- 服务令牌校验收进 `platform/serviceclient`（当前全仓 25 处 `ConstantTimeCompare` 复制）
- 分页解析全部收进 `httpapi.ReadPaging`（当前 4 套并存，边界不一致）
- `httpapi` 补 `FieldErr` 接线（结构已定义但全仓零写入）
- 修 `admin/exports.go:510,523` 的 HTTP 410 借用 1005 码值

规模 12 文件，风险低。

### 阶段 2：identity 迁移

`internal/central/identity` → `domain/identity` + `infrastructure/database/user_repository.go`
+ `interface/http/user_api.go`。

第一轮已把它拆成 `user` / `session` / `auth` / `phone` 四个聚合，与
`domain/identity/` 的聚合划分一一对应，**沿用不推翻**。

规模 11 文件，风险低。

### 阶段 3：进程拆分

`user` / `admin` 独立，`central` 收缩为内部服务。动 `compose.dev.yaml`、
生产 compose、Caddy 路由、`admin-web` 的 baseURL 与 miniprogram 的 API 地址。

**这是唯一会碰到前端的阶段**，单独排。

规模：大，风险中。

### 阶段 4：charging 迁移

`central/order` + `central/charge` + `central/pricing` 的领域部分
→ `domain/charging/` + `application/` + repository。

规模 15 文件，风险中。

### 阶段 5：money 迁移

`central/payment` + `wallet` + `refund` + `coupon` + `card` + `settlement` + `channel`
→ `domain/money/` + `application/` + repository。

规模 20 文件，风险中。

### 阶段 6：devices + operations

`central/admin`（15405 行 / 45 张表）与 `central/devices` 相关表
→ `domain/devices/` + `domain/operations/` + repository + `admin_api.go`。

后台读写必须经 `application` 用例，**不得沿用现在直查 45 张表的写法**。
后台列表查询（分页、关键词、数据范围过滤）全部要在 application 层新建。

规模 40 文件，风险高，拆多个 PR。

### worker / gateway 重组（最后做）

`worker/charge` 一个包塞了 7 文件 947 行，混着同步、触发、客户端，
拆成 `consumer` / `sync` / `trigger` / `outbox` / `schedule` / `ops`。
`gateway` 补 `listener` / `session` 目录。

**放在最后**——它们是进程编排，等领域层稳定后再按新边界重组，否则要返工两次。

---

## 第六部分：遗留技术债

第一轮登记、未纳入批次的项，与本次分层重构**不冲突**，可并行处理：

| 编号 | 问题 | 现状 |
|---|---|---|
| P1-1 | 服务令牌校验逐字复制 20+ 份 | 未收敛为共享助手 → 阶段 1 |
| P1-2 | 分页解析 4 套并存 | `httpapi.ReadPaging` 已落地，`admin/parsePage` 与 `charge/user_query` 仍是独立实现 → 阶段 1 |
| P1-3 | 幂等机制 4 套并行（`LAST_INSERT_ID` 17 处） | 仅随家族包局部收敛 |
| P1-4 | `httpapi` 错误码 | `codes.go` 已建，1xxx/2xxx/5xxx 段已分；`exports.go` 的 410 仍借用 1005；`FieldErr` 零写入 → 阶段 1 |
| P1-5 | `identity/http.go` profile 复写鉴权链 | 未处理；阶段 2 迁移时顺带收进 `SessionAuthenticator` |
| B3 | DLQ 自动消费循环 | `ConsumeBatch` 生产零调用，运维查询与重放已可用。接线前需先定各流的消费组归属，否则会双重投递 |

**worker 是否保留为独立进程**：保留。前几轮重构后它已不接触任何业务表
（停机判定、告警判定、退款入账都移回了 central），只剩消费事件流、跑定时任务、
转发 HTTP 三件事。若并入 central，1 秒级消费循环与 30 秒退避任务会挤占
HTTP 响应，且 cron 调度（618 行）会把 central 的包撑大——而那正是要拆的对象。

---

## 第七部分：未验证的边界

1. **未做实机与真实资金验收。** 全部结论来自源码、schema、单元测试与
   `PAYMENT_MODE=simulation` 的模拟器结果。
2. **无集成测试（2026-10-02 决定）。** 仓库已删除 67 个 `*_integration_test.go`
   与 `scripts/test/`，只保留单元测试。代价是事务原子性、行锁并发、唯一键冲突
   与幂等重放**没有自动化覆盖**——这类行为编译器和单元测试都测不出，只能靠
   开发栈联调与人工核对。改动资金与并发路径时必须实际走一遍流程。
3. **`make layers` 的旧规则仍在按第一轮形态判定 `internal/central/*`。** 随
   迁移推进逐包撤销对应条目。

---

## 第八部分：维护约定

- **目录结构有两份**——本文件 2.4 节与 [AGENTS.md](AGENTS.md#工具与代码风格) 各一份，
  内容必须逐行一致。改任何一处都要同步另一处。
- 两份树是**目标状态**，当前代码尚未迁移到位（见第七部分第 3 条）。迁移过程中
  旧路径会暂时存在，不因本图而回退已完成的阶段。
- 阶段完成后更新本文件第五部分对应小节：把「计划」改写为「已做」并记录
  实施中发现的计划外裁决，与第一轮的执行进度记录方式一致。
- 结构图不是装饰。新增文件前先在图里找到它的位置；图里没有，说明缺的是
  一次结构决策，不是随手新建目录。
