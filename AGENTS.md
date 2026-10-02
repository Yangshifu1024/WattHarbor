# 仓库维护规则

## 依据与边界

- 以当前源码、路由注册、数据库初始化 SQL、Compose 配置和测试为实现依据。README 记录运行方式与业务契约；本文件记录维护规则。
- `cmd/` 只负责配置、依赖装配与进程生命周期，每个子目录是一个部署单元。业务实现按分层位于 `internal/domain`、`internal/application`、`internal/infrastructure`、`internal/interface`；进程特有编排位于 `internal/worker`、`internal/gateway`、`internal/delivery`；设备契约位于 `internal/protocol`；无业务语义的通用能力位于 `internal/platform`。
- 部署单元分两类。**对外**：`user`（C 端，`:8080`）、`admin`（运营后台，`:8081`）。**对内**：`central`（用例编排与内部端点，`:8082`）、`gateway`（设备接入，`:8083` 与 DC589/TCP `:9100`）、`worker`（事件消费、定时任务、外部投递，`:8085`）。对内端口不对外映射。五个进程共享同一套 `domain`/`application`/`infrastructure`，差别只在各自注册哪些 `interface` 路由——进程拆开不等于代码复制。
- 业务状态与资金写入由所属领域执行。worker 复用已定义的内部接口、任务与幂等记录，不复制计费算法或资金入账逻辑。
- 公共 HTTP 处理使用 `internal/platform/httpapi`；服务间 HTTP 调用使用 `internal/platform/serviceclient`。设备适配器实现 `protocol.Adapter`，协议解析与数据库访问分离。
- 环境变量变更须核对配置加载、进程使用、Compose 注入和模板；仅在 `.env` 中定义不能视为已生效。

## 工具与代码风格

- Go 版本以 `go.mod` 为准，当前为 Go 1.27；使用其标准库与语言能力，不为旧编译器改写代码。
- 后端检查直接使用 Makefile：`make fmt` 调用 `go fmt ./...`，`make lint` 调用 `go vet ./...`，`make test` 调用 `go test ./...`，`make layers` 调用 `scripts/check/layers.sh`；`make check` 顺序执行四项。
- 目录结构定稿如下。新增或迁移文件时同步维护本图，括号内注明迁移前的位置：

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
│   │   │   ├── user/                           用户聚合（已落地）
│   │   │   │   ├── user.go                     User / Profile / ErrUserFrozen
│   │   │   │   └── auth.go                     访问令牌校验、账号状态哨兵
│   │   │   └── auth/                           登录身份来源
│   │   │       ├── wechat.go                   微信小程序 code 换取
│   │   │       └── development.go              开发模式身份
│   │   │   （session 与 phone 的规则尚未迁入）
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
│   │   └── operations/                         运营后台的业务概念
│   │       ├── account.go                      后台账号身份、权限清单、退役权限
│   │       ├── scope.go                        数据范围与字段遮蔽
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
│   │   │   ├── admin_repository.go             role / permission / audit_log
│   │   │   ├── admin_account.go                admin_user 账号与登录判定
│   │   │   └── admin_scope.go                  admin_data_scope / admin_field_mask
│   │   ├── redis/
│   │   │   ├── session_store.go                user:session:
│   │   │   ├── admin_session.go                admin:session: / admin:mfa:
│   │   │   ├── session_token.go                刷新令牌布局与共用原语
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
│   │   │   ├── admin_auth.go                   后台鉴权与会话入口
│   │   ├── admin_mfa_settings.go           自助 MFA 开通与关闭
│   │   ├── admin_api.go                    /api/v1/admin/*     → cmd/admin
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
│       ├── auth/totp.go                                TOTP 与随机密钥
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

- 代码按分层组织，依赖单向、无例外：

  ```
  domain          →  无
  protocol        →  无（契约自足）
  application     →  domain
  infrastructure  →  domain
  interface       →  domain + application + infrastructure
  worker/gateway  →  protocol + domain + application + infrastructure
  cmd/*           →  interface + application + infrastructure + platform
  ```

  真正被约束的是**方向**：`interface` 允许直接持有仓储（`infrastructure`），
  因为处理器本身就是编排的最后一层，再往里包一层接口只会平移代码。
  被禁止的是反向——`domain`、`application`、`infrastructure` 一律不得 import `interface`。
  `domain` 与 `protocol` 是平行层，互不 import。除 `worker/` 与 `gateway/` 外任何包不得 import `protocol`。这三条由 `make layers` 机检，违反即失败。

- `internal/domain/` 是业务核心，零 gorm、零 gin、零 Redis。子域目录用业务概念命名，不是表名或集群名：

  ```
  domain/
    identity/    user/（user.go auth.go）  auth/（wechat.go development.go）
    charging/    order.go  session.go  billing.go  stop.go  scheme.go
    money/       payment.go  wallet.go  refund.go  coupon.go
                 card.go  settlement.go  channel.go
    devices/     station.go  device.go  vendor.go  fault.go  telemetry.go
    operations/  account.go  scope.go  role.go  audit.go  announcement.go
                 export.go  webhook.go  alert.go  whitelabel.go
  ```

  文件名即业务对象。`order.go` 就是订单，不按技术层命名。仓储接口定义在 domain，实现见 `infrastructure/database/`；状态机、金额与计量规则、纯计算留在 domain，不 import 任何框架，可独立测试。

- `internal/application/` 扁平单包，一个用例一个文件，跨聚合事务在此。分子目录容易生环，用例之间高度互联，扁平优于分组。

- `internal/infrastructure/` 按外部系统分子包：`database/`（gorm 只在此处，数据库表名只在此处）、`redis/`、`wechat/`、`gateway_client/`。

- `internal/interface/http/` 按服务分文件：`user_api.go`（`/api/v1/user/*`）、`public_api.go`（`/api/v1/user/auth/*`）、`admin_api.go`（`/api/v1/admin/*`）、`internal_api.go`（`/api/v1/internal/*`）、`device_api.go`、`worker_api.go`。`gin.Context` 只在此处出现；`middleware.go` 管鉴权与数据范围，`error_mapper.go` 把领域错误映射为 HTTP 状态。

- `internal/protocol/` 是 gateway 与 central 共享的设备契约层，与 `domain` 平级，既不属于 `domain` 也不属于 `infrastructure`：
  - 根包只放跨进程契约：`Adapter`（`Name` + `ServeConn`）、`Sink`、`Event`、`Command`、`Registry`、`Listener`。
  - 每个协议一个子目录实现 `Adapter`，当前为 `dc589/`。帧格式、校验、指令编解码、故障码语义（`0xBB` 烟雾）、单位换算（分 → 协议余额单位）全部封闭在该目录内。
  - **协议码值、帧结构、校验算法一律不得出现在 `domain/` 与 `application/`。** 业务代码出现 `0xBB`、`dc589.` 前缀或帧相关字样即违反本约定。
  - **新增协议必须新开子目录实现 `Adapter`，不得修改 `protocol` 根包。** 若做不到，说明抽象有缺陷，应改抽象而不是把新协议逻辑塞进根包。
  - 设备领域模型对所有协议保持一致；加协议不产生新的设备类型，只产生新的语义翻译。
  - `ServeConn(context.Context, net.Conn, Sink)` 假设"一条长连接 = 一个设备会话"。若将来某协议走 HTTP 轮询，要改的是 `Adapter` 接口本身，不是各协议实现——那是接口演进，不要为想象中的协议提前过度抽象。
- 前端使用 Node.js 24、npm 和已提交的 `package-lock.json`。以 `npm ci` 安装，保留 `miniprogram/.npmrc` 的依赖配置。
- Go 注释用严肃、精炼的语言说明行为、单位、约束、并发关系或错误条件。复杂事务和协议解释保留必要细节；删除口语、重复说明、操作过程和失效历史。保留 `go:embed` 等编译指令及 `Deprecated:` 标记。
- 不提交构建产物、依赖目录、真实配置、凭证、数据库备份或临时脚本；临时文件置于 `.tmp/`。

## 数据与计算

- 金额以整数分传输和保存；费率及中间精度沿用 `internal/domain/charging/scheme.go` 的十进制计算与舍入规则。不得使用浮点金额或另建前端计费算法。
- 电量、功率和时长必须注明单位。设备 `0.1 W`、`mWh`、`Wh`、分钟和秒按协议字段转换；`1 Wh = 1000 mWh`，`1 kWh = 1000 Wh`。
- 用户 Snowflake ID 在数据库中保留 `BIGINT UNSIGNED`，公开 API、Redis 会话和前端采用十进制字符串。使用 `httpapi.DecimalID`；前端不得将大 ID 转为 JavaScript `Number`。
- 编号分配与业务记录共用事务；重试复用已持久化编号。保留历史业务编号，不重新生成或批量改写。
- 数据库连接按 UTC 解析时间；业务日界与 DC589 本地时间使用北京时间。转换必须显式指定时区，不依赖宿主机时区。
- 订单计费使用创建支付意图时冻结的完整方案、套餐及实际计量；刷卡在首次扣款事务中冻结。不得读取当前模板重算历史订单。跨费率缺少边界实测读数时进入计量复核，不按时间均摊电量或插值补造证据。
- 缺失遥测、金额、关联信息以 `null` 或明确状态表达，不用零值伪装为已测量、已结算、已到账或端口空闲。
- 手机号存入 `central_db.user.phone` 明文列，未绑定为 `NULL`，由唯一索引约束号码归属。复用 `internal/platform/phone` 的规范化、校验和展示脱敏；正式绑定须使用微信验证结果。

## 状态、幂等与审计

- 对余额、支付、退款、订单、端口归属和配额的并发更新使用既有事务与行锁顺序。同库的状态变更、幂等凭据、审计及 Outbox 应与业务写入同事务提交。
- 同一请求标识绑定同一业务内容；一致重放返回原结果，内容或身份冲突返回冲突。网络重试沿用原 `request_id`、命令 ID、支付单号和退款单号。
- 支付客户端成功、启动请求受理、STOP 回执、结束计量、费用核算和退款到账是不同事实；按持久化回执推进各自状态，不提前确认后续环节。
- 微信通知先验签、解密并核对商户、应用、订单身份、金额及渠道交易号，再入账。未知渠道结果保留查询与预留资金，不换号重提。
- 人工退款、计量分段修订和发票通过保留不同人员复核约束，第二签核对前一审核人的实时权限及原快照。人工最终金额结算沿用现有授权、实付上限、原因及审计约束；分段修订保持原总量与冻结来源，不能补造截止计量。
- 审计记录操作人、目标、请求标识及必要的前后快照；不记录密码、JWT、服务密钥、TOTP 密钥或验证码。设备日志保留关联编号和解析信息，不泄露卡号、余额、SIM 标识、面板密码或原始敏感报文。

## 权限与接口

- 后台授权实时读取有效账号、角色和权限；JWT 及前端菜单只表示会话或展示，不代替服务端授权。
- 站点、设备、厂商及配置操作沿用服务端数据范围检查。列表与详情、导出、写入和批量操作使用一致的授权条件；客户端筛选不能扩大范围。
- 用户资源以当前会话校验归属；无权访问与不存在统一按既有接口返回。服务间接口使用 `X-Service-Token`，前端不得持有服务令牌。
- 修改 API 时同步检查调用方、DTO、输入约束、错误状态和 `internal/interface/http/openapi.json`。统一响应保留 `code/message/data/request_id/trace_id`；微信通知沿用渠道规定的 HTTP 确认响应。
- 维持乐观版本检查和幂等标识。前端刷新令牌不能改变业务请求身份；账号切换后的旧请求不得覆盖新会话状态。

## 设备协议

- 当前新建设备使用 DC589/TCP。新增协议在独立适配器中实现，并补齐协议帧、能力声明和模拟器验证，不在现有解析器中猜测兼容。
- 帧长度、校验、字节序、字段范围和可选负载均须严格验证。十进制 BCD 字段校验每个半字节；模块/SIM 标识保留既有非十进制半字节兼容，不按十进制统一拒绝。启动和停止回执匹配设备、端口及六字节命令会话，连接会话与命令会话不得混用。
- 设备事件先持久化，再确认上行；指令及端口占用先提交，再下发。结束结果经 central 确认后释放 gateway 端口，迟到回执不能释放新订单的占用。
- 停机条件需要可靠、连续的端口证据；离线或缺失功率不等于零功率。保持断线重连、持久重试及补偿停止的既有语义。

## 数据库变更

- 当前项目尚未发布，三个 schema 仅保留各自的 `0001_init.sql`；结构调整直接修改初始化定义，不累加发布前迁移，也不使用 GORM 自动建表。
- 每张表、字段均填写有效 MySQL `COMMENT`，注明金额和计量单位、枚举值、快照及幂等用途。维护月分区、联合键和唯一索引语义。
- 现有数据默认保留。结构不兼容时先备份并设计数据迁移；仅对明确可丢弃的开发数据使用 `scripts/db/reset-dev.ps1 -ResetDevelopmentData`。不得仅删除 Goose 版本表或无条件清空库、卷和 Redis。
- MySQL DDL 不具备事务回滚；初始化中断应针对空 schema 处理，不将失败的结构更新标为成功。

## 开发环境

- 开发环境由 `scripts/dev/start.ps1` 启动的 `compose.dev.yaml` 容器统一提供，包含后端、后台、用户端、MySQL 和 Redis。启动、数据准备、模拟器与联调顺序见 [本地运行](README.md#本地运行) 与 [模拟器](README.md#模拟器)，本节只记录使用规则。
- 可自主拉起、重启与停止开发容器，包括未运行时的 `start.ps1` 与 `docker compose -f compose.dev.yaml`。不为启动而启动：只做当次任务需要的动作，用完不留下多余容器，`start.ps1` 保留已有数据。**破坏性动作仍需确认**——重建数据卷、`down -v`、`reset-dev.ps1`、清空开发库前一律先问。
- 验证与联调优先用既有路径：`scripts/dev/start.ps1`（开发栈）、`/health/ready` 判定就绪。**不要为"多验证一点"另造旁路脚本**——自建的临时容器、临时二进制、临时灌库脚本既要自己收拾，又复现不了仓库既有的正确做法。需要新验证手段时先用仓库里已有的，或提出方案经确认后再写。
- 不得自行在宿主机启动常驻本地服务器或 dev server，也不得为 `dist` 另建静态服务；这两类一律用容器内的地址。确需一次性临时服务时避开容器占用端口，用完立即停止并说明用途。
- 验证前端渲染访问容器内的后台与用户端 H5 地址，并同时确认页面控制台无报错。
- 本地地址固定：后台 `5173/admin/`、用户 H5 `5174/`、MySQL `3306`、Redis Cache `6379` 与 Stream `6380`、DC589 `9100`；user、admin、central、gateway、worker 就绪状态查各自 HTTP 端口的 `/health/ready`。
- Go 服务没有热重载。修改后按 README 重建受影响的服务，优先只重建该服务而非整个栈；确需重启整栈时先说明理由。
- 设备联调的两个模拟器须在独立终端分别执行 `scripts/dev/simulator.ps1 -Number 1` 与 `-Number 2`，属前台交互进程，不得作为后台服务重复拉起。模拟器与模拟支付结果不得当作实机或真实资金验收。

## 验证与交付

- Git 操作一律需要用户明确授权。未获授权不得执行 `commit`、`reset`、`revert`、`push`、`rebase`、`merge`、`cherry-pick`、`tag`，不得新建或切换分支，不得改写提交历史。改动只落在工作区，由用户审阅后自行提交。`git status`、`git diff`、`git log`、`git show` 等只读命令可以直接使用。
- 交付时报告工作区的未提交改动，不代替用户提交。
- Go 业务变更运行 `make check`；涉及模块依赖时运行 `go mod tidy -diff`，涉及进程装配或构建时用 `make build SERVICE=<name>` 或 `make build-all` 构建对应 `cmd`，可执行文件一律落在已忽略的 `bin/`，不在包目录下留产物。
- 本仓库只保留单元测试，**不设需要真实数据库的集成测试**。`make check` 为秒级本地检查，可在每次改动后运行。
- 由此产生的覆盖缺口须在交付说明中写明：事务原子性、行锁并发、唯一键冲突与幂等重放没有自动化覆盖。改动资金、并发或唯一性路径时，除 `make check` 外须在开发栈上实际走一遍对应流程，并可用 `go test -race ./...` 补充竞态检测。
- 后台逻辑修改运行 `npm test` 与 `npm run build`；用户端逻辑修改运行 `npm run typecheck`、`npm test`，涉及平台适配或构建配置时构建受影响的 H5／微信目标。命令在对应前端目录执行。
- 纯文档、注释和文案修改检查链接、引用、事实与格式；不为低影响可逆修改增加重复测试。已有验证通过后，仅在新增修改、失败或未决问题需要时扩大检查。
- 测试覆盖实际契约和故障边界，重点验证并发重放、非法状态、授权范围、金额与计量不一致、外部未知结果和回执丢失；避免照抄实现的测试。
- 功能、命令、目录或部署契约改变时同步维护 README 与本文件；设备协议完整规范维护于 `protocols/dc589.md`，精确业务字段留在源码、SQL 注释和 OpenAPI，不新增重复文档树。
- 完成交付前检查差异与失效引用，报告实际修改、验证结果及仍存在的实现边界；不得把模拟器或模拟支付验证称为实机或真实资金验收。
