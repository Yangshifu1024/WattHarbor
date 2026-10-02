# 仓库维护规则

## 依据与边界

- 以当前源码、路由注册、数据库初始化 SQL、Compose 配置和测试为实现依据。README 记录运行方式与业务契约；本文件记录维护规则。
- `cmd/` 只负责配置、依赖装配与进程生命周期，每个子目录是一个部署单元。业务实现按分层位于 `internal/domain`、`internal/application`、`internal/infrastructure`、`internal/interface`；进程特有编排位于 `internal/worker`、`internal/gateway`、`internal/delivery`；设备契约位于 `internal/protocol`；无业务语义的通用能力位于 `internal/platform`。
- 部署单元分两类。**对外**：`user`（C 端，`:8080`）、`admin`（运营后台，`:8081`）。**对内**：`central`（用例编排与内部端点，`:8082`）、`gateway`（设备接入，`:8083` 与 DC589/TCP `:9100`）、`worker`（事件消费、定时任务、外部投递，`:8085`）。对内端口不对外映射。**`central` 的 8082 在两套编排里都不映射端口**，宿主机打不开，内部调用只在容器网络内可达。开发栈另把 `gateway`:8083、`worker`:8085 绑到 `127.0.0.1` 便于本地直连排查；生产编排对这两个进程只写 `expose`，对外入口仅 Caddy。

五个进程共享同一套 `domain`/`application`/`infrastructure`，差别只在各自注册哪些 `interface` 路由——进程拆开不等于代码复制。
- 后台进程不取微信与支付凭据：它不替用户登录，也不发起支付。`config.Admin` 与 `config.Central` 分开定义，让"这个进程需要什么凭据"在类型上就是清楚的——多给一份 `WECHAT_SECRET` 就等于多一处泄露面。
- C 端充电中的遥测、历史曲线、充电采样与设备摘要，经 `GATEWAY_INTERNAL_URL` 回调 **gateway**（`:8083`）的 `/api/v1/internal/devices/*`，携服务令牌。这些端点由 `internal/gateway/control` 提供，不在 central 进程里——判断回调对象要看代码用的 base URL 字段，不能只看路径里有 `/internal`。
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
├── cmd/                                        部署单元，每个子目录一个进程
│   ├── user/main.go                             C 端接口进程      :8080
│   ├── admin/main.go                            后台接口进程      :8081
│   ├── central/main.go                          对内服务间接口     :8082
│   ├── gateway/main.go                          设备接入进程      :8083 + DC589/TCP :9100
│   ├── worker/main.go                           消费调度进程      :8085
│   └── migrate/main.go  devseed/  simulator-dc589/
│
├── internal/
│   │
│   │ ═══ 业务核心 ═══════════════════════════════════════════
│   │
│   ├── domain/                                 零 gorm、零 gin、零 Redis
│   │   ├── identity/
│   │   │   ├── user/                            用户聚合
│   │   │   │   ├── user.go                      User / Profile / 账号状态哨兵
│   │   │   │   └── auth.go                      访问令牌校验
│   │   │   └── auth/                            登录身份来源
│   │   │       ├── wechat.go                    微信小程序 code 换取
│   │   │       └── development.go               开发模式身份
│   │   │
│   │   ├── charging/                            充电与计费
│   │   │   ├── offer.go  offer_actual.go         价格档与按实际计量计价
│   │   │   ├── scheme.go  scheme_cost.go        计费方案与成本
│   │   │   ├── spec.go  rule.go                 规则口径与规则
│   │   │   ├── mode.go  cost.go                 计费方式与费用计算
│   │   │   ├── segments.go  measured_segments.go 计量分段
│   │   │   ├── end_meter.go  estimate.go        结束计量与预估
│   │   │   ├── budget_stop.go                   预算停机
│   │   │   ├── order_package.go                 订单冻结计费口径与展示名
│   │   │   ├── metering.go                      计费方式与协议能力的匹配
│   │   │   ├── live_meter_view.go               实时表读数投影
│   │   │   ├── capabilities.go  display.go       协议能力与展示开关
│   │   │   ├── device.go  actual.go  breakdown.go
│   │   │
│   │   ├── money/                               资金
│   │   │   ├── payment_errors.go                支付渠道与回调核验
│   │   │   ├── review.go                        退款人工复核
│   │   │   ├── allocation.go                    分账分配
│   │   │   ├── coupon.go  coupon_activity.go    券发放与活动规则
│   │   │   ├── card_number.go  card_port.go  scan.go  卡号与刷卡
│   │   │   ├── reconcile.go                     对账差异比对
│   │   │
│   │   ├── devices/                             设备
│   │   │   ├── fault.go                         故障语义与报修单状态流转
│   │   │   ├── telemetry.go                     遥测（功率、电量、时长）
│   │   │   └── identity.go                      设备编号合法性
│   │   │
│   │   └── operations/                          运营后台
│   │       ├── account.go                       后台账号身份、权限、退役权限
│   │       ├── scope.go                         数据范围与字段遮蔽
│   │       ├── export.go                        导出时间窗与公式注入防护
│   │       └── validate.go                      自由文本、公开链接与请求号校验
│   │
│   ├── application/                             跨聚合事务编排
│   │   ├── auto_stop.go                        停机扫描
│   │   └── *_test.go
│   │
│   │ ═══ 外部依赖实现 ═══════════════════════════════════════
│   │
│   ├── infrastructure/
│   │   ├── database/                           gorm 只在这里，表名只在这里
│   │   └── redis/
│   │       ├── session_store.go                 user:session:
│   │       ├── admin_session.go                 admin:session: / admin:mfa:
│   │       └── session_token.go                 刷新令牌布局与共用原语
│   │
│   │ ═══ 接口层 ═══════════════════════════════════════════
│   │
│   ├── interface/http/                          gin 只在这一层
│   │   ├── user_api.go  auth.go                C 端登录与资料
│   │   ├── charge_user_account.go              C 端账户路由装配
│   │   ├── user_wallet.go                      余额、流水、充值与余额退款
│   │   ├── user_station.go                     附近站点、站点详情与站内公告设备
│   │   ├── user_invoice.go                     开票申请与我的发票
│   │   ├── user_fault.go                       设备报障的提交与查询
│   │   ├── user_coupon.go                      我的优惠券
│   │   ├── user_announcement.go                公告列表
│   │   ├── user_account_helpers.go             分页与坐标解析助手
│   │   ├── charge_user_query.go                充电历史、进行中、曲线
│   │   ├── charge_user_stop.go                 用户停机
│   │   ├── charge_live_meter.go                实时表读数
│   │   ├── charge_feedback.go                  充电评价
│   │   ├── payment_scan.go  payment_*.go       扫码、起充、支付回调
│   │   ├── order_*.go                          启动授权、结果回传、结束计量
│   │   ├── card_*.go  phone_api.go  coupon_api.go  settlement_bill.go
│   │   ├── billing_service.go  refund_internal_api.go   对内资金端点
│   │   ├── admin_*.go                          后台：鉴权、账号角色、管道、MFA
│   │   ├── admin.openapi.json  user.openapi.json  两份接口契约
│   │   └── middleware.go
│   │
│   │ ═══ 协议契约（独立层）══════════════════════════════════
│   │
│   ├── protocol/                               契约自足，不依赖任何实现层
│   │   ├── adapter.go  command.go  endpoint.go  router.go  session_audit.go
│   │   └── dc589/
│   │       ├── adapter.go  device.go  config.go
│   │       ├── frame.go  log_data.go
│   │       ├── commands.go  power_control.go  cards.go
│   │       ├── measurements.go  fault.go
│   │       └── session_audit.go
│   │
│   │ ═══ 进程特有编排 ═══════════════════════════════════════
│   │
│   ├── gateway/                                设备接入运行时
│   │   ├── listener/server.go                   监听 endpoint、连接分派
│   │   ├── session/registry.go                  会话注册表
│   │   ├── control/                            设备侧 internal 端点与充电过程
│   │   │   ├── http.go                          路由注册
│   │   │   ├── authorize.go  start.go           启动授权与启动
│   │   │   ├── charge_process.go                充电进程
│   │   │   ├── telemetry.go  charging_samples.go
│   │   │   ├── historical_curve.go  device_summary.go
│   │   │   ├── device_faults.go  scan.go  user_stop.go
│   │   │   ├── worker_sync.go                   启动/结束回执同步端点
│   │   │   ├── card_delivery.go                 刷卡事件投递与回复冻结
│   │   │   ├── worker_sync_models.go            同步端点的记录类型
│   │   │   ├── backfill.go  compensate.go       历史回填与迟到补偿
│   │   │   └── session.go
│   │   ├── store/                              gateway_db 持久化
│   │   │   ├── mysql.go  gorm_models.go
│   │   │   ├── session.go  charge_start.go
│   │   │   ├── charge_process.go  user_stop.go
│   │   │   ├── scan.go  aggregate.go
│   │   │   └── compensation.go
│   │   ├── provision/                          设备开通与厂商
│   │   │   ├── http.go  vendors.go
│   │   └── simulator/dc589/                    联调模拟器，不参与生产
│   │       └── simulator.go  behavior.go  tui.go
│   │
│   ├── worker/                                 事件消费、定时、投递编排
│   │   ├── charge/                             命令与结果同步、刷卡决策
│   │   │   ├── sync.go  sync_client.go  end_sync.go
│   │   │   ├── cards.go  paid_start.go
│   │   │   ├── meter_segments.go  auto_stop.go
│   │   ├── outbox/                             publisher.go  dlq.go
│   │   ├── store/                              worker_db 持久化
│   │   │   └── schedule_store.go
│   │   ├── schedule/                           scheduler.go  http.go
│   │   ├── internaljob/                        ops.go  http.go
│   │   ├── alerts/sync.go                      设备告警同步
│   │   ├── billing/dispatch.go                 账单下发
│   │   └── refund/dispatch.go                  退款派发
│   │
│   ├── delivery/                               外部投递实现
│   │   ├── events.go  senders.go  webhook.go  regulatory.go
│   │
│   │ ═══ 无业务语义的通用能力 ═══════════════════════════════
│   │
│   └── platform/
│       ├── auth/                              jwt.go  totp.go
│       ├── config/config.go                   各进程配置
│       ├── dbconn/mysql.go                    连接池与 DSN
│       ├── dbutil/dbutil.go                    月分区、重复键、LIKE 转义
│       ├── httpapi/                           gin.go  codes.go  decimal_id.go  metrics.go
│       ├── netguard/guard.go                  出站 SSRF 防护
│       ├── phone/phone.go                     手机号规范化与脱敏
│       ├── serviceclient/                     内部 HTTP 客户端
│       └── snowflake/snowflake.go             ID 分配
│
├── migrations/{central_db,gateway_db,worker_db}/
├── scripts/{check,db,dev}/
├── admin-web/  miniprogram/
├── AGENTS.md  README.md  REFACTOR-PLAN.md  Makefile  go.mod
```

- 代码按分层组织，依赖单向、无例外：

  ```
  domain          →  无
  protocol        →  无（契约自足）
  application     →  domain + infrastructure + protocol
  infrastructure  →  domain
  interface       →  domain + application + infrastructure
  worker/gateway  →  protocol + domain + application + infrastructure
  cmd/*           →  interface + application + infrastructure + platform
  ```

  真正被约束的是**方向**：`interface` 与 `application` 都允许直接持有仓储
  （`infrastructure`），因为它们本身就是编排层，再往里包一层接口只会平移代码。
  但持有的是句柄，不是 SQL：表名与 gorm 查询构造只允许出现在持久化层，
  由 `make layers` 机检。另外 `worker` 不得直接访问 `central` 拥有的业务表——
  两者是独立进程，worker 取业务数据要经 central 的内部端点，而不是拿自己的
  连接直连；`worker` 自己的 `scheduled_task` / `event_outbox` / `dlq_*` 不在其列。被禁止的是反向——`domain`、`application`、
  `infrastructure` 一律不得 import `interface`。
  `domain` 与 `protocol` 是平行层，互不 import。除 `worker/` 与 `gateway/` 外任何包不得 import `protocol`。这三条由 `make layers` 机检，违反即失败。

- `internal/domain/` 是业务核心，零 gorm、零 gin、零 Redis。子域目录用业务概念命名，不是表名或集群名：

  ```
  domain/
    identity/    user/（user.go auth.go）  auth/（wechat.go development.go）
    charging/    order.go  session.go  billing.go  stop.go  scheme.go
    money/       wallet.go  refund.go  coupon.go  channel.go
                 card_number.go  card_port.go  scan.go  reconcile.go
    devices/     station.go  device.go  vendor.go  fault.go  telemetry.go
    operations/  account.go  scope.go  role.go  audit.go  announcement.go
                 export.go  webhook.go  alert.go  whitelabel.go
  ```

  文件名即业务对象。`order.go` 就是订单，不按技术层命名。仓储接口定义在 domain，实现见 `infrastructure/database/`；状态机、金额与计量规则、纯计算留在 domain，不 import 任何框架，可独立测试。

- `internal/application/` 扁平单包，一个用例一个文件，跨聚合事务在此。分子目录容易生环，用例之间高度互联，扁平优于分组。

- `internal/infrastructure/` 按外部系统分子包：`database/`（gorm 只在此处，数据库表名只在此处）、`redis/`、`wechat/`、`gateway_client/`。

- `internal/interface/http/` 按服务分文件：`user_api.go`（`/api/v1/user/*`，含 `/api/v1/user/auth/*` 登录）、`admin_auth.go` 与 `admin_*.go`（`/api/v1/admin/*`）、`internal_api.go` 等（`/api/v1/internal/*`）、`device_api.go`、`worker_api.go`。`gin.Context` 只在此处出现；`middleware.go` 管鉴权与数据范围，`error_mapper.go` 把领域错误映射为 HTTP 状态。

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
- 修改 API 时同步检查调用方、DTO、输入约束、错误状态和对应的 OpenAPI 文档：后台是 `internal/interface/http/admin.openapi.json`，C 端是 `user.openapi.json`（由 user 进程承载）。两份文档都要求"声明的端点必须真的注册"且"没有悬空的 schema 引用"，`make layers` 机检这两个方向。只锁"文档多写"——文档少写是补全工作，另行跟踪。文档准确性只能靠打真实接口核对：照代码"合理推断"写出来的请求体与响应形状会错，包括把整数写成字符串、给不分页的列表加分页字段。
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
