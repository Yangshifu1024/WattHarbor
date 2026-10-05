# 模块重构实施路线图

状态：实施中。源码及验收日期：2026-10-05。批次 P／0 已实施；其余批次按第 9 节记录。未完成任务中的目标路径和类型仍表示设计。

本文件负责任务、顺序和验收；[架构边界与组织约束](architecture-boundaries.md) 负责长期规则；[charging 检查结果](charging-review.md) 记录两个 P1 的修复及验证。完成任务后原位更新状态与证据，不追加多套“当前状态”或重复的历史结论。

## 1. 目标、范围与执行约定

### 1.1 最初四步与本路线图

| 调整方向 | 已有基础 | 后续任务 |
|---|---|---|
| 建立 charging 生命周期模块 | start／stop／finish／measurement、回执事务和客户端已归位 | 批次 0 修复恢复与取证问题；批次 3 增加独立查询入口 |
| HTTP 事务迁回业务模块，统一退款，收窄依赖 | charging 的回执和余额退款已收敛，其余入口尚未完成 | 批次 1–4 逐用例下沉，按模块同步 repo 命名迁移 |
| 核对表归属、跨模块 SQL 与接口边界 | 默认测试、真库测试和代码评审已有入口 | 每批执行第 8 节验收；不恢复 scripts/check，不以 import 合法代替数据归属检查 |
| 重新评估 central，考虑 user + central | 尚未作出合并决定 | 批次 6 交付比较结果；本轮代码重构保持五个进程 |

### 1.2 范围边界

- 保留 12 个业务模块、domain 的纯规则、protocol、持久化回执、Outbox 与当前三个 schema。业务模块共享代码，cmd 决定各进程装配和路由。
- 本轮不调整公开 URL、金额与计量单位、大整数身份、JSON 形状、数据库表名、分区、枚举或支付渠道协议。确需变化时单独列行为任务，核对全部调用方后实施。
- 每次修改一个可核验的业务入口；名称迁移、职责搬迁、正确性修复分别记录。不得在一次批量替换中同时改 SQL、路由和部署。
- 接管当前工作区前先读 git status／diff，保留已有未提交修改；按用户后续授权在每批验收通过后暂存并提交，不推送；不得切分支、重置或改写历史。
- 以开始任务时的源码、注册路由、初始化 SQL 和测试为准。发现本计划引用失效时先刷新该任务描述，不能按旧名称创建第二份实现。
- 已删除的旧重构文档不恢复。现存 AGENTS、README 与协议文档中的失效引用纳入 P-01 修正，不再次清空维护文件。
- 详细中文注释是所有重构任务的必交付项，规则见 [中文注释规范](architecture-boundaries.md#中文注释规范)。覆盖字段级释义、函数／方法的全部入参与出参，以及核心逻辑的业务依据、约束和恢复；语言严肃简练，不使用口语化表述。新增、修改和迁移代码均适用，随模块完成，不留到最后统一补写。

### 1.3 一个任务的标准操作顺序

1. 读当前入口、消费者、事务体及测试，列出输入、结果、状态、表、锁、幂等键和外部调用先后关系。
2. 写出目标业务方法与最小依赖；先核对反向引用及同库事务，确定错误如何由 HTTP 映射。
3. 补足会被迁移影响的故障／并发证据；优先迁移已有测试，缺口才增加测试。
4. 迁移用例、所属 repo 和模型，更新 cmd 装配及消费者；同一步删除原业务实现，不留转发层或旧类型别名。逐字段补齐中文注释，逐函数／方法核对入参、出参与错误，在核心决策处说明事务、并发和恢复依据。
5. 运行适用的默认、真库、构建及接口检查，核对状态和资金没有提前确认。
6. 原位更新任务状态、迁移路径、接口与验证记录；检查失效 import、源码链接、go:embed、OpenAPI 和目录说明。

## 2. 目标 internal 结构与命名

以下为固定的职责骨架，省略具体 repo、测试和已存在的工具文件。各业务模块先保持根包组织；新增子包必须能说明独立职责和依赖。

```text
internal/
├── charging/
│   ├── start/                     启动授权、待启动清单、启动回执
│   ├── stop/                      停止授权、截止冻结、停机恢复
│   ├── finish/                    结束回执、释放业务端口、提交计费任务
│   ├── measurement/               无 IO 的计量证据转换
│   └── query/                     运行状态、遥测及过程组合查询
├── payment/                       checkout、callback、渠道、支付 repo
├── settlement/                    billing、refund、invoice、计量复核及 repo
├── wallet/                        recharge、refund、risk 与资金 repo
├── card/                          swipe、status、后台卡管理与 repo
├── order/                         query、feedback、订单状态及快照 repo
├── pricing/                       scheme、publication、目标范围与 repo
├── marketing/                     coupon、activity、权益发放及 repo
├── device/                        import、station、准入、fault、alert 与 repo
├── identity/                      auth、session、phone、用户管理及用户会话 Redis repo
├── ops/                           account、role、mfa、audit、announcement、dashboard 及后台会话 repo
├── export/                        任务、格式生成、下载准入、文件清理与 repo
├── domain/                        charging／money／devices／operations 纯规则
├── http/                          HTTP 入站适配，直接按调用方组织
│   ├── user/                      用户接口按业务族组织
│   ├── admin/                     保留现有业务族，移出业务编排
│   ├── central/                   内部用例 HTTP 适配
│   └── auth/                      HTTP 鉴权适配，不拥有身份／权限业务规则
├── contracts/                     central／gateway；有实际调用时补 worker
├── integration/                   与 contracts 对应的具名客户端
├── gateway/                       control、repo、listener、session、provision、runtime、simulator
├── worker/                        charge、billing、refund、alerts、outbox、schedule、internaljob、runtime、repo
├── protocol/                      根契约与 dc589 实现
├── delivery/                      对外事件与签名投递
├── persist/                       分页、自增 ID 等原语
└── platform/                      配置、数据库连接、HTTP、认证工具等
```

`charging/query` 不成为生命周期根包的汇总服务；start／stop／finish 继续互不引用。目标结构不保留 `infrastructure`：现有 database 的 dashboard 归 ops，case 分归 order／device，Redis 用户会话归 identity、后台会话归 ops。数据库连接继续由 platform/dbconn 提供；先完成职责迁移，再删除空目录。

HTTP 目标路径采用 `internal/http`，移除只有一个子目录且没有独立职责的 `interface` 层。user／admin／central 表示接口面与注册归属，auth 提供共享 HTTP 鉴权适配；通用响应与服务器工具仍在 platform/httpapi，服务间出站客户端仍在 integration。以后出现独立的入站协议再按实际职责增加目录，不预建 interface／transport 包。下文源码链接仍指向迁移前的真实路径。

Redis 是 repo 的实现方式，不单独形成顶层业务模块。用户／后台记录、键前缀、刷新寿命与轮换策略归各自模块；无 IO 的随机源归 platform/auth，令牌布局、编码、摘要与共用注销脚本归 platform/sessiontoken。共用机制只接受调用方的前缀、key 与摘要；不得引用 identity／ops 的业务模型或固定业务键，不把整个会话包直接搬进 platform。

### 2.1 repo 命名迁移 N-模块

规则见 [数据访问命名](architecture-boundaries.md#数据访问命名)。每个模块建立独立的 N 子任务，随该模块批次完成：

| 对象 | 迁移规则 |
|---|---|
| 文件 | `*_store.go` → `*_repo.go`；匹配的测试文件同步，保留 integration tag 和测试职责 |
| 类型／字段 | 项目自定义持久化 `XxxStore` → `XxxRepo`，注入字段改为 Repo 或职责明确的 XxxRepo |
| 独立包 | gateway/store → gateway/repo；worker/store → worker/repo，更新 package、import 及别名 |
| 模型 | schema／model 文件保持名称与数据库映射，不按 repo 规则机械改名 |
| 消费者接口 | 按能力命名，如 Reader、Gateway、Authorizer；已有 UserRepository 等无需为了后缀统一再改 |
| 外部协议 | 第三方 Store、SQL 表列、JSON、Redis 键、事件 stream/source、服务路由和配置项保持契约 |

业务根包直接放 repo 文件，不创建 12 套 repo 子目录。对于 `user_repository_store.go` 等重复表达，应按内容定为 user_repo.go，而非 user_repository_repo.go。测试辅助文件如 gorm_integration_test.go 不强制改名。

名称迁移的退出条件：旧 import／类型／字段全部消除，消费者编译通过，相关默认测试与装配检查通过；源码引用更新为真实新路径，未修改行为或 SQL。不做无边界的全仓 Store 字符串替换。

## 3. 数据归属、事务与依赖

### 3.1 关键写入归属

本表规定重构涉及的核心表族；实施者须逐 repo 核对该入口实际写入的完整集合。只读联表与业务写入分别记录，不能靠表名前缀判定归属。

| 模块／子包 | 所属写入 | 跨模块协作 |
|---|---|---|
| charging/start | charge_start_receipt、启动阶段 active_port_charge、生命周期日志 | card 状态、order 状态、payment 凭据、wallet／settlement 退款均在原回执事务内执行 |
| charging/stop | charge_billing_cutoff | card 读取已购时长，order 锁定状态；gateway 独立持久受理 STOP |
| charging/finish | charge_end_receipt、结束阶段 active_port_charge 释放、生命周期日志 | card 释放、order 结束及 settlement 计费任务同事务 |
| order | charge_order、charge_order_pricing、feedback | 状态写入对外提供事务内能力；支付、结算和实时查询不能建立反向 import |
| payment | payment_order、charge_payment_intent、charge_prepay、payment_callback_idempotent | 回调事务调用钱包到账／营销权益／退款记录能力，不直接写它们的表 |
| settlement | charge_billing_job、charge_fee_receipt、fee_calculation／fee_delivery／fee_receipt、charge_bill／charge_bill_read、manual_fee_review、charge_manual_settlement、charge_meter_review、refund_*、manual_refund_request、invoice_* | payment 维护支付金额与状态；wallet 维护余额及预占；保留完整结算／退款事务 |
| wallet | wallet_account、wallet_txn、wallet_recharge_request、wallet_refund_request／part、wallet_risk_*、risk_freeze_log | 退款记录由 settlement 的注入能力创建；支付来源由 payment 能力提供 |
| card | online_card／online_card_audit、card_charge、card_operation | 调用 wallet、payment、order 的既有单向能力 |
| pricing | pricing_rule、pricing_template、pricing_publication | 读取 device 的设备／站点事实，规则计算使用 domain/charging |
| marketing | coupon、coupon_grant／request、coupon_redemption、coupon_activity_rule | 核销与发放提供 InTx 能力，不反向 import payment／settlement |
| device | device_meta、device_import／identity、station、device_fault_report／event、alert_event | 网关设备、厂商、命令、会话和遥测的写入留 gateway |
| identity | user、user_login_identity；会话通过 Redis 实现 | 新用户钱包初始化调用 wallet 能力，不反向引用 identity |
| ops | admin_user、role／permission、role_permission、admin_data_scope／field_mask、audit_log、announcement、event_outbox／admin_event_outbox | 审计和 Outbox 与所属业务事务提交；dashboard 是只读投影 |
| export | export_task、任务文件 | 数据源只读，范围校验和文件生命周期归 export |
| gateway | gateway_db 的设备、端口、命令、回执投递、遥测、会话、厂商及 Outbox | 不写中央资金或订单业务表 |
| worker | scheduled_task、task_execution_log 与投递状态 | 可访问既有 Outbox 基础设施来源，不直接读写中央资金、订单等业务表 |

中央 Outbox 的历史来源标签不因 repo 改名改变。gateway 的 vendor 档案继续由 gateway 持有，后台厂商接口使用具名客户端，不另造中央厂商写入模块。

### 3.2 事务核验基线

| 路径 | 当前顺序或边界 | 迁移要求 |
|---|---|---|
| 余额启动回执 | 钱包 → card 会话 → order；READ COMMITTED | 成功／拒绝回执、退款、幂等、日志、Outbox 保持原子 |
| 结束回执 | card 会话 → order；READ COMMITTED | 结束幂等、计量、端口释放、计费任务和日志不可拆提交 |
| 刷卡到期冻结 | card 会话 → order → 首次 cutoff | 用所属模块的锁定能力读取，保留原截止点 |
| 钱包充值申请 | READ COMMITTED；user → wallet，随后请求凭据／支付创建；预支付在提交后 | 身份读取后等待锁仍看见同号原凭据，不用已有 RR 快照重建支付 |
| 充值回调 | payment → recharge_request → wallet | 不重排成申请路径的锁顺序；支付摘要、到账、奖励与 Outbox 同事务 |
| 钱包退款申请 | wallet 后首次建立默认 RR 快照；原请求重放只读；候选支付 ID 普通读取后按 PRIMARY 锁定已支付来源 | 不扫描锁定待付充值，避免与到账 payment → wallet 形成循环；冻结、来源分摊及退款记录同事务 |
| 风险批准／解冻 | READ COMMITTED；request → wallet，再锁所属支付／退款或冻结 | 等待钱包后读取新到账来源及用户状态，原动作回执和审计同事务；生效冻结用当前读 |
| 计费入账 | 余额路径先 wallet，再 payment → order | 费用回执、余额动作、退款预留、优惠券及任务状态完整提交 |
| 渠道退款 | 必要的钱包锁 → payment → refund；网络调用在持久 claim 后 | 查询未知结果并沿用原退款单号，确认后再更新到账与预占 |
| 设备导入 | import 任务锁内执行有界 gateway 调用，然后更新元数据／任务 | 首批保持原顺序；优化持锁时间另作领取／租约／恢复设计 |

表中的顺序是已核对路径的基线，不代表全局死锁已经被证明不存在。每个任务还要列出辅助函数锁定的行；若发现循环锁顺序，新增单独的行为修复与确定性真库用例，不借目录迁移悄悄重排。

### 3.3 必须先定义的能力

接口由消费者定义，具体实现由 cmd 注入；下列名称是职责建议，实施时以输入与恢复需求定稿。

| 消费者 | 能力 | 实现方与约束 |
|---|---|---|
| payment 回调 | LateRefundRecorder／钱包到账与充值请求读取 | settlement／wallet；收同一个 tx 和业务值，不能暴露对方 ORM 模型 |
| wallet 申请 | 支付创建、原支付来源锁定、预支付参数复用 | payment；wallet 不直接 import payment，避免未来 payment → wallet 的反向循环 |
| wallet 退款预占 | RefundRecorder | settlement；同 tx 创建 refund_record，wallet 自己写 frozen 与 part；禁止 wallet → settlement 反向 import |
| card Swipe | 端口事实、有效计价方案 | device／pricing／gateway 客户端；不能依赖 HTTP ScanAPI 或 HTTP 状态码 |
| pricing 发布 | 设备能力与站点归属读取 | device；device 若需要反查方案，使用消费者接口，不形成 device ↔ pricing |
| order／charging/query | 支付／结算摘要、计费进度、cutoff、gateway 读证据 | 所属模块或客户端；order 基础包不 import settlement／charging |
| 各管理用例 | Actor／DataScope、事务内审计、实时复核权限 | 纯值与 ops 能力；审核必须复查首签人的当前权限 |
| device 导入／厂商管理 | Provisioner／VendorReader／VendorWriter | integration/gateway；不把网络配置带进用例 |
| export | 授权／范围检查、只读数据源、文件输出能力 | ops 与所属模块；不依赖 admin HTTP 类型 |

共享业务值放 domain；服务间 DTO 放 contracts；私有输入／输出放用例自身。反向依赖通过窄接口解决，不通过新增万能 common、全量 Repo 或 DI 容器解决。

### 3.4 跨表只读投影

order 基础查询可保留明确的付款展示投影；充电实时查询由 charging/query 组合。dashboard 与导出允许专用 repo 执行受范围约束的跨表只读查询，记录字段、来源和用途，避免逐行调用造成 N+1。它们不能借投影执行资金或状态写入，也不能暴露可继续拼 SQL 的 GORM builder。

## 4. 批次总览与任务依赖

所有新增任务初始均为待实施；表中的“已有基础”不代表对应整个批次完成。

| 批次 | 任务 | 前置条件 | 退出条件 |
|---|---|---|---|
| P | P-01 文档入口；P-02 契约／事务基线 | 当前工作区可识别 | 已确认维护规则、真实路由、源码路径、资金锁和测试入口 |
| 0 | C-01 停机恢复；C-02 连续证据；C-03 card 读取边界；N-charging | P | 两个 P1 关闭，截止首写和恢复有真库证据，证据缺失不会误停 |
| 1 | S-01 计费；F-01 事务内能力；PAY-01 支付准备；PAY-02 支付回调归属；W-01 钱包；S-02 审核；对应 N 任务 | 批次 0；F-01 先于模型迁移 | 资金入口不在 HTTP；退款、到账归属明确，无循环；账单故障可恢复 |
| 2 | D-01 准入与导入；PR-01 发布；CA-01 刷卡／卡管理；对应 N 任务 | F-01；CA-01 依赖设备与计价读取能力 | 元数据／发布／刷卡入口可独立调用，重放、并发与范围契约不变 |
| 3 | O-01 订单与反馈；CQ-01 充电查询；M-01 营销；I-01 身份；对应 N 任务 | 资金查询与设备读取能力稳定 | 订单基础包不反向依赖；读视图、发券预算、会话与手机号正确 |
| 4 | OP-01 账号权限／审计；D-02 故障告警；E-01 导出；对应 N 任务 | 业务入口及只读数据源具备；审计能力提前提供 | 实时授权、双人复核、工单与导出可恢复；HTTP 不持有这些事务 |
| 5 | H-01 HTTP 目录；INF-01 基础设施目录收尾；X-01 剩余客户端；G-01 gateway；WK-01 worker；残留 N 任务 | 前述入口已迁出 HTTP；I-01／OP-01／O-01／D-02 的基础设施职责已归位 | 路由／嵌入资源一致，interface／infrastructure 已移除，通用 sync_client 无业务调用，全部项目持久化命名归 repo |
| 6 | DEP-01 central 部署评估 | 批次 0–5 完成且流程回归有证据 | 交付保留／合并比较结论及依据；是否实施合并单独决定 |

审计值和 InTx 能力在需要它们的最早批次提取；OP-01 的完整账号迁移仍在批次 4。具名客户端与所属任务一起交付，批次 5 仅收尾。不同批次可提前完成稳定的只读能力，但不得跳过资金／恢复前置条件。

批次 1 的具体顺序：先完成 F-01 的接口与装配，再提供 W-01 的到账／充值请求读取和 S-02 的退款记录事务内能力，随后迁 PAY-02 回调、W-01 的申请／预占；最后迁移结算模型与剩余审核入口。S-01 的计费入口迁移可独立推进，补出账恢复单独验证。PAY-01 需要的 D-01 设备准入读取、PR-01 有效方案读取和 gateway 具名客户端应提前交付这些窄能力，完整设备管理与发布事务仍留批次 2；不能为了按批次搬文件暂时复制 SQL。

### P-01 文档与规则入口

- 核对现存 AGENTS／README 的链接，改指三份新文档，去掉对已删除重构文件的维护要求；更新未来数据访问规则为 repo，目录图仍反映真实源码，不能提前写入未创建文件。
- 记录当前工作区已有修改，源码检查日期以及适用的配置／路由／schema；不维护易过期的总代码行数或一次性工作区文件数量。
- 分开登记“实现存在”“默认测试通过”“真库通过”“设备／真实支付验收”，后者不能由前者推导。

### P-02 契约与事务基线

- 从 cmd/user、admin、central、gateway、worker 的注册函数核对每个迁移入口：HTTP 方法／路径、认证、输入约束、响应／错误、超时、幂等 ID、对端 base URL。
- 保存现有消费者测试和关键输入样例；业务 DTO 不直接复用 ORM 模型。公共 Snowflake 身份保持十进制字符串，内部整数身份按当前契约保留。
- 按第 3 节补齐实际锁图、事务隔离、唯一键／分区组合键、外部调用前后提交点和重试恢复入口。

## 5. 逐模块实施清单

每小节包含职责迁移和对应 N 子任务；列表可作为后续任务的直接输入。当前路径在源码改名时原位更新，不保留失效链接作为历史导航。

### 5.1 charging：C-01／C-02／C-03／N-charging

来源：[自动停机](../internal/charging/stop/auto_stop.go)、[截止存储](../internal/charging/stop/cutoff_repo.go)、[刷卡到期冻结](../internal/charging/stop/deadline_repo.go)、[零功率判断](../internal/charging/stop/meter.go)、[gateway 取证](../internal/gateway/control/charging_samples.go)。

目标：stop 拥有停机判定与恢复；cutoff_repo.go／deadline_repo.go 持有数据库操作；证据契约由 contracts/gateway 与 integration/gateway 共同维护。

- [x] C-01：在重新计价／取证前读取已有 cutoff；冻结记录是原决策的恢复依据，不能因为新证据缺失阻断已经确定的 STOP。首次写入遇到并发唯一键冲突时重读原记录，不重新冻结费用或时间。
- [x] C-01：仍在充电时沿用原订单身份请求 STOP；gateway 已持久受理后由既有重试恢复，central 不把受理视为停机完成。单笔冲突／调用失败记录并继续，系统性数据库或整批取证失败与单笔错误区分。
- [x] C-02：保留目标端口缺失事实，检查证据时间顺序、未来时间、新鲜度、相邻缺口及分页连续性；间隔阈值依据可证明的采样契约定稿，不把具体协议码或帧常量写入 charging。
- [x] C-03：将 deadline 中 card／order 读取改为所属模块的事务内能力，原 card 会话 → order 顺序不变；统一首写／重读行为。
- [x] N-charging：全部生命周期 *_store.go 改 *_repo.go，保留 StartResultService／EndResultService 等业务类型，start／stop／finish 不相互 import。

验收：冻结提交后请求失败、gateway 已受理但响应丢失、重启恢复、并发首写、原截止／费用不变、已结束订单不再派发、早期失败不阻挡后续订单；连续零功率成功，重连／缺失端口／陈旧／乱序／未来／分页缺口不误停；默认与真库用例分别覆盖判断和持久语义。回归现有启动拒绝退款一次、结束并发／顺序重放及内容冲突测试。

### 5.2 settlement：S-01／S-02／N-settlement

来源：[HTTP 计费服务](../internal/http/central/billing_service.go)、[计费入账](../internal/settlement/billing_job_repo.go)、[费用计算](../internal/settlement/fee_calculation_repo.go)、[账单](../internal/settlement/bill_repo.go)，以及 HTTP finance、device/meter_reviews、settlement_bill、user_invoice 的业务编排。

目标：billing.go 的 BillingService.Run；billing_job_repo.go、fee_calculation_repo.go、bill_repo.go；refund.go、invoice.go、meter_review.go 与各自 repo／schema。Calculate 等有业务行为的组件按职责命名，不因为原名 Store 就统一改成万能 Repo。

- [x] S-01：迁入 Run、deliverPending、人工计费复核入队和 defer；BillIssuer 等能力由 settlement 定义。HTTP 只绑定、鉴权、调用和映射结果。
- [x] S-01：明确 Calculate 持久结果、Apply 入账、MarkDelivered、账单 Issue 的不同完成事实；迁移先保留原算法与快照，再单列补出账恢复任务。
- [x] S-01：新增有界的“满足现有出账条件但没有账单”恢复查询，覆盖 MarkDelivered 后 Issue 失败及 backlog 投递路径；使用原结算回执与既有账单唯一键，不重算、重复扣款或创建影子账单来源。
- [x] S-02：迁入人工结算、退款申请／审核／执行、发票申请／审核、计量分段提案／复核；将操作人、范围、实时复核权限与审计放进用例事务。
- [x] S-02：ChargeFeeRecord／RefundRecord 模型迁到 settlement，必须先完成 F-01 和 PAY-02／W-01 的反向依赖消除；payment 不通过类型别名引用 settlement。
- [x] N-settlement：退款、账单、发票和计费 repo 统一名称；复用 RefundBalanceInTx，不新增另一套余额退还算法。

验收：相同来源费用重放一致，异内容冲突；并发退款占用不超实付；缺少计量／快照进入复核；补出账失败可下一轮恢复且不再入账；不同人员复核、首签权限变化、金额／链接／分段快照变化均按原约束处理；渠道未知结果仍留预占和原退款编号。

### 5.3 payment：F-01／PAY-01／PAY-02／N-payment

来源：[扫码支付入口](../internal/http/user/payment/payment.go)、[支付意图](../internal/payment/payment_intent_repo.go)、[支付回调](../internal/payment/payment_callback_repo.go)、[设备运营读取](../internal/device/operation_repo.go)、[支付模型](../internal/payment/money_schema.go)。

目标：CheckoutService.Offers／Start、CallbackService.Apply；payment_intent_repo.go、payment_callback_repo.go、payment_order_repo.go 等支付数据能力。保留渠道验签／解密边界。

- [x] F-01：先定义充值请求读取／到账、迟到退款与钱包退款记录的事务内输入输出；补上 payment、wallet、settlement 的 cmd 注入，证明无反向 import。
- [x] PAY-01：把 Replay → 端口准入 → 有效方案／套餐核对 → Reserve → 预支付参数复用／申请／保存迁入 checkout；报价发现的匿名访问与提交支付的登录要求不变。
- [x] PAY-01：设备状态查询归 device，扫码运行端口事实由具名 gateway 客户端提供；用例返回业务错误，不依赖 ScanAPI 或 HTTP 状态码。
- [x] PAY-02：回调继续拥有 payment／intent／幂等摘要事务；同 tx 调用 wallet 到账、marketing 权益、settlement 退款记录、order 建单／快照及 ops Outbox 能力。
- [x] PAY-02：拆出 settleWalletRecharge 中的 wallet 表访问和 queueLatePaymentRefund 的 refund_record 写入；渠道金额／身份核验与支付状态推进仍归 payment。
- [x] N-payment：迁移文件、类型和消费者；money_schema.go 只留下 payment 所属模型，删除迁出的退款／结算模型旧定义。

验收：同 request_id 同内容返回原支付单与参数，异内容拒绝；过期意图付款退款且不正常建单；通知商户／应用／付款人／关联／金额／交易号不符拒绝；重复通知一次到账和发券；预支付响应未知可用原单恢复，客户端成功不推进设备状态。

### 5.4 wallet：W-01／N-wallet

来源：[钱包 HTTP](../internal/http/user/wallet/wallet.go)、[账户能力](../internal/wallet/wallet_account_repo.go)、[退款预占](../internal/wallet/wallet_reservation_repo.go)、[后台风控入口](../internal/http/admin/finance/wallet_risks.go)。

目标：RechargeService.Create／CreditInTx、RefundService.Request、RiskService.Review／Release；wallet_account_repo.go、wallet_ledger_repo.go、wallet_refund_repo.go、wallet_reservation_repo.go 和 risk repo。

- [x] W-01：迁入充值申请的用户／钱包检查、请求重放、支付创建和提交后预支付复用；payment 能力通过接口注入。
- [x] W-01：迁入退款申请的归属、余额减冻结额度、近期频次、冻结联动、请求内容核对与响应凭据；不能把风险批准视为渠道已到账。
- [x] W-01：ReserveRefund 保留钱包冻结、原充值来源分摊与 wallet_refund_part；其中 refund_record 创建调用注入的 settlement 能力，同事务提交。
- [x] W-01：定义充值到账的事务内能力，冻结钱包仍可接收已确认资金；账户版本与流水必须一致，首充奖励仍在支付回调原事务内执行。
- [x] W-01：后台风险审核／解除用例保留范围、复核、预占释放与审计，不能由 handler 任意改状态。
- [x] N-wallet：repo 与持久化模型的角色分开；钱包不 import payment／settlement，实现通过 cmd 注入。

验收：并发预占不超可用余额；申请／审批／渠道重试分别幂等；重复充值只一条资金动作；退款未知结果不释放 frozen；驳回／失败／成功按既有规则只释放一次；同请求不同金额／原因／用户冲突，响应丢失后重放返回原结果。

### 5.5 device：D-01／D-02／N-device

来源：[导入编排](../internal/http/admin/device/device_imports.go)、[导入任务](../internal/device/import_repo.go)、[用户故障](../internal/http/user/device/fault.go)、[通用 case](../internal/device/fault_repo.go)、[告警存储](../internal/device/alert_repo.go)（原 alert_sync_store）。

目标：ImportService.Create／Retry、OperationReader、StationService、FaultService.Report／Dispatch／Resolve／History、AlertService.Sync／Acknowledge；import_repo.go、operation_repo.go、fault_repo.go 等。

- [x] D-01：先提供设备操作状态、站点／端口归属与能力事实，支撑 payment、card、pricing；接管 payment_device_store 中业务元数据读取。
- [x] D-01：迁入导入创建、任务锁、范围核对、gateway 预置、metadata 更新、失败／完成及审计；保留有界请求和失败状态可提交的原行为。
- [x] D-01：站点／设备新增、更新、启停、导入身份冲突检查迁回 device；厂商 CRUD 仍由 gateway 提供具名能力。
- [x] D-02：从 casework 与通用 CaseStore 拆出设备工单；参数不再收任意表名。状态机沿用 device/fault.go，历史事件和用户可见字段与状态变更同事务。
- [x] D-02：告警同步与确认归 device，用例调用 typed gateway 读取；后台 ops 目录中 ackAlert 只是 HTTP 路由位置，不拥有 alert_event。
- [x] N-device：统一 repo 命名并消除接口／payment 包里的设备表写入。

验收：同导入内容重放、不同归属冲突、网关成功后中央失败可原任务恢复、网关失败留下可重试状态；不能默认为异步导入。用户／后台工单列表、详情、历史和状态变更范围一致；缺失设备证据不伪造故障恢复；告警重复同步无重复活动事件。

缩短导入持锁时间属于后续性能方案，需要持久领取、租约、结果恢复及取消设计，不与 D-01 文件搬迁合并。

### 5.6 pricing：PR-01／N-pricing

来源：[方案管理 HTTP](../internal/http/admin/pricing/charging_schemes.go)、pricing_admin_repo.go、pricing_scope_repo.go、scheme_target_repo.go、metering_repo.go。

目标：SchemeService.Save／ChangeStatus、QueryService.List／Effective／Capabilities、PublicationService.Publish／Assign、有效方案只读能力及所属 repo；计算继续留 domain/charging。

- [x] PR-01：迁入方案格式／范围、目标重叠、设备计量兼容、乐观版本、发布与审计；后台 scope 转为明确用例输入。
- [x] PR-01：保留 pricing → device 单向读取，device 不反向 import pricing；payment／card 使用所需的方案读取能力。
- [x] PR-01：订单／支付意图／刷卡首次扣款继续冻结完整方案和套餐，发布变更不覆盖历史快照。
- [x] N-pricing：根包 repo 命名，不新增一套规则计算或每设备协议的计费包。

验收：不兼容设备、目标重叠、版本变化和无权范围被拒绝；发布事务失败不留下半发布；不同收费模式、单位与舍入继续回归已有 domain 测试；历史价格不受当前方案修改影响。

### 5.7 card：CA-01／N-card

来源：[刷卡 HTTP](../internal/http/user/card/api.go)、[主事务](../internal/card/card_repo.go)、[后台在线卡](../internal/http/admin/user/online_cards.go)、worker/charge/cards.go。

目标：SwipeService.Apply、CardStatusService.Change、CardAdminService.Bind／ChangeStatus；card_repo.go、charge_repo.go、start_result_repo.go 与窄读取能力。

- [x] CA-01：将事件时效、端口事实、活跃会话、方案与余额准入放入 SwipeService；现有 Swipe 事务原样收拢后再考虑拆函数。
- [x] CA-01：迁入用户卡状态和后台绑定／启停事务，绑定人与操作范围校验归用例。
- [x] CA-01：为 charging 提供会话 Lock／ConfirmStart／FailStart／ClearActivePort 的稳定 InTx 能力，避免跨模块自行映射 card 表。
- [x] CA-01：卡事件读取、决定冻结、reply、advance 迁入共享契约和具名客户端；冻结后的回复和原 event_key 重试不变。
- [x] N-card：更新 CardStore 等类型与调用方，不把纯卡号／端口校验搬出 domain。

验收：过期事件拒绝、同事件重放、卡归属及冻结限制、端口占用冲突；续费与截止冻结竞争保持已购时长一致；扣款／累计时长／操作凭据一起提交；回复丢失后重发原决定，金额与不足余额语义不变。

### 5.8 order 与 charging/query：O-01／CQ-01／N-order

来源：[用户订单查询](../internal/http/user/order/query.go)、[反馈提交](../internal/http/user/order/feedback.go)、[后台 casework](../internal/http/admin/order/casework.go)、order 的 history／view／package repo，以及后台 order_live／order_process。

目标：order.QueryService.History／Detail、FeedbackService.Submit／Reply；charging/query.Service.Ongoing／Snapshot／Curve／Process。身份、范围及窗口作为业务输入。

- [x] O-01：订单基础 repo 提供状态锁定、编号、定价快照和历史投影；付款／退款金额写入仍调用 payment 能力。
- [x] O-01：反馈提交、回复状态、订单归属与附件规则迁回 order，拆除通用 case 的表名选择能力；审计同事务。
- [x] CQ-01：组合计费进度、cutoff、支付／费用摘要及 gateway 遥测／历史曲线／过程；定义窄 Reader，order 不 import settlement 或 charging。
- [x] CQ-01：gateway DTO 转为本用例读模型，再由 HTTP 映射公开字段；不把当前遥测当最终结算，不将缺失值替换为零。
- [x] N-order：history／state／view／package／snapshot repo 统一名称，纯展示规则与 SQL 投影分清。

验收：用户隔离、后台范围、稳定分页、非法／超限曲线窗口；已结算与待计费明确区分；网关不可用时按现有接口表达缺失／失败；套餐名称从冻结快照读取；反馈状态和重放规则不改变；组合查询无逐行外部调用。

### 5.9 marketing：M-01／N-marketing

来源：HTTP admin/marketing/coupons.go、coupon_activity.go、user_coupon.go、coupon_api.go；[核销存储](../internal/marketing/coupon_redeem_repo.go)、[活动规则](../internal/marketing/coupon_activity_repo.go)。

目标：CouponService.Create／Grant／Redeem、ActivityService.Create／Update／ApplyInTx，保留已有纯规则与同事务发放／核销能力。

- [x] M-01：迁入管理员活动、券、发放目标／预算／范围、用户领取及核销的业务准入；HTTP 不组合预算判断和写入。
- [x] M-01：沿用支付与计费事务中的权益能力，活动不适用与存储失败分别表达；marketing 不 import payment／settlement。
- [x] M-01：冻结规则、金额和 event_key，保留确定编号、请求凭据与并发预算当前读。
- [x] N-marketing：规则文件保留业务名称，持久化文件和类型改 repo。

验收：预算边界的两独立事务确定性测试；重复事件／领取／核销只生效一次；过期／冻结／不适用拒绝；优惠与实付一致，事务回滚不留下发券或核销结果。

### 5.10 identity：I-01／N-identity

来源：[认证适配](../internal/http/auth/auth.go)、[登录接口](../internal/http/user/identity/session.go)、phone_api.go、admin/user/charge_users.go、identity 的用户与微信能力、infrastructure/redis 的会话实现。

目标：SessionService.Login／Refresh／Logout／Authenticate、PhoneService.Bind、UserQueryService；用户 repo 与 session_repo.go 的 Redis 实现。HTTP auth 子包只提取凭据、调用身份能力及写入请求上下文。

- [x] I-01：登录交换 → 用户准入／创建 → 会话创建 → 签发，以及签发失败撤销会话的顺序迁入用例。
- [x] I-01：刷新、注销、冻结用户和会话归属核对进入服务；Redis／JWT 实现通过所需能力注入。
- [x] I-01：将 infrastructure/redis/session_store.go 及用户会话测试迁到 identity/session_repo.go；保持用户 ID 十进制字符串、旧数值记录兼容、Lua 原子轮换、previous 摘要与 KEEPTTL。通用令牌机制与 OP-01 共用，RT_ 和 user:session: 前缀留在 identity。
- [x] I-01：手机号正式绑定继续使用微信验证结果；开发模式单独注入，不能与正式绑定共享绕过分支。
- [x] I-01：后台用户列表／详情／状态业务归 identity，充值历史通过钱包读取能力提供；后台账号、角色和 MFA 留 ops。
- [x] N-identity：用户与用户会话的持久化类型／文件命名归 repo，保留既有稳定的 Repository 接口名和会话键格式；cmd 注入新实现，删除旧用户会话实现。

验收：会话过期／撤销／刷新竞争、冻结用户、跨用户凭据、签发失败清理；号码唯一性与归属、重复绑定、微信缺失结果；公开身份仍为十进制字符串，开发能力不能进入生产路径。

### 5.11 ops：OP-01／N-ops

来源：[账号管理](../internal/http/admin/account/admin_accounts.go)、[角色管理](../internal/http/admin/account/admin_roles.go)、[MFA](../internal/http/auth/admin_mfa_settings.go)、[审计辅助](../internal/http/admin/request.go)、[dashboard](../internal/ops/dashboard_repo.go)、HTTP admin/ops/operations.go。

目标：AccountService、RoleService、MFAService、AnnouncementService、DashboardQueryService；account／role／audit／projection repo、session_repo.go 的后台会话实现及 mfa_repo.go。审核人实时状态能力供 settlement 使用。

- [x] OP-01：先提取 Actor／AuditContext 值与 AppendAuditInTx，供前面批次复用；HTTP 不持有 AuditedTransaction 的业务提交责任。
- [x] OP-01：账号创建／修改／删除／解锁／密码重置、角色、MFA 设置迁入服务；数据范围读取归scope_repo，当前无写入注册路由，不新增API；保留最后管理员保护及角色集合的原串行化边界。
- [x] OP-01：权限实时读取，不信任 JWT／前端菜单；首签人实时权限查询通过注入回调或窄接口提供，ops 不 import settlement。
- [x] OP-01：Redis MFA pending／锁／频次 IO 由实现层提供，规则归 ops；敏感证明不进入审计或普通数据查询；原受授权目标MFA登记回执保留专用材料。原登录／刷新回执仅返回自己的凭据，口令通过的新登记回执仅返回自己的短期绑定密钥，保留原受保护契约。
- [x] OP-01：将 infrastructure/redis/admin_session.go 迁入 ops/session_repo.go，后台刷新、撤销与挑战记录由 ops 持有；保留 AuthVersion、ART_／admin:session:、绝对 TTL、轮换／撤销脚本和令牌格式。共用技术能力按第 2 节提取，不让 ops 与 identity 为复用会话实现互相引用。
- [x] OP-01：公告业务归 ops；dashboard 移成有明确来源的跨模块只读投影，保留北京时间日界。admin/ops 中告警确认转 D-02。
- [x] N-ops：业务 repo 统一命名；不要通过泛化“表名 + values”写入接口绕过所属业务模块。

验收：回归现有并发互删与角色锁真库测试；过期权限、越权范围、锁定／解锁、TOTP 重放、首签权限撤销；事务失败不留下审计成功记录；dashboard 查询失败不输出伪完整快照。

### 5.12 export：E-01／N-export

来源：[任务与清理／下载](../internal/http/admin/export/exports.go)、[CSV／XLSX 格式实现](../internal/export/format.go)、export_task_repo.go、export/cleanup.go。

现状基线：PDF 生成、字体目录与 fpdf 依赖已移除，当前仅支持 CSV、XLSX。PDF 创建请求返回参数错误，历史 PDF 任务不再提供下载；资源清单、OpenAPI 和初始化 SQL 已同步。本任务以这一实现为准，不再安排字体迁移或恢复 PDF。现有数据库未因初始化 SQL 修改自动变更。

目标：ExportService.Create／Generate／GetDownload／CleanupExpired、FormatWriter、ExportRepo；HTTP 只流式输出被授权的文件。清理循环继续由 admin 受管理运行。

- [ ] E-01：迁入资源选择、范围快照、行数限制、生成、成功／失败推进与清理；数据源使用明确只读 repo，避免复制业务写入。
- [ ] E-01：首批保持当前同步执行／受管理生命周期，不添加脱离请求 context 的后台 goroutine，也不默认把导出搬给 worker。
- [ ] E-01：下载用例复查当前权限、申请人、范围与有效期；文件路径由可信目录与任务编号推导，不信任库中任意路径。
- [x] E-01：迁入 CSV、XLSX 格式实现，保留转义、中文、缺失字段表达与 XLSX 流式写入；资源清单、格式校验、MIME、文件扩展名和 OpenAPI 保持一致。
- [ ] N-export：任务和清理数据访问改 repo；Cleaner 的实现也回 export，不由 HTTP 实现业务清理。

验收：多取一行判超限并失败、文件 IO 失败／取消后的任务状态、24 小时下载窗口、重复清理和文件已不存在、路径越界拒绝、权限撤销后下载拒绝；CSV／XLSX 格式回归通过，PDF 与未知格式请求仍被拒绝，历史 PDF 下载仍返回格式不可用。历史任务及文件的保留／清理按现有生命周期执行，不借重构修改现有库枚举或删除历史数据。

## 6. HTTP、客户端与运行时收尾

### 6.1 H-01 HTTP 目录及薄适配

业务迁出后再移动处理器，不在文件仍拥有事务时仅改目录。来源与目标按下表核对全部注册入口：

目录迁移：`internal/interface/http` → `internal/http`。同步更新 cmd、消费者 import／别名、测试中的相对源码路径和文档；全部迁出后删除空的 `internal/interface`，不保留兼容转发包。目标中的 http/user、http/admin、http/central、http/auth 均相对于 internal。

| 现有接口族 | 目标业务归属／HTTP 位置 |
|---|---|
| user_api、auth、phone、用户档案 | identity／http/user/identity 与 http/auth |
| payment_scan、payment_payment、development_payment | device 准入与 payment／http/user/payment；开发回调仍按模式注册 |
| user_wallet、card、coupon、invoice、fault、station、announcement | wallet／card／marketing／settlement／device／ops，各自 user 接口族 |
| charge_user_query、status、live_meter、feedback、user_stop | charging/query、order/feedback、charging/stop；对应 user 适配 |
| 启动／停止授权、待启动、启动／结束回执、自动停止 | charging 生命周期／http/central |
| billing、refund_internal、内部 card、告警同步 | settlement／card／device 的用例及 central 适配 |
| admin_accounts、roles、auth、mfa、ops | ops 服务；鉴权适配归 http/auth，路由仍由 admin 注册 |
| admin/order 的订单、支付、过程、case | order／payment／charging/query／device；按真实归属注入服务 |
| admin/device 的 meter_reviews | settlement，后台路由可保留原族位置 |
| admin/user 的在线卡与充值历史 | card／wallet，不能按页面所在目录判表归属 |
| admin/station、device、pricing、marketing、finance、export、vendor | 所属业务服务；vendor 使用 gateway 具名能力 |

迁移公共 Page／Actor／错误适配时拆除 admin/contracts 对巨大 HTTP 父包的业务依赖；纯分页用 persist，通用响应用 httpapi。HTTP 子包不得再经父包获得 DB、URL 或万能依赖容器。

保持 user.openapi.json、admin.openapi.json 的承载进程与地址；资源随目录迁至 `internal/http`，移动 go:embed 时核对相对路径并更新 cmd 的读取测试。实际 newRouter 的路由、认证、schema 引用与文档逐项一致。旧根包的业务实现和转发符号最终删除，是否保留纯路由汇总函数由 cmd 的实际复用决定。

### 6.2 X-01 具名客户端清单

已经具名化的 START／STOP、待启动、授权和启动／结束结果不重复迁移，只回归契约。剩余调用按下面顺序收敛：

| 消费方 | 目标服务／能力 | 对应批次与证据 |
|---|---|---|
| payment／card／user 扫码发现 | gateway 扫码解析、端口解析 | PAY-01／CA-01；参数转义、归属、未知端口与设备状态 |
| charging/query／后台实时视图 | gateway 遥测、历史曲线、采样、摘要、充电过程 | CQ-01；分页窗口、缺失字段、单位、大整数、错误和取消 |
| device 导入／厂商后台 | gateway 预置与厂商读写 | D-01；范围、身份冲突、受理与失败恢复 |
| worker 刷卡派发 | gateway card-events／决定冻结／reply／advance；central swipe／balance | CA-01；冻结原决定、回复后推进、失败原 ID 重试 |
| worker 资金循环 | central billing／refund／auto-stop | S-01／S-02／C-01；部分完成、超时、单任务失败后后续任务 |
| 告警同步 | central device-alerts/sync；用例再读 gateway 故障证据 | D-02；分清两段 base URL 和服务身份 |
| 后台 worker 运维／任务调用 | worker 的实际已注册端点 | X-01；需要时才增加 contracts/worker、integration/worker |

每条调用依次完成：核对实际路由 → 共享 DTO／端点 → 专用客户端 → 消费者窄接口 → cmd 配置／连接池注入 → 实际注册路由的契约测试 → 删除通用调用。校验成功状态、业务 code、身份、受理语义、nullable 字段和游标；传输层不擅自换业务 ID 或添加重试。

完成条件：worker/charge/sync_client.go、internaljob 的通用 HTTP 业务调用没有消费者后删除；允许 platform/serviceclient 保留为 integration 的传输实现。URL／token／通用客户端留在 cmd／integration／HTTP 鉴权适配，业务用例和 worker 的业务派发组件不持有它们。

### 6.3 G-01／WK-01 运行时和 repo

- gateway/control 里的 SQL 归 gateway/repo 的完整操作；协议解析留 protocol，命令受理与端口占用先提交，再 TCP 下发，设备事件先持久化再确认。
- worker/repo 只持有调度与投递基础设施数据；事件 Outbox 访问不扩大成订单／资金表访问。资金算法仍由中央业务模块执行。
- 保持 worker 一秒顺序：告警 → 刷卡 → 待启动 → 启动结果 → 结束结果 → Outbox；十秒顺序：自动停止 → 计费 → 退款。游标实例与轮间状态不被重新装配。
- 保持 gateway 五秒 STOP／补偿恢复及 context 取消；HTTP／TCP／循环全部退出后释放资源，不产生未受管理任务。
- N-gateway／N-worker 完成目录、package、import、类型和测试改名；不得顺带修改设备会话与命令会话、stream/source 标签或重试身份。

验收：绑定失败取消其它服务、任务失败保留恢复入口且后续独立任务执行、取消不进入下一任务、结束 central 确认后才释放端口、迟到回执不释放新订单、停机超时后仍等待处理器退出；已有 runtime 和服务装配测试继续通过。

### 6.4 INF-01 infrastructure 目录收尾

本任务依赖职责迁移完成，不把删除目录当作独立业务重构。核对以下归属：

| 当前文件 | 目标职责／路径 | 所属任务 |
|---|---|---|
| infrastructure/database/admin_dashboard_store.go | ops 的 dashboard_repo.go，只读运营投影 | OP-01 |
| infrastructure/database/admin_case_store.go | order/feedback_repo.go、device/fault_repo.go，按用途拆分 | O-01／D-02 |
| infrastructure/redis/session_store.go | identity/session_repo.go，用户会话 | I-01 |
| infrastructure/redis/admin_session.go | ops/session_repo.go，后台会话与挑战记录 | OP-01 |
| infrastructure/redis/session_token.go | 通用令牌布局与编码、摘要、原子注销归 platform/sessiontoken；随机源归 platform/auth，业务前缀归所属模块 | I-01／OP-01 |
| infrastructure/redis/session_store_test.go | 用户会话测试随 identity 迁移，共用机制测试随提取能力迁移 | I-01／OP-01 |

验收：旧包没有消费者或遗留文件；cmd、HTTP 适配、测试与源码链接引用真实新路径；identity／ops 无反向依赖；用户与后台会话原契约测试、相关装配和全仓默认检查通过。最后删除空的 infrastructure 并更新 AGENTS 的实际目录图，不添加占位包、旧路径别名或兼容转发。

## 7. 部署评估 DEP-01

前置：批次 0–5 完成；支付、起充、停机、结束、计费、退款和重启恢复已有流程证据。评估依据见 [central 的后续评估](architecture-boundaries.md#central-的后续评估)。

交付一份原位更新的比较表：保留五进程与 user + central 四进程候选，分别记录凭据／安全隔离、独立扩容／发布、资源限制、故障范围、请求与恢复成本、运维成本以及数据来源。没有实测的项明确标为未验证。

若保留五进程，写明原因并关闭评估任务。若选择合并，另行列出路由、配置、网络入口、资源生命周期、Compose／模板、切换／回退和流程回归任务；不能把“评估完成”勾成“合并完成”。本路线图不在模块迁移中执行服务合并，internal 的业务包不随进程数量复制或重建。

## 8. 验收矩阵与命令

### 8.1 检查按改动选择

| 改动 | 必须取得的证据 |
|---|---|
| 纯文档 | 链接、引用、事实、目标与现状区分、任务 ID／前置关系、Markdown 格式 |
| 注释补齐与迁移核对 | 逐字段及逐函数／方法评审；核心逻辑说明与实际分支、锁、事务和恢复一致；字段含义与 SQL／DTO／OpenAPI 一致。纯注释修改不额外重复业务测试 |
| Go 命名／职责迁移 | make check；包路径／外部依赖变化核对 go mod tidy -diff；装配变化构建受影响 cmd |
| 资金／状态／唯一键／锁／隔离／幂等／任务 claim | 默认测试 + 相关真库测试；批次退出最终全仓 integration 回归，不能用默认绿代替 |
| HTTP／DTO／客户端 | 实际注册路由测试、鉴权与输入约束、JSON／错误、OpenAPI／调用方一致；fixture 只证明客户端契约 |
| 设备与恢复 | 真实 gateway 数据／同步接口及现有模拟器流程；保留实机未验收边界 |
| 前端结果或接口影响 | 后台 npm test／npm run build；用户端 npm run typecheck／npm test，适配／构建配置改变时构建受影响目标 |
| SQL／配置／部署 | 初始化 SQL、字段 COMMENT／单位／索引／分区、配置加载、实际进程使用、Compose／模板、数据保留与回退 |

### 8.2 后端入口

在仓库根目录执行；连接变量由已有开发环境提供，不把凭证写入任务记录。

```text
make check

go mod tidy -diff

make build SERVICE=user
make build SERVICE=central
make build SERVICE=admin
make build SERVICE=gateway
make build SERVICE=worker

make test-integration TEST_MYSQL_URL=...
```

构建只跑受影响服务；跨包路径迁移影响全部 cmd 时构建全部入口。Make 的前置检查使用 Node，全部入口通过 `go build -trimpath -o bin/ ./cmd/...` 构建，产物仅落 bin；Windows 本地真库入口为 `scripts/db/test-integration.ps1`，使用 `compose.test.yaml` 独立 MySQL（3307）与 Redis（6381），不得将显式测试主键插入联调开发库。构建通过不能替代真库测试。默认缓存不可读时使用已忽略的 .tmp/go-cache 与 .tmp/go-tmp，不向其它系统目录写测试产物。

真库测试需要 TEST_MYSQL_URL，真实会话测试还需要 TEST_REDIS_URL；跨库／网关测试还需其实际使用的 DATABASE_URL_GATEWAY、DATABASE_URL_CENTRAL、DATABASE_URL_WORKER，参照现有 CI integration job 与测试连接辅助函数。迁移使用现有开发栈与 cmd/migrate；不得为了通过测试另建旁路容器、自动建表或无条件清库。每个 integration 文件必须保留 build tag，缺配置明确失败，不能跳过。

阶段内可运行受影响包的 `go test -tags=integration -count=1 <包路径>` 快速定位；这不替代阶段退出的全仓 integration 命令。测试提交结果未知或环境不可用时记为待验收，该任务不能勾为完成。

### 8.3 并发与故障证据

- 并发使用两条独立连接和 channel／指定表查询回调闸门，确定等待与放行顺序；不靠大量 goroutine 碰概率。锁匹配按 FROM 表，不能按列名中的子串。
- 校验当前读的预算／退款聚合以及 READ COMMITTED 幂等路径；事务前已建立 RR 快照不能因后续行锁而假定刷新。
- 回滚测试优先外层事务；必须提交的并发场景显式清理并核对零残留。隔离级别、行锁、唯一键、当前读或幂等比对的关键断言通过针对性变异证明有效，不保留变异代码。
- 故障覆盖提交前、提交后响应丢失、网络受理未知、重启、重复投递、身份／金额冲突、权限撤销和取消；每个点写出恢复所依赖的持久凭据。
- 模拟支付、loopback HTTP 和模拟器不等于真实资金／实机验收。测试记录说明真实 DB、fixture、stub、模拟设备各自覆盖到哪一层。

### 8.4 单任务完成定义

- [ ] 原 HTTP／通用基础设施中的业务实现已删除，只有一个权威入口。
- [ ] 输入、输出、权限、数据范围、错误、金额／单位／身份及请求 ID 契约已核对。
- [ ] 自己的表由自己的 repo 写入；跨模块调用／投影已登记理由，没有反向循环。
- [ ] 事务、锁、隔离、幂等、审计与 Outbox 保持完整；外部调用与恢复有持久依据。
- [ ] repo 名称、消费者、测试、cmd、嵌入资源、文档与 OpenAPI 同步；无失效别名／链接。
- [ ] 重构范围内字段均有中文释义；函数、方法及接口方法逐项解释全部入参、出参、错误和副作用；核心逻辑详细说明业务依据、顺序、约束与恢复。注释严肃简练，与实现一致，无口语、失效说明或仅翻译名称的占位内容。
- [ ] 适用测试与构建实际通过，资金／并发真库证据完整；未验收项没有被称为完成。

## 9. 进度与交付记录

下表是唯一的实施进度入口。实施者可在任务旁细分子项，完成后原位填写证据；不要追加新的“最终完成”章节。

| 任务 | 状态 | 完成证据／剩余边界 |
|---|---|---|
| P-01／P-02 | 完成 | AGENTS 失效文档引用已修正；依当前 SQL、路由、锁顺序核对基线。管理员真库测试隔离自身账号，保留 bootstrap 管理员及真实角色锁 |
| C-01／C-02／C-03／N-charging | 完成 | cutoff 恢复、首写保留及卡→订单锁归属已实现；gateway 实际取证到判定覆盖 10080 行分页边界；charging 持久化文件改为 repo。详细验证见 charging-review |
| S-01／S-02／N-settlement | 完成 | 计费、缺账单恢复、人工退款／开票／计量审核入口及 repo 归位；审计回滚、首签撤权、并发一单一票及注册后台审核通过。两连接旧 RR 验证原申请、首签、费用与成功回执当前读；全仓及独立暂存快照 make check／integration／cmd 构建通过 |
| F-01／PAY-01／PAY-02／N-payment | 完成 | checkout／回调及归属注入完成，支付模型与渠道中文契约已核对；注册路由覆盖原号恢复、严格输入、付款人／商户／应用／金额／交易号冲突；完整回调取消回滚通过。全仓及独立暂存快照验收通过，tidy 无差异 |
| W-01／N-wallet | 完成 | 充值／退款／风险入口及 repo 归位；11 笔申请固定 5 次 SQL；两连接验证原请求重放、频次、来源额度、待付行锁隔离、审核等锁后新到账及原成功重放；流水余额使用当前资金事实，取消与审计故障整体回滚；全仓及快照验收通过 |
| D-01 | 完成 | 导入、站点、设备运营及厂商编排已归 device，普通归属／能力和站点锁由所属 repo 提供；真实注册后台及 gateway 覆盖范围、原号重放、旧版本、未知值、提交后恢复及审计。两连接验证首次申请唯一键及反向清单统一锁序，变异稳定失败并恢复；工作区及独立暂存快照默认检查、全仓真库、全部 cmd 构建和 tidy 已通过；现有双模拟器实际 TCP／心跳／摘要／扫码及完整方案读取通过 |
| D-02／N-device | 完成 | 工单提交、查询和流转归 FaultService，状态／事件／审计同事务，后台范围一致及实时处理人准入已定向验证；告警同步与确认已归所属服务并验收；用户站点组合／公告所属读取及最后device repo已归位；精确快照全仓默认检查、完整三库、全部入口构建与tidy通过，开发原站点读取保持原空值与设备事实 |
| PR-01／N-pricing | 完成 | SchemeService／PublicationService／QueryService 接管模板、发布、继承及读取组合；五个 repo 改名并核对中文契约。真库覆盖版本、范围、兼容、审计回滚及同请求两连接重放；注册 HTTP 覆盖创建、发布、继承、模板列表、设备能力及空方案原响应。设备普通事实及锁已使用所属能力，工作区及独立暂存快照默认检查、全仓真库、全部 cmd 构建和 tidy 已通过；现有双模拟器实际 TCP／心跳／摘要／扫码及完整方案读取通过 |
| CA-01／N-card | 完成 | SwipeService、CardStatusService、CardAdminService 接管准入及完整事务；所属 InTx 能力保持资金、订单及回执边界，六个 card store 改为 repo。共享卡契约与具名客户端贯通实际注册端点、worker 及 cmd。两连接绑定、同事件扣款、续费与截止双向竞争、旧 RR 当前读、审计回滚及冻结回复恢复通过；本地 TCP 首购／续购／停机／结算退款走通。工作区及精确暂存快照默认检查、独立三库、全部入口构建与 tidy 已通过 |
| O-01／CQ-01／N-order | 完成 | 反馈、订单基础与批量历史、后台实时估算／过程、用户详情／快照／遥测／历史曲线已归所属服务；具名网关能力、私有读模型、冻结展示及表归属收敛。非法／配置禁用窗口、三年／2000桶、确认零与缺失、网关降级／503、取消及实际两库／Redis注册验证通过；工作区与精确暂存快照默认检查、独立三库、全部入口构建和tidy通过，本地原订单读取保持最终费用／退款与钱包事实 |
| M-01／N-marketing | 完成 | CouponService／ActivityService／CouponQueryService及六份repo归位，显式同事务发放／核销保留；当前预算、锁后配置、共享模板与原凭据、审计和资金回滚、注册路由和开发账户通过。工作区及精确暂存快照默认检查、独立三库、全部入口构建和tidy通过 |
| I-01／N-identity | 完成 | SessionService、PhoneService、UserQueryService 及用户／会话 repo 归位，旧实现删除；真实三库与 Redis、实际注册路由及两连接开户／唯一号码通过。公开精度、旧记录连续轮换、冻结即时生效、签发失败清理、5次批量查询和充值归属已核对；工作区及精确暂存快照默认检查、全量 integration、全部 cmd 构建、tidy 与前端通过，本地 user/admin 流程通过 |
| OP-01／N-ops | 完成 | 认证／MFA、账号角色、公告、审计查询和看板所属服务及repo归位；通用表名写入和无消费者审计事务删除。原空值、北京时间日界、未计费、当前准入及审计回滚真库／注册路由通过；最终暂存快照默认检查、完整独立三库、全部入口构建和tidy通过，开发实际读取保持 |
| E-01／N-export | 完成 | ExportService及Cleaner接管同步任务、文件发布、当前下载准入与清理，ExportRepo命名归位；两池UUID／旧RR、范围／mask、到期、取消锁与故障恢复、注册五路由及开发CSV／XLSX均通过；工作区和精确暂存快照完整检查通过，tidy无差异 |
| H-01／X-01／G-01／WK-01／N-gateway／N-worker | 进行中 | H-01四个HTTP接口面与薄适配已验收；worker资金具名消费者和N-worker持久命名／完整repo操作已验收，查询与范围归所属服务、认证单次响应保持；剩余gateway repo与两进程runtime收尾继续实施 |
| INF-01 | 完成 | dashboard最后只读投影归ops，case及两类会话已迁完；infrastructure无源码或消费者、无兼容包，AGENTS实际目录图已更新；最终暂存快照相关装配、原会话及完整检查通过 |
| DEP-01 | 待评估 | 完成代码及流程验收后评估，不承诺执行合并 |

状态只用：待实施、进行中、待验收、完成、待评估。每个任务按下面格式填写短记录；检查失败或未执行如实写明。

```text
任务 ID / 状态：
实际行为与路径：从哪里迁到哪里，删除哪个旧实现
契约与归属：输入输出、错误、表、跨模块理由
事务与恢复：锁顺序、隔离级别、幂等键、提交点、重试入口
装配与文档：受影响 cmd、客户端、OpenAPI、配置、资源
中文注释：字段覆盖、函数／方法入参与出参、核心逻辑及契约一致性核对结果
验证：实际命令与结果、真实 DB / fixture / stub / 模拟器范围、清理结果
剩余边界：未执行的验收、待定决策、当前工作区改动
```

### 下一项工作的直接入口

批次 P／0 已完成并提交；资金批次完成入口、归属、中文契约及实际锁图核对。2026-10-05 工作区及独立暂存快照 `.tmp/batch1-validation-replay` 均通过 `make check`、`go test -tags=integration -count=1 ./...`；快照另通过 `go build -buildvcs=false -trimpath -o bin/ ./cmd/...` 和 `go mod tidy -diff`（无差异）。日志为 `.tmp/batch1-check-replay-final.log`、`.tmp/batch1-integration-replay-final.log`、`.tmp/batch1-snapshot-replay-*.log`。后台 78 项测试及构建、用户端 typecheck 与 59 项测试通过；构建保留已有 Vite 大 chunk 警告。协议测试原固定延时退出改为等待实际事件记录，业务协议未改变。

变异验证包括计量当前读、审计、发票内容、充值付款人、充值／风险 RC、来源扫描锁和退款第二签当前读，均稳定失败并已恢复。资金原凭据及钱包流水的旧快照测试在修复前分别复现冲突、重复键及错误余额，修复后通过；真实提交造数按本测试身份清理并核对零残留，其他使用外层回滚。验收证据是实际 MySQL 与注册 HTTP，渠道、会话存在性和设备准入 fixture 不等于真实资金／实机验收。每批验收后提交，不推送。批次 2 按依赖拆成 2a（D-01／PR-01）与 2b（CA-01）；2a 已完成验收，2b 及其余任务仍按表中状态实施。

2a 实际证据：`.tmp/batch2-management-full-registered.log`、`.tmp/batch2-pricing-read-registered.log`、`.tmp/batch2-owned-device-facts.log`、`.tmp/batch2-import-concurrency.log`、`.tmp/batch2-import-overlap.log` 已通过。站点详情／更新范围、设备版本、首次导入缺行间隙锁和反向清单锁序变异稳定失败，日志为 `.tmp/batch2-*-mutation.log`，变异均恢复。真实提交场景清理自身身份为零，其余外层回滚；网关开通、厂商与摘要使用实际注册端点及真实库，权限会话存在性／并发开通次数部分为 fixture。厂商中央审计仍是跨服务提交后尽力记录，无持久补偿；两台现有 DC589 模拟器实际 TCP 注册、心跳、后台摘要、两端口在线空闲扫码及完整方案读取已验收；支付到结束的完整流程和实机尚未在此子批验收。

2a 退出证据：工作区 `.tmp/batch2a-check-final.log`、`.tmp/batch2a-integration-final.log`、`.tmp/batch2a-build-final.log`、`.tmp/batch2a-tidy-final.log` 与独立暂存快照 `.tmp/batch2a-validation` 的同类日志全部通过，tidy 无差异；后台 78 项测试及构建、用户端 59 项测试及 typecheck 通过。快照 Go 文件与 Git 暂存原内容核对一致，Windows 导出换行转换不作为依赖变化。真实开发栈过程见 `.tmp/batch2a-live-prepare.log`、`.tmp/batch2a-live-summary.json`：修复准备脚本的大整数 ID 精度丢失，以及无规则同站多设备缓存误判／完整方案兼容检查遗漏；新增真库测试与两项稳定失败并恢复的变异见 `.tmp/batch2a-import-*-mutation.log`。临时模拟器已停止，开发服务继续运行，准备脚本创建的开发样例保留；数据库备份保留，未再次重建。

2b 实施与行为证据：三种 card 服务位于 `internal/card/{swipe,status,admin}.go`，卡、会话、操作、用户和管理访问集中到六个 repo，payment／order／identity 提供所属事务内能力；stop.CutoffRepo 通过窄接口注入。卡→钱包→会话→订单锁序、原事件身份、首购完整支付与方案、续购原套餐、回执退款与释放保持同一事务。管理绑定先争卡号唯一键再当前读，避免缺行间隙锁；全局卡权限保留原用户归属语义。字段、方法入参与结果、错误及副作用的中文契约已核对；卡号行政绑定1–19位与设备32位范围分别保留。

2b 验证：`.tmp/batch2b-card-current-read.log`、`.tmp/batch2b-card-binding-concurrency.log`、`.tmp/batch2b-card-registered-routes.log`、`.tmp/batch2b-card-admin-registered.log`、`.tmp/batch2b-card-concurrency-final.log`、`.tmp/batch2b-card-clients.log` 和原冻结回复恢复测试通过。两连接按门控固定同事件／首绑等待与续购先提交、截止先提交；核对实付、钱包、购买分钟、凭据和截止。截止当前读、原操作当前读、绑定当前锁三项变异稳定失败并恢复。真实库及注册 HTTP 使用外层回滚或自身身份清理；会话存在、设备准入和回复丢失部分为 fixture，不等于实机证明。服务端／客户端共享端点及 DTO，5秒上下文期限、禁止重定向、有界响应，只有409/2009映射明确拒绝；缺字段不得当作正常零值。

2b 开发流程证据：`.tmp/batch2b-live-{summary.json,central-facts.log,gateway-facts.log,final-facts.log}` 及 `.tmp/batch2b-simulator.log`。实际登录、两笔模拟充值、绑定、原生 TCP B6 首购、B7/B8 启动、移卡重刷续购、用户停机、B9/BA/BB/BC 回执、完成与后续结算退款。首购／续购各100分，累计120分钟，唯一START使用首次余额190个协议单位，两个操作均confirmed且原事件done；两端端口均释放。3秒低额计费按原规则归零，异步全退200分，最终钱包2000分。模拟支付与模拟器不替代真实商户或实机。

2b 环境问题：先前真库测试虽清理行，显式高位主键仍抬高开发自增序号，导致19位新订单不能编码16位 BCD；未改变协议或业务身份绕过。测试已隔离独立三库3307，新增 `compose.test.yaml` 和 `scripts/db/test-integration.ps1`；实际入口迁移与全量测试通过。按已有明确授权备份并重建开发栈，保留 `.tmp/dev-db-before-reset-20261005-090128.sql` 及前两份备份，恢复样例后 TCP 全流程成功。开发 Compose 改用仓库实际 SQL 初始化文件，重建脚本停下并恢复五个写入进程；Make 验收移除 Bash 条件／循环。

2b 退出证据：工作区 `.tmp/batch2b-check-final.log`、`.tmp/batch2b-integration-final.log` 及实际独立测试入口 `.tmp/batch2b-test-entry.log` 通过；精确暂存快照 `.tmp/batch2b-validation` 的 check／integration／build／tidy 日志 `.tmp/batch2b-snapshot-*.log` 全部通过，tidy 无差异，575份 Go 与模块原 blob 完全一致。暂存快照初验发现结算准备路由测试的 Scopes 条件会被前次查询消费，包间扫描锁可能冲突；改用本测试连接的意图更新回调持续限定本用户后，工作区及快照全量真库通过。后台78项、用户59项测试及类型检查、后台生产构建通过，保留既有 Vite 大 chunk 提示。前端使用直接 Node 执行既有脚本；pnpm 自动安装生成的未跟踪锁文件已移除，未修改依赖清单或锁文件。

批次2b与3a身份部分已完成；批次3继续订单组合查询及营销边界，后续故障／运营／导出及HTTP／基础设施任务仍待完成。开发样例及本次模拟充值／充电／退款保留；临时模拟器已停止，五个开发服务继续运行；后续真库测试只使用3307独立实例。

批次3a（I-01／N-identity）完成：身份服务从 HTTP 迁入 identity/session.go、phone.go、query.go；用户与 Redis 持久实现改为所属 repo，列模型归 user_schema。通用 sessiontoken 无业务前缀；后台会话实现暂留 infrastructure，OP-01／INF-01 未完成。新用户与零余额钱包保持同事务，身份唯一锁行保留恢复依据；正式号码来源只接收微信代码，开发来源在 cmd 单独装配。后台身份只读跨域投影及 identity → wallet、payment → wallet 的充值读取已登记 architecture-boundaries；旧支付用户存在性／充值组合入口删除。

3a 真实证据：`.tmp/batch3-identity-all-targeted.log`、`.tmp/batch3-identity-concurrency.log`、`.tmp/batch3-identity-routes.log`、`.tmp/batch3-user-query-routes.log`。两连接按身份锁与号码预查闸门固定并发，核对一用户一钱包、首次 unionID、失败开户回滚、号码唯一归属及 NULL 解绑；提交造数清理自身记录。真实 Redis 验证并发轮换仅一获胜、previous 注销、KEEPTTL、旧大整数连续两轮精确保存、格式与取消、真实 Lua 存储故障。注册 user/admin 使用真实 DB／Redis，微信交换为 fixture；冻结、签发失败清理、用户精度、全局 user.read 即时撤销、充值范围及原金额通过。列表一名及11名用户固定5次SQL。`.tmp/batch3a-mutant-{storage-classification,legacy-precision}.log` 的针对变异稳定失败，恢复后 `.tmp/batch3a-mutants-restored.log` 通过；后台共享注销及故障分类在真实 Redis 通过，不把它称为 OP-01 全部完成。

3a 退出证据：工作区 `.tmp/batch3a-{check,integration,build,tidy}.log`；精确暂存快照 `.tmp/batch3a-validation` 的 `.tmp/batch3a-snapshot-{check,integration,build,tidy}.log` 均通过，585份 Go／模块文件与暂存原 blob 一致，tidy 无差异。CI 及独立测试入口增加 TEST_REDIS_URL，测试 Redis 6381 不访问开发实例。后台78项、用户59项测试及类型检查、后台构建通过，保留既有 Vite 大 chunk 警告。user/admin 开发进程已重启，`.tmp/batch3a-live-identity.json` 记录实际登录、档案、开发号码绑定／解绑、刷新、原令牌注销后访问401、后台档案与原充值页；未触发真实微信登录或手机号渠道。

批次3b（O-01反馈部分）实施：用户评价及后台反馈列表／回复／关闭归 order.FeedbackService，表访问归 feedback_repo；HTTPS 附件规则与测试迁入订单。device/user_fault_store 的反馈写入删除，infrastructure/database/admin_case_store 拆为订单反馈与 device/fault_repo，删除 CaseListSpec／CaseStore 通用表名选择。报修原 HTTP 编排尚待 D-02，订单基础 repo 命名和 O-01／CQ-01 组合查询尚未完成。

3b 真库证据：`.tmp/batch3b-feedback-{repo,routes,restored}.log`。两独立连接按订单锁门控验证只提交一评价；旧 RR 快照早于另一连接提交时，SHARE 当前读拒绝重复。`.tmp/batch3b-mutant-feedback-current-read.log` 移除当前读后稳定失败，原实现恢复通过。注册 user/admin 使用真实用户、订单、后台权限及 Redis，覆盖数字／业务号归属、未完成拒绝、评分／附件、原重复409、后台身份精度与图片数组、回复与关闭重放、即时撤权、审计503及整体回滚。报修所属 repo 回归 open→dispatched→fixed→closed，重复指派不增事件、跳级拒绝、历史恰好3条；不将此记为 D-02全部完成。外层事务回滚或清理自身提交行并核对零残留。

3b 退出证据：工作区 `.tmp/batch3b-{check,integration,build,tidy}.log` 与精确暂存快照 `.tmp/batch3b-validation` 的 `.tmp/batch3b-snapshot-{check,integration,build,tidy}.log` 均通过，589份 Go／模块原 blob 一致，tidy 无差异。实际重启 user/admin 后，`.tmp/batch3b-live-feedback.json` 验证原2b已退款订单的用户提交、原重复409、后台回复／关闭／关闭重放，保留一份已关闭开发反馈并注销测试会话。公开 HTTP 字段与 OpenAPI 未改变；订单查询、CQ-01、营销和后续批次仍待完成。

批次3c（O-01基础查询／N-order）实施：history、state、view、package、pricing_snapshot 改为 repo，编号 SQL 从纯编号规则拆到 order_number_repo；订单事件／后台基础投影从 ops／settlement 归 order/admin_query_repo。QueryService 接管原用户归属、正在充电、历史及后台列表／档案／时间线；FeeReader 由 cmd 注入 settlement/query_fee_repo，一次批量读取确认费用，保持无回执 null 与确认零值的区别。包内订单 builder 不再对外导出。原全局 order.read 权限、用户隔离、分页、原身份及冻结方案契约保持；修复站点投影缺少明确表名。后台详情输出实际补充后的行，实时视图完整证据仍随 CQ-01 验收。字段与函数／方法中文契约已核对，组合用户详情及实时查询尚未完成。

3c 定向证据：`.tmp/batch3c-order-query.log`、`.tmp/batch3c-order-query-routes.log` 验证实际 MySQL、Redis 和注册 user/admin 路由，覆盖一笔与多笔历史均三次 SQL、他人隔离、费用无回执／无效内容／确认零值、冻结名称／站点、事件与缺行、取消、费用故障503和即时撤权。`.tmp/batch3c-mutant-{station-table,history-owner}.log` 变异稳定失败，恢复后 `.tmp/batch3c-mutants-restored.log` 通过；数据使用外层回滚及自身 Redis SID 清理。工作区全仓验收通过，精确暂存快照证据见下。

3c 退出证据：工作区 `.tmp/batch3c-{check,integration,build,tidy}.log` 与精确暂存快照 `.tmp/batch3c-validation` 的 `.tmp/batch3c-snapshot-{check,integration,build,tidy}.log` 全部通过，596份 Go／模块文件与原暂存 blob 一致，tidy 无差异；后台78项、用户59项测试、两端类型检查及后台构建通过，保留已有大 chunk 提示。重启 user/admin 后 `.tmp/batch3c-live-order-query.json` 确认原2b已退款订单、零确认费用、冻结套餐、站点及5条事件。联调脚本首轮注销误将刷新凭据放在请求体，改用原 Bearer 契约后重跑通过；首轮两份开发会话按原TTL自然过期，未清空共享 Redis。只读联调未新增业务订单或修改资金；CQ-01、营销和后续批次继续实施。

批次3d（CQ-01实时估算子项）实施：原 HTTP LiveMeterService 与后台逐单并发补查删除，权威入口归 charging/query.LiveService，观测转换归 live_evidence。后台装配现有具名 gateway 客户端及订单／卡所属批量 repo；21单只发2次外部取证、中央快照／累计购买各1次SQL。4秒整体期限内顺序执行有界批，取消不启动后续调用，返回前无未管理任务；单批外部失败范围最多20单，其它批可继续。原完整证据才估价，缺失快照／卡购买及不足计量保留原提示，不计断线墙钟时间或覆盖确认费用。删除无生产消费者的 admin/order/order_package 重复实现，额外断言随权威 domain 规则迁移；不留兼容别名。用户组合详情、曲线／遥测和后台过程尚待完成。

3d 定向验证：`.tmp/batch3d-live-integration.log` 使用真实中央／网关库和已注册网关路由验证21单调用／SQL次数、原价格、刷卡累计及零购买、错端口隔离与断线60秒不增长。`.tmp/batch3d-live-registered.log` 通过真实 Redis 与后台注册列表／详情，实时估算3分与已确认0分独立保留。取消、独立批失败、不完整／缺失响应及所属存储故障由单元 fixture 验证。`.tmp/batch3d-mutant-{wall-clock,zero-card}.log` 均在对应断言稳定失败，恢复后 `.tmp/batch3d-mutants-restored.log` 通过；首次未设置本地Go缓存造成环境失败已排除，不作为变异证据。真库造数外层回滚，Redis只清理自身SID；工作区全仓默认检查／integration／全部 cmd 构建及 tidy 无差异、后台78项与用户59项测试及类型检查／后台构建均通过；精确暂存快照验收通过，证据见下。

3d 开发流程：`.tmp/batch3d-live-summary.json`、`.tmp/batch3d-live.log`、`.tmp/batch3d-simulator.log` 记录原卡用户再次实际 TCP 起充，后台列表／详情均返回60秒、0.06kWh、估算1分；STOP及结束回执后最终确认1分、原卡退款99分成功，设备无充电会话。该次模拟器物理输入设为3600W以产生可精确表示的整Wh观测，未更改计费规则或生产配置。测试会话已注销，核对自有二进制路径后停止临时模拟器，开发五服务继续运行。实际数据为订单2，开发余额由2000分净变为1999分；模拟卡支付和设备不代表真实商户／实机验收。

3d 退出证据：工作区 `.tmp/batch3d-{check,integration,build,tidy}.log` 与精确暂存快照 `.tmp/batch3d-validation` 的 `.tmp/batch3d-snapshot-{check,integration,build,tidy}.log` 全部通过，599份 Go／模块原 blob 完全一致，tidy 无差异；前端验收日志为 `.tmp/batch3d-admin-{tests,typecheck,build}.log` 和 `.tmp/batch3d-mini-{tests,typecheck}.log`，保留已有 Vite 大 chunk 提示。`.tmp/batch3d-live-final-account.json` 实际核对钱包1999分、无正在充电订单；原最终结算与退款已确认，测试模拟器已停止。CQ-01其余读取、营销、故障／运营／导出、HTTP／客户端／基础设施与部署评估仍按表中状态实施。

批次3e（CQ-01后台过程与查询契约子项）实施：后台过程代理及重复DTO从 HTTP 删除，charging/query.ProcessService 先经 order.QueryService.ProcessIdentity 校验原 order.read 与未删除订单，再将规范业务号／设备／端口及原分页传给具名客户端；身份投影从 ops/admin_audit_store 归 order/admin_query_repo，无反向依赖。HTTP只负责整数／重复输入约束、身份适配和错误／响应。查询DTO及端点归 contracts/gateway/query，TelemetryAPI实际遥测／历史／过程响应使用共享契约，SQL列模型仍留网关所属持久层。具名三读取强制5秒、禁止重定向、信封及必需字段校验，保留null、零、原单位、摘要JSON和uint64游标，不换ID或自动重试；用户遥测／历史消费者及组合详情尚未迁移。

3e 定向验证：`.tmp/batch3e-query-clients.log` 是loopback客户端契约，包含参数转义、原时刻、可空零、原摘要大整数及近uint64上界分页、缺字段／坏游标／业务失败／重定向／取消；不把fixture称为真实网关。`.tmp/batch3e-query-registered.log` 使用真实网关库及实际注册路由，验证原始端口隔离、聚合摘要DECIMAL字符串、过程单位／空值及两页精确主键。`.tmp/batch3e-process-registered.log` 使用真实两库／Redis与后台注册路由，验证规范订单不可被额外输入覆盖、末页、缺单404、依赖503及撤权403。造数外层回滚，Redis只清自身SID。工作区全仓默认检查／integration／全部入口构建及 tidy 无差异均通过；后台78项、用户59项、两端类型检查及后台构建通过，保留已有 Vite 大 chunk 提示。精确暂存快照 `.tmp/batch3e-validation` 的 check／integration／build／tidy 全部通过，604份 Go／模块文件与原暂存 blob 一致，tidy 无差异；日志为 `.tmp/batch3e-snapshot-{check,integration,build,tidy}.log`。

3e 恢复与开发证据：`.tmp/batch3e-mutant-{cursor,canonical-identity}.log` 分别破坏原游标和规范设备，均在预期断言失败；恢复后 `.tmp/batch3e-mutants-restored.log` 通过。实际重启 admin/gateway 后，`.tmp/batch3e-live-process.json` 与日志验证开发订单2的6条原过程记录、3页遍历、60秒／0.06kWh和3600W单位，末页游标null；只读取原持久事实、未创建模拟器或修改资金，测试后台会话已注销。用户查询消费者、其余业务边界及后续批次仍待完成。

批次3f（CQ-01用户组合查询）实施：HTTP中的方案、费用、卡操作、截止及曲线业务删除，UserService先核对原用户／订单再组合所属只读能力；原方案文案归 user_display，卡操作SQL归 card/query_operations_repo，UserStateRepo只调用各所属读取，不新增资金写入或改变事务。私有读模型保持原省略、null、零、数组及金额／计量单位；当前快照网关失败仍保留原确认事实，曲线失败503。修复原窗口未核对AllowedWindows的问题，非法或禁用值明确400；保留三年和2000桶限制。费用、支付存储故障明确503，不再静默视为无回执／无支付。user OpenAPI修正原错误粒度minute/hour、points与快照响应及费用／截止字段，记录原实际形状与上述故障行为。字段、方法及窄接口中文契约已核对。

3f定向证据：`.tmp/batch3f-unit.log`、`.tmp/batch3f-user-registered.log`、`.tmp/batch3f-query-summary.log` 及恢复日志验证取消、先归属再补充、配置窗口、2000允许／2001拒绝、粗粒度、三年、冻结方案、原支付退款／卡操作／截止、确认零费用、缺失省略、遥测端口隔离及零／null、实际存储故障503、网关快照降级和曲线503。注册验证使用3307真实两库、6381真实Redis和实际网关注册；造数外层回滚，Redis只清自身SID。窗口、确认电量和费用故障变异在预期断言失败并恢复；首个窗口变异造成未使用变量的编译失败已排除，修正变异后才计入证据。

3f开发读取还发现网关加权平均功率的average_value未映射到Avg，原摘要为空串；补显式列映射，真实单桶及不同count的两桶187.59375W均通过，移除映射变异稳定失败并恢复。实际重启user读取原开发订单2，最终确认电量是0.0620kWh／62秒（此前实时观测60秒／0.06kWh不等于最终回执），确认费用1分、原退款99分、钱包1999分；初版联调脚本错用钱包路径及把实时观测当最终电量的断言已修正，失败轮次仍执行会话注销。开发读取不改变业务事实，修正摘要后实际重启gateway并核对avg_power_w为3600W；`.tmp/batch3f-live-user-query.json` 与日志保留原最终事实，最新30分钟窗口已无观测时正常返回空数组，原历史仍有聚合点。工作区 `.tmp/batch3f-{check,integration,build,tidy}.log` 全部通过，tidy无差异；后台78项、用户59项及两端typecheck／后台构建通过，保留已有Vite大chunk提示。精确暂存快照 `.tmp/batch3f-validation` 的 `.tmp/batch3f-snapshot-{check,integration,build,tidy}.log` 全部通过，833份暂存文件中的609份Go／模块与原blob完全一致，tidy无差异。O-01／CQ-01／N-order按上述证据完成；营销、故障／运营／导出及后续批次继续实施。

批次3g（M-01／N-marketing）实施：模板、人工发放、活动配置及用户持券归入所属服务；权限、优惠互斥、有效期、预算、触发不可变及邀请模板准入从HTTP删除，六份store改repo，无兼容转发。发放／核销／活动使用显式同事务能力；私有JSON读模型保持原金额、null、用户精度、双count／total及数组。修复活动repo使用事务外连接导致审计回滚无效的问题；原请求重放优先、用户锁归identity能力，当前计数与凭据覆盖旧RR快照。活动使用锁后配置、按主键锁全部模板，并保留券／来源共享预算与跨来源单人份数口径；旧0单人配置仍默认一份。

3g定向证据：`.tmp/batch3g-service-integration.log`、`.tmp/batch3g-registered.log`、`.tmp/batch3g-billing-atomicity.log` 验证独立连接审计回滚、旧快照额度及原号、不同触发共享模板预算、锁前配置变更／停用、邀请两份真实插入和更新后原模板重放、百分比冻结费用、核销同支付重放／跨支付拒绝／取消／回滚、用户范围与展示过期／null、注册后台准入400／存储503／撤权403及原Snowflake审计。核销原价1001分、折扣75%保持250分优惠，避免从实付再算；时长列改读free_minutes，原零服务费兜底及套餐不组合规则保持。邀请旧65字符事件键超出CHAR(36)，改确定32字符键。营销存储故障返回资金事务并回滚费用；正常不适用仍完成结算，删除旧日志吞错。

3g验证纠正：初版共享模板闸门停在规则读取后，优化器全表锁扫描使去掉模板锁的变异也通过，未计为模板锁证据。改为候选ID后PRIMARY按原ID当前锁定配置，并将闸门停在模板读取返回后；修正测试后，去掉模板锁、去掉当前总量读、使用事务外活动连接、吞掉结算营销错误的四个变异均在预期断言失败，已恢复，日志为`.tmp/batch3g-mutant-*.log`。完整三库真测只使用3307／6381，已把Makefile和旧营销测试说明的开发3306例子改成独立测试栈。

3g开发验收：重启user／admin／central／worker，`.tmp/batch3g-live-marketing.json` 和日志核对原开发用户模板1／发放1、同号同ID、新号单人额度409、两条用户原查询和后台统计一致、活动1保持disabled、钱包仍1999分。保留一份开发模板／权益及停用活动，无充值、订单或资金写入；测试会话均注销。工作区完整检查、三库integration、全部入口构建与tidy通过，后台78项、用户59项、两端typecheck及后台构建通过；保留原Vite大chunk提示。精确暂存快照 `.tmp/batch3g-validation` 的 `.tmp/batch3g-snapshot-{check,integration,build,tidy}.log` 全部通过，842份暂存文件中的618份Go／模块与原blob一致，tidy无差异；最终仅补文档退出记录。M-01／N-marketing完成，D-02／运营／导出及HTTP、worker／gateway和部署任务继续。

3g提交后复核纠正：`c71f143` 的营销精确暂存快照完整通过，提交钩子默认检查通过。随后工作区最终integration出现上一批注册历史边界测试偶发失败：原结束时间为当前前一分钟，却用当前墙钟减2000桶构造开始时间；尾桶可能比当前桶早一桶，实际只有2000桶，200响应正确。测试已改为以原结束桶构造包含首尾的2000／2001桶，先验200允许再验2001拒绝；不修改曲线业务。已扩展到桶内0／8分钟／14分59秒三种相位；初版第三相位构造未来结束而被网关正常拒绝503，改用完整过去桶后定向`.tmp/batch3g-history-boundary-phases.log`通过。精确暂存快照 `.tmp/batch3g-boundary-validation` 的默认检查和完整三库日志 `.tmp/batch3g-boundary-snapshot-{check,integration}.log` 均通过，618份Go／模块原blob一致；工作区 `.tmp/batch3g-integration.log` 全量通过。此修正只改带integration标签的测试与记录，业务与全部入口构建产物来源不变。失败原日志保留为`.tmp/batch3g-integration-history-boundary-failure.log`。

批次4a（D-02设备工单）开始：已有FaultRepo的表归属保持，从user_fault与admin/order/casework迁入完整提交／指派／处理事务与只读查询；下一步统一后台列表、历史和写入的实时数据范围。用户只能查询自己提交且user_visible的事件；兼容原报告未保存port_no及允许未知设备号的历史行为，不凭缺失元数据补造设备事实。告警同步／确认留4b同一D-02任务继续，暂不勾选完成。

4a定向证据：`.tmp/batch4a-targeted.log`验证真实MySQL／Redis及注册用户／后台接口，报告首事件失败回滚、指派／修复审计失败整体回滚、线性状态与同人／同状态重放、原null及用户可见性、范围外列表空页／历史与修改404、归属存储故障503和实时撤权403。两独立连接固定工单锁顺序，重放仅一条指派事件；事务先读取范围再停用处理人，等待后的当前账号准入拒绝。`.tmp/batch4a-mutant-{current-assignee,write-scope,audit-atomicity}.log`三项变异均在预期断言失败并恢复。提交造数按自身身份清理并核对零残留，其余造数外层回滚；未访问开发数据库或更改资金事实。

4a开发验收：重启user／admin后，`.tmp/batch4a-live-fault.json`及日志记录原开发用户在原设备提交工单1，重复指派、跳级409、重复修复和关闭后状态closed，后台／用户各4条原可见事件，无重复；钱包仍1999分，会话已注销。保留一份已关闭开发报告，不等于实机故障和修复。工作区默认检查、完整三库integration、全部入口构建与tidy通过；后台78项、用户59项测试及两端typecheck／后台构建通过，保留既有Vite大chunk提示。告警同步／确认继续4b，D-02整体仍进行中。

4a退出证据：精确暂存快照`.tmp/batch4a-validation`的`.tmp/batch4a-snapshot-{check,integration,build,tidy}.log`全部通过，847份暂存文件中的623份Go／模块与原blob逐字节一致，tidy无差异，Markdown本地链接有效。最终仅补本段退出记录；已关闭开发工单和前三份数据库备份保留。

批次4b（D-02告警）实施：HTTP同步与确认事务迁入device，alert_sync_store改alert_repo，删除ops告警表访问；worker及用例通过共享DTO、具名gateway／central能力调用。修复after_key被先解析after拒绝及最近故障水印倒退；原设备／协议／时刻和状态存在性核对避免伪恢复，确认列表与修改使用同一范围。用户站点组合与最后device持久文件继续下一子批，N-device不提前完成。

4b定向证据：`.tmp/batch4b-{client,unit,sync-integration,registered}.log`验证原64位身份、参数转义、缺失／矛盾响应、重定向／取消、真实两库与注册gateway、标记实际提交后丢弃响应及下一轮重扫、原证据重放、持续／乱序／恢复后新故障、旧水印CAS与单条坏载荷不阻塞另一故障。两独立central连接在首次缺行后固定并发，RR仲裁只一条活动告警，冲突返回错误并由原扫描恢复；真实提交造数按原身份清理并核对零残留。注册后台真Redis验证列表／认领范围404、审计503整体回滚、重复一审计、撤权403，以及worker→central→gateway实际触发链路。设备源为协议fixture，不代表实机。

4b验证纠正：初版真库fixture重复扫描未清空目标结构主键，GORM自动附加旧ID条件；修正后最新告警与CAS测试通过，原失败日志保留。增加存在性验证时初版把PortStates视为数值数组，真实protocol.Event的[]uint8实际为base64字符串；两库原载荷发现错误后修正并加入原Marshal回归。该轮同设备变异因编码错误失败，未计为变异证据。修正后水印、同设备、恢复CAS、首次缺行锁、显式状态及认领审计六项变异均在预期断言失败并恢复，日志`.tmp/batch4b-mutant-*.log`。

4b全仓纠正：首次完整integration中的旧worker同步测试因新fixture自增事件ID碰撞其显式高位预留主键而失败，原日志保留为`.tmp/batch4b-fixture-auto-id-collision.log`。新源fixture及后台告警fixture改用独立较低显式ID，仍大于JS安全整数以验证原精度；各测试连接持续限定自身设备，避免跨包扫描他人提交证据。工作区完整三库回归重新通过，不修改生产主键分配或清空数据库。

4b开发证据：重启admin／gateway／central／worker后，`.tmp/batch4b-live-alert.json`及日志使用原DC589模拟器实际TCP上报端口故障3／4、后台两次确认和完整故障心跳，同一告警1保持acknowledged且只有一条活动记录；清除故障并实际心跳后变auto_resolved。钱包仍1999分，无充电订单或资金写入，测试会话已注销；保留已恢复告警及原设备证据，不等于实机验收。后台78项、用户59项及两端typecheck／后台构建通过，保留已有Vite大chunk提示。补齐原后台告警GET缺失OpenAPI并登记范围／事务故障契约。

4b最终复核补充：worker共享HTTP池原5秒超时会先于central受管理25秒轮次截止；具名同步客户端复制请求Client并设30秒，复用原Transport且不修改共享池。新增原池1纳秒／处理器20毫秒的实际loopback回归，核对触发成功、原池配置不变、取消及缺失进度／重定向拒绝；`.tmp/batch4b-central-client.log`通过。最终源码仍需完整快照验收后提交。

4b退出证据：最终精确暂存快照`.tmp/batch4b-validation`的`.tmp/batch4b-snapshot-{check,integration,build,tidy}.log`全部通过，857份暂存文件中的633份Go／模块与原blob逐字节一致，tidy无差异。复核发现旧告警存储文档链接已失效，改指向所属repo；最终仅补文档记录。admin／worker已重启加载最终客户端与OpenAPI，原告警恢复和钱包事实继续保留。

批次4c（用户站点／N-device）实施：公开站点分页、详情与运行组合从HTTP迁入UserStationService；最后device/interface_query_store拆为user_station_repo和ops/announcement_query_repo，公告规则归ops.AnnouncementQueryService，跨模块通过窄接口注入。删除HTTP自建服务客户端、URL／令牌和旧别名。保留无登录公开路由、未定位10／定位20、DECIMAL坐标、null距离、原公告候选窗口及字符串目标；设备摘要101台分两批，未上报与未开通／网关故障分别保留已知／未知，不补造离线事实。

4c定向证据：`.tmp/batch4c-unit.log`及`.tmp/batch4c-station-registered.log`通过。真实两库和实际注册网关覆盖原坐标、半径／无定位、启用／未删除、公告范围／有效期、101台分批及退休过滤、null／离线／未知、公告故障503和取消；外层造数回滚，无Redis或资金修改。单元fixture覆盖201台三批中间批失败隔离、错设备／重复身份拒绝、取消停止后续及坏输入先拒绝。待全仓退出检查后提交。用户新增具名客户端Request／Reply后缀要求已写AGENTS及结构文档，下一独立批次核对并改名既有契约，暂不记录为完成。

4c验证补充：启用站点过滤、公告范围、网关摘要身份三项变异均在指定断言失败并恢复，`.tmp/batch4c-mutant-*.log`与恢复日志保留。首轮完整integration中旧支付准备注册测试偶发503而隔离重复10次通过；原失败日志保留，调试完整运行通过，不将未获得底层原因的失败定性为站点改动。核对发现结算旧fixture仍用可被前次查询消费的Scopes限定意图扫描，改为持续UPDATE回调；两份旧fixture都通过Statement.AddClause明确加入自身用户条件，避免包间扫描他人外层事务，不改生产过期逻辑。

4c退出证据：精确暂存快照`.tmp/batch4c-validation`的`.tmp/batch4c-snapshot-{check,integration,build,tidy}.log`全部通过，862份文件中的638份Go／模块与原blob逐字节一致，tidy无差异，本地文档链接有效。重启user后`.tmp/batch4c-live-station.json`读取原开发站点及两台设备、原null距离和公开公告，无业务写入或新会话。此次未修改前端及公开JSON字段；前端最近完整验收为4b的78／59项及类型检查／构建。最终仅补本退出记录，Request／Reply命名下一批继续。

批次4d（具名客户端DTO命名）实施：按用户补充要求，central／gateway端点请求统一Request，端点响应统一Reply；原Data／Result／Acceptance／Query／Input及卡操作等根DTO直接改名，同步所有客户端、服务端、窄接口和测试，无旧类型转发别名。私有解码信封及字段存在性结构也以Reply命名；集合记录、事件及曲线点保留业务名。扫码增加共享ScanRequest／ScanReply，网关显式映射所属读模型、DiscoveryService显式转为领域结果后合并中央运营状态，客户端不再直接反序列化领域读模型。原HTTP路径、JSON字段、整数／可空／省略、单位与冻结内容保持；补齐触及共享字段的中文语义。

4d验证：`.tmp/batch4d-check.log`默认检查和`.tmp/batch4d-integration.log`完整独立三库通过，包含注册扫码／卡操作、启动结束同步、查询、导入厂商和告警既有契约验证；未加入只对类型名字的镜像测试。最终源码与中文说明仍需精确暂存退出验收，H／X／gateway／worker整体不因此提前完成。

4d退出前纠正：首次命名快照全仓通过，删除HTTP残留StartResult／EndResult别名后的回归发现旧身份并发测试将请求启动顺序当作首次成功提交顺序；实际双方同ID且只有后发请求IsNew=true，原登录允许死锁有界重试，旧断言不适用。只修测试为恰好一个创建者、unionID匹配实际创建者、一用户一钱包及自身清理零残留；新增首次锁后1213故障fixture核对真实事务回滚和原重试，不宣称该fixture是引擎死锁证据。生产登录代码不变，`.tmp/batch4d-login-retry-proof.log`两路径重复5次通过；原失败日志保留为`.tmp/batch4d-login-first-winner-fixture-failure.log`。五个开发服务已重启，`.tmp/batch4d-live-scan.json`验证实际匿名设备／端口扫码保留原设备、两端口和中央运营状态，无资金写入。

4d最终退出证据：`.tmp/batch4d-validation`最终快照的`.tmp/batch4d-snapshot-{check,integration,build,tidy}.log`全部通过，862份暂存文件中的638份Go／模块与原blob逐字节一致，tidy无差异、Markdown本地链接有效。旧HTTP回执别名已删除，所有当前具名客户端根请求／回复与私有解码类型采用Request／Reply；原JSON及冻结载荷不改。最终仅补本退出记录；前端源码／接口形状未改，最近完整前端验收仍为4b。继续OP-01／导出及HTTP／运行时／基础设施／部署评估。

批次4e（OP-01后台会话repo）实施与证据：后台SessionRepo及真实Redis测试从infrastructure/redis迁入ops，旧实现和源码消费者删除，cmd和注册HTTP测试直接装配所属repo，无别名。ART_、admin:session:、七天绝对寿命、previous仅注销、五分钟登录挑战及AuthVersion归属保留。真实Redis复现旧数值ID／权限版本经cjson轮换后精度丢失，原失败见`.tmp/batch4e-legacy-precision-before.log`；共用RotateScript从原JSON恢复十进制文本、新记录用JSONUint64字符串编码，identity与ops互不引用。边界编码、原精度连续两轮、KEEPTTL、两真实调用仅一获胜、旧令牌注销、取消、Lua存储故障与挑战隔离通过，日志`.tmp/batch4e-{codec,session-real-redis,mutants-restored}.log`。精度和摘要竞争两项变异在指定断言失败并恢复，`.tmp/batch4e-mutant-{precision,digest}.log`保留；只清理自身测试SID／挑战，未访问开发Redis。

4e退出证据：工作区`.tmp/batch4e-{check,integration,build,tidy}.log`及精确暂存快照`.tmp/batch4e-validation`对应四份`.tmp/batch4e-snapshot-*.log`全部通过；864份暂存文件中的640份Go／模块与原blob逐字节一致，tidy无差异、本地文档链接有效。重启admin后`.tmp/batch4e-live-session.json`验证实际登录、刷新保持原账号／权限、注销后401，不记录令牌，不修改开发账号安全设置或资金。前端代码／公开JSON未改变，最近完整前端验收仍为4b。后台HTTP认证、MFA设置／锁／频次、账号角色与运营事务尚待迁移；现有登录挑战读取不消费，HTTP将挑战当刷新令牌撤销的原错误及挑战版本准入继续下一认证批次纠正，OP-01／INF-01不提前完成。

批次4f（OP-01后台认证／MFA）实施：AdminAuth只保留HTTP输入、Bearer提取、上下文和原响应映射，删除原认证编排、JWT声明、Redis频次、MFA设置SQL事务及缓存IO；SessionService／MFAService分别拥有完整顺序，cmd注入账号、会话、专属JWT、AuthRateRepo及MFARepo。原路径、公开字段、八小时访问期限、五次一分钟窗口、五分钟登记及错误状态保持。修正原HTTP把登录挑战当刷新令牌撤销而未消费的问题；原密码验证账号／版本在SQL行锁后核对，锁定账号拒绝，好码消费原CAS挑战后才写成功审计并发会话。签发失败尽力撤销新会话；取消后的原context清理可能失败，测试核对仍受原TTL限制，不宣称已注销。设置同账号租锁、SID／版本／标识／绝对期限、坏码不消费、版本提升及审计事务归ops，无旧入口别名；字段和方法中文契约已核对。

4f定向证据：`.tmp/batch4f-security-{flow,registered,all}.log`及`.tmp/batch4f-mutants-restored.log`使用真实MySQL／Redis，验证实时权限删除、改密及刷新失效、旧挑战版本、锁定准入、成功消费后重放、原公开回执和敏感证明不进入审计。两个独立连接在两个原Redis读取后固定放行，恰好一个会话／成功审计；旧RR快照早于版本提交时，当前锁读拒绝且不消费。消费后注入审计故障回滚SQL，挑战／设置凭据保持已消费、须重新登录／登记，不宣称Redis与SQL跨存储原子提交。挑战消费、版本、当前锁读及设置审计四项变异均在指定断言失败并恢复，`.tmp/batch4f-mutant-*.log`保留；去掉当前锁读时会在后续档案阶段拒绝但已错误消费挑战，针对该事实的断言失败，不把它描述为成功签发。提交fixture创建自己的自定义角色／账号并清理表、随机SID／挑战；注册设置造数外层回滚，缓存只清理本测试键，不触碰bootstrap或开发安全设置。

4f退出证据：工作区与精确暂存快照`.tmp/batch4f-validation`的check／完整三库integration／全部入口build／tidy全部通过，日志`.tmp/batch4f-{check,integration,build,tidy}.log`及`.tmp/batch4f-snapshot-*.log`；870份暂存文件中的646份Go／模块与原blob逐字节一致，tidy无差异，本地Markdown链接有效。新增用户技能目录及skills-lock.json保留在工作区，已从本批暂存与最终验证快照移出，不混入重构提交。后台78项／用户59项、两端typecheck及后台构建通过，保留原Vite大chunk提示。重启admin后`.tmp/batch4f-live-session.json`验证新服务实际登录、原档案／权限、刷新和注销后401，未修改开发密码、MFA或资金；真实设置流程由独立测试库及注册路由覆盖。OP-01尚余账号角色、公告写入、dashboard及剩余ops持久命名，后续任务仍按表中状态实施。

批次4g（OP-01账号／角色）实施与定向证据：账号创建、更新、软删、解锁、重置及目标MFA迁入AccountService，角色字典及创建／修改／删除迁入RoleService；旧HTTP事务及角色授予校验删除，cmd注入所属服务。三个旧账号／角色store改为AccountRepo／RoleRepo对应repo，无旧别名；安全快照明确排除散列／密钥，身份不经浮点转换。所有管理写入先锁角色集合再锁当前操作者、角色和目标；实际权限及引用当前读。修正普通账号删除误受零客户管理员阻挡，状态变更提升版本防止旧会话复活，重置恢复locked但保留disabled，目标MFA必须匹配服务器原登记；原受授权登记材料回执保留而不进入审计。数据范围未发现已有写路由，不凭目标文档新增API。

4g定向验证：`.tmp/batch4g-management-{flow,concurrency,registered,restored}.log`通过真实独立MySQL／Redis及实际注册账号、角色、运营创建路由，覆盖原字段／状态／严格输入、实时撤权、旧RR权限与新引用、最后管理员三个移除路径、停用再启用、目标MFA原材料、敏感快照和审计503整体回滚。两真实连接按实际集合锁查询闸门固定撤权先提交、分配后当前读取，只拒绝旧权限且没有新账号。四项当前权限／引用／剩余管理员／原MFA密钥变异在指定断言失败并恢复，`.tmp/batch4g-mutant-*.log`保留；提交造数按自身身份清理零残留，注册造数外层回滚，不修改bootstrap、开发安全设置或资金。仓库新增Go技能规则本轮已加载；gopls／govulncheck及C编译器未发现可用命令，采用源码引用核对、编译／vet和真库闸门证据，不声称完成语言服务或race验收。完整退出验收继续，OP-01整体仍进行中。

4g退出证据：工作区默认检查、完整独立三库integration、全部入口构建与tidy通过；最终精确暂存快照`.tmp/batch4g-validation`的`.tmp/batch4g-snapshot-{check,integration,build,tidy}.log`全部通过，875份文件中的651份Go／模块与原blob逐字节一致，tidy无差异，文档本地链接有效。首次快照中既有发布并发测试失败但旧日志未展开error；已仅改善失败诊断，不改业务，原日志保留`.tmp/batch4g-snapshot-publication-original-failure.log`。隔离5次及完整工作区诊断回归、最终快照回归通过，未复现原底层原因，不宣称已定位修复；后续若重现以新诊断跟进。后台78项、用户59项及两端typecheck／后台构建通过，保留已有Vite大chunk提示。admin已重启，`.tmp/batch4g-live-management.json`核对实际原账号、角色和权限公开读取、无敏感列且会话注销；未更改开发安全设置或资金。只读技能审查未发现新的可达功能问题，补齐repo中文事务前提与错误契约；用户技能目录和锁文件仍未提交。OP-01继续公告、审计查询组合、dashboard及剩余持久命名，下一批不提前完成。

批次4h（OP-01运营查询／N-ops／INF-01）实施：公告发布和列表迁入AnnouncementService，审计筛选分页迁入AuditQueryService，看板日界和七格补零迁入DashboardQueryService；所属表访问均为repo，dashboard最后跨模块只读投影从infrastructure归ops，旧包无消费者／源码。删除泛化表名+values、未用投影及无消费者HTTP审计事务，不保留别名。保留公告target_ids的JSON null／字符串数组、可空end_at、published及审计目标JSON文本；所有发布和审计同事务，当前账号／权限在原集合串行点后核对。看板保留北京时间自然日、确认零与未计费区别、原跨表全球指标，不新增范围过滤或多查询一致快照承诺。

4h定向验证：`.tmp/batch4h-operations-{targeted,registered}.log`、`.tmp/batch4h-admission.log`及`.tmp/batch4h-mutants-restored.log`使用独立真实MySQL／Redis及实际注册路由，覆盖当日两端边界、七格补零、软删、原公告及审计空值、严格输入、审计回滚、取消、当前撤权及版本变化的403／401，任一看板查询失败只503而无部分数据。注册审计筛选暴露原LikePattern用!转义但SQL未声明的问题，已显式ESCAPE，含下划线账号原筛选回归通过。公告审计、未计费条件和转义声明三项变异均在指定断言失败并恢复，日志`.tmp/batch4h-mutant-*.log`；fixture仅自身账号和随机SID，业务造数外层回滚，不修改开发事实。初版站点fixture缺必填坐标已补齐，不改生产schema。数据库技能只读审查发现看板二次准入误映射503，已修正并以首次鉴权后实际撤权／提升版本的SQL闸门验证；其余未发现新可达问题，过时描述及字段中文说明已清理。用户技能及锁文件另行授权提交295f5c4，后续精确验证快照包含其已提交原blob，不混入本批业务改动。

4h退出证据：工作区与最终精确暂存快照`.tmp/batch4h-validation`的check、完整三库integration、全部入口build及tidy全部通过，日志`.tmp/batch4h-{check,integration,build,tidy}.log`与`.tmp/batch4h-snapshot-*.log`。快照包含用户已提交技能，1177份文件中的666份Go／模块与原blob逐字节一致，tidy无差异、本地Markdown链接有效。admin已重启，`.tmp/batch4h-live-operations.json`核对原公告、审计和看板七格趋势读取，会话注销；无公告发布、开发安全设置或资金修改。前端及公开字段未改，最近前端完整验收为4g的78／59项、两端类型检查及后台构建，原chunk提示保留。最终仅更新任务完成及本退出记录，下一批继续导出。

批次4i（E-01格式／持久命名子项）实施：CSV／XLSX编码及原格式回归归export.FormatWriter，HTTP只调用所属编码，不保留旧编码函数；ExportStore／export_task_store改ExportRepo／export_task_repo并更新全部消费者。FormatWriter按行及输出检查ctx，源取数使用WithContext，取消不能被误报编码成功；中文、空值、公式文本防护、XLSX流式及不支持PDF原行为保持。`.tmp/batch4i-{format,targeted}.log`验证原格式／严格PDF拒绝、输出首写中取消不续写和预取消零输出，真实独立SQL四资源源取数均响应取消。此子项尚未迁入任务事务、下载和清理，E-01不提前完成。技能只读核对确认原首次UUID竞争、失配到期字符串、库路径信任、范围／mask收窄、厂商设备过滤及文件／SQL推进缺口，继续下一服务子批修正；不新增schema或异步worker。

4i退出证据：工作区`.tmp/batch4i-{check,integration,build,tidy}.log`及最终精确暂存快照`.tmp/batch4i-validation`的对应四份`.tmp/batch4i-snapshot-*.log`全部通过；1180份文件中的669份Go／模块与原blob逐字节一致，tidy无差异、文档本地链接有效。无开发数据或凭据操作，无公开字段、依赖、路由和格式清单变更；前端最近验收仍为4g。任务状态维持进行中，下一服务批继续安全准入、恢复和清理。


批次4j（E-01／N-export服务）实施：原导出五路由成为薄适配，所属ExportService接管资源、完整UUID受理／原审计、同步生成／恢复、下载和清理，cmd注入同一配置，旧无消费者写入入口及HTTP事务审计包装删除。只写export_task与audit_log，业务投影明确选择注册列；设备源按站点／厂商交集，订单／站点继承原站点语义，账单仅全局日期窗口。新产物在可信根v1保存私有身份／范围／mask／SHA256凭据，缺失不降级旧文件；原根目录历史文件只在当前全局且无需脱敏时兼容。file_path不作为任意IO路径，PDF继续禁止下载，不变schema、公开DTO、路由或清单。

4j指定验收：真实独立两池同UUID只一任务／一审计，旧RR已建立快照后能当前读取任务及期限，范围／mask收窄拒绝旧宽文件；等待锁和生成发布跨截止均拒绝。超限、取消、目录IO失败、完成SQL故障清理并持久failed，失败持久也故障时返回错误且pending能原键恢复；历史无凭据文件兼容、文件已不存在和重复清理、外部路径保留及私有摘要／身份失配均核对。实际注册五路由使用真实SQL／Redis，CSV／XLSX创建及下载、原回执重放、严格输入／PDF拒绝、503不泄漏内部原因、撤权403和版本401通过。日志为`.tmp/batch4j-{owned-all,ownership-final,registered,managed-tx,cancel-lock}.log`。取消闸门在真实文件发布后、SQL完成回调内固定竞争，锁直到同步回调退出；不宣称已中断磁盘中的写。恢复普通期限读、移除范围当前读和恢复请求取消自动事务的三项变异均稳定失败并恢复，日志`.tmp/batch4j-{expiry-current,scope-current,cancel-lock}-mutation.log`。完整工作区、精确暂存快照及开发服务退出证据尚待补齐，E-01仍不提前完成。


4j工作区退出：`.tmp/batch4j-{check,integration,build,tidy}.log`全部通过，完整独立三库integration包含上述服务和实际注册回归，tidy无差异。admin已重启，`.tmp/batch4j-live-export.json`及`.tmp/batch4j-live.log`验证CSV／XLSX各一份原站点导出、原键重放、详情与实际下载、MIME／中文／ZIP形状，服务端路径不出响应，会话注销。两份开发导出及其审计保留至原24小时清理，无源业务表、资金、开发安全设置或schema改动；备份保留。精确暂存快照退出仍待核验，任务状态待验收；前端／公开形状未改，最近前端完整验收仍为4g。


4j最终退出：精确暂存快照`.tmp/batch4j-validation`的`.tmp/batch4j-snapshot-{check,integration,build,tidy}.log`全部通过；1191份暂存文件中的680份Go／模块与原blob逐字节一致，本地Markdown链接有效、tidy无差异。真库缺配置须明确失败，`.tmp/batch4j-missing-config.log`实际核对，无静默跳过；最终快照包含该修正。只读复核确认当前期限读取及生成事务生命周期，无新增具体阻断问题；历史兼容与资金只读边界已说明。最终仅补本退出记录及任务完成状态，E-01／N-export完成；按17组进度15组完成，继续H-01／X-01／G-01／WK-01及DEP-01，不代表整个目标完成。


批次5a（H-01鉴权及后台适配目录）实施：后台业务族移至internal/http/admin，认证和MFA适配归internal/http/auth，账号／角色适配归admin/account；cmd及测试直接使用新包，无旧路径别名。原admin/contracts删除，分页直接使用persist，严格JSON解码归platform/httpapi.DecodeResource，后台公共Actor／错误／分页／归一化独立于旧HTTP根包。无消费者的范围解码、校验包装及设备摘要转发删除，保留原有效业务服务。go:embed与源文件一起移动，OpenAPI字节、公开路由、认证、输入及回执不变。迁移函数与字段中文注释已核对。

5a范围边界：user及central仍在internal/interface/http，尚余10个处理器直接调用仓储／查询（包括账单已读写入），后台16处范围读取仍经StationScope的SQL依赖。没有HTTP业务事务不等于薄适配完成；下一子批先把这些入口归所属服务并注入范围读取，再按真实路由拆user／central和跨族测试。H-01及所在组合组仍进行中，17组维持15组完成。全仓、精确暂存快照及开发服务证据待补，不提前标记完成。


5a工作区退出证据：`.tmp/batch5a-{check,integration,build,tidy}.log`全部通过，真实集成使用已有独立三库／Redis，公开字段与前端代码未改，最近完整前端验收仍为4g。重启admin及user后，`.tmp/batch5a-live-http.json`核对admin OpenAPI与源文件逐字节相同、账号／角色／权限与运营只读查询，以及C端档案、钱包、空进行中订单、注销401；两类会话均注销。首次开发登录创建development:tester-1测试用户与零余额钱包，SQL创建时刻已只读核对，未充值／扣款，不把此记录写成开发业务数据完全未变。

联调同时复现原UserAccountAPI.userID在SessionAuthenticator已写错误后再次写401，使失效钱包请求包含两个JSON信封；HEAD原代码具有同样双写，5a只迁移保持此行为，下一独立行为批次补注册路由回归并修正其它重复写入调用。用户档案注销后401是单信封。全仓通过不能掩盖这一已知缺口，H-01不提前关闭；精确暂存版本的最终证据待补。


5a最终退出：`.tmp/batch5a-validation`精确暂存版本的`.tmp/batch5a-snapshot-{check,integration,build,tidy}.log`全部通过；1190份文件中的679份Go／模块与原blob逐字节核对，本地Markdown链接有效，tidy无差异。最终补充8份迁移文件的范围、失败映射及所属服务中文注释，经Go词法token（包含go编译指令）与已测试版本完全一致，证据`.tmp/batch5a-comments-only.log`；按8.1纯注释不重复业务测试。补注释及本退出记录纳入最终索引后再次核对原blob、工作区Go／模块与文档链接。只读复核未发现本批迁移改变接口、权限、错误或事务的具体阻断问题；原双写错误响应已明确待下一行为批次修正。H-01及组合组仍进行中，17组维持15组完成，继续剩余HTTP查询／范围及user／central、X／G／WK和DEP评估。


批次5b（H-01用户认证单次响应）行为修复：SessionAuthenticator已负责错误信封，UserAccountAPI.userID、UserQueryAPI.queryUser／ongoing／history、BillHTTP三个入口及CouponAPI失败后仅返回，不再追加401。原零主键已由identity.Authenticator拒绝；保留凭据／会话401、冻结403、认证存储或取消503及原业务码、消息、请求号，不改变成功业务查询或账单写入。此批与5a结构迁移分开提交。

5b定向证据：user_auth_failure_test.go通过真实Register挂载29个受保护端点，窄身份fixture覆盖缺头、坏格式、坏签名、零主键、过期、冻结、会话／用户读取故障和取消九类；261个指定失败请求只输出一个JSON信封，业务依赖不装配也不继续执行。修复前23个端点共207项单信封断言失败，`.tmp/batch5b-auth-before.log`；修复后`.tmp/batch5b-auth-after.log`通过。这是HTTP适配及错误契约fixture，不作为JWT密码学或SQL／Redis故障实测。重启user后`.tmp/batch5b-live-auth.json`覆盖实际29路由共87次缺凭据、注销真实会话、无效签名，全部单JSON401；既有tester-1读取零余额／空进行中，注销完成，未新建用户、充值或扣款。

5b工作区四项`.tmp/batch5b-{check,integration,build,tidy}.log`通过，三库集成仍在独立测试栈。前端沿package.json命令直接调用已安装Node／TypeScript／Vite，因为本机npm脚本误指向不存在的全局npm-cli.js；不安装或修改环境。后台／用户测试、两端类型检查及后台构建记录`.tmp/batch5b-{admin-test,admin-typecheck,admin-build,user-typecheck,user-test}.log`，最终结果待核对；精确暂存及只读复核待补。H-01仍需10处仓储／查询入口、16处范围读取及user／central目录，组合组不提前完成。


5b最终退出：精确暂存快照`.tmp/batch5b-validation`的`.tmp/batch5b-snapshot-{check,integration,build,tidy}.log`全部通过；1191份文件中的680份Go／模块与原blob及工作区一致，tidy无差异、本地Markdown链接有效。只读复核确认所有共享鉴权消费者失败立即返回、29个方法／路径真实注册、单信封断言可识别双写，未发现新增阻断问题；独立鉴权的charge/stop未纳入新fixture，其原分支均已有单写后返回。后台78／用户59项、两端类型检查及后台构建通过，保留已有Vite大chunk提示。最终仅补此退出记录，H-01仍进行中，继续所属查询及范围能力、user／central目录、X／G／WK和DEP评估，17组仍完成15组。

批次5c（H-01查询／范围能力）实施：10个HTTP查询／账单入口转接所属业务服务，后台16处范围通过ScopeReader调用ops.ScopeQueryService，cmd只注入同一受管理连接；HTTP移除DB／UserDB和Repo依赖。UserBillService保留按用户归属查询后保存已读凭据的原顺序、分页／可空值及查询404／保存503；导入执行前原范围重读保持。无新增SQL、事务、锁、公开字段或权限解释。

5c定向及工作区证据：实际Register的用户账单三路由在独立真实MySQL验证本人／他人归属、原金额／null、列表、同账单已读重放与两个阶段的错误恢复，`.tmp/batch5c-bill-registered.log`通过。身份为窄能力fixture，指定GORM查询／创建回调故障仅验证原阶段错误映射，不宣称实际数据库故障或并发幂等验收。工作区`.tmp/batch5c-{check,integration,build,tidy}.log`全部通过；完整集成仅使用独立三库3307／Redis6381。迁移辅助工具初版按import路径末段误删版本化包名的有效导入，已恢复全部24份无关源码及7份实际迁移文件的Redis导入；最终检查均在修复后执行，不将初版结果计为通过。

5c开发及复核：admin／user加载本批源码后，`.tmp/batch5c-live-queries.json`验证7条后台及3条用户查询，复用既有tester-1零余额用户，两类会话均注销；没有新增用户或资金变动，查询没有写入来源业务数据。登录沿用原SQL登录时间及会话记录更新，不能把整个验收描述成完全没有持久写入；5d核对原UserRepo.Login后补明此边界。只读复核确认原查询、归属及范围语义不变，未发现新增阻断。无前端或公开DTO改动，最近前端完整验收为5b的后台78／用户59项、两端类型检查及后台构建。精确暂存退出检查待补；H-01组合组仍进行中，17组维持15组完成。

5c最终退出：精确暂存快照`.tmp/batch5c-validation`的`.tmp/batch5c-snapshot-{check,integration,build,tidy}.log`全部通过；1196份文件中的685份Go／模块与原blob及工作区一致，tidy无差异，本地Markdown链接有效。最终仅补本退出记录及状态说明；H-01组合组仍进行中，下一批按真实路由拆user／central，再完成X／G／WK及DEP评估。17组仍完成15组，不代表整个目标完成。

批次5d（H-01内部适配目录）实施：按cmd实际注册将central授权、回执、派发、内部卡、模拟通知及告警同步归internal/http/central；CardAPI拆开用户与服务身份依赖，原规则不复制。告警服务令牌适配归http/auth，central不依赖admin或旧根包。迁移前新增cmd/central真实装配回归，三种支付模式、空／非空配置及缺失／错误凭据覆盖原11／12路由，共136次拒绝；单JSON401、业务码、消息及请求／追踪号核对，所有业务依赖为空，`.tmp/batch5d-assembly-before.log`通过。这仅证明注册及拒绝先于业务调用，不替代资金或真实存储测试。迁移、全仓及开发容器网络验收继续，H-01组合组不提前完成。

5d工作区证据：`.tmp/batch5d-{targeted,check,integration,build,tidy}.log`全部通过，集成只用独立三库／Redis。迁移生产字段、函数和接口方法及四份随迁测试均补中文输入、结果、错误及副作用说明；AST注释清单无central缺项。`.tmp/batch5d-move-proof.log`将33个原生产函数、11个原测试／fixture函数及cmd装配共45个签名和方法体按语法树规范打印后核对token；仅规范化已迁移类型和服务令牌中间件的包选择器，匿名结构注释产生的换行不算逻辑变化。公开JSON、SQL、用例、schema、配置与前端未变，最近前端验收仍为5b。

5d开发验收：重启central／user后，在现有Compose容器网络核对central原12路由24次坏凭据及4条合法只读查询，`.tmp/batch5d-live-central-{before,after}.json`中空待启动、无效卡零余额、原缺失订单409及单信封401一致；没有映射8082到宿主机。`.tmp/batch5d-live-user-admin.json`及日志保留7条后台／3条用户原查询、既有tester-1零余额及两类会话注销。未新增用户或资金写入；登录按原规则更新SQL登录元数据和Redis会话，已补明5c同一验收边界。没有触发合法的central写入路由，不把该联调当作真实支付或实机验收。精确暂存退出检查继续，用户分包与X／G／WK及DEP仍待完成。

5d最终退出：精确暂存快照`.tmp/batch5d-validation`的`.tmp/batch5d-snapshot-{check,integration,build,tidy}.log`全部通过；1199份文件中的688份Go／模块与原blob及工作区一致，tidy无差异、本地Markdown链接有效。最终仅补本退出记录、状态及README实际入口说明；H-01组合组仍进行中，下一批按用户接口族拆除UserAccountAPI全量容器及旧根包。17组仍完成15组，继续X／G／WK和DEP评估，不代表整个目标完成。


批次5e（H-01用户目录）实施：用户接口按card、charging、device、identity、marketing、ops、order、payment、settlement、wallet十个实际业务族归internal/http/user，cmd直接装配各所属服务。删除全量UserAccountAPI与旧internal/interface目录，不保留类型别名或转发包。跨族注册回归集中在只有测试文件的internal/http/integration；纯卡文档规则测试回归card。父包只保留错误映射和原ASCII空格状态筛选，坐标与金额校验归其消费者；分页及认证助手直接内联原能力，孤立fixture与无消费者mustJSON删除。user.openapi.json原字节随迁，不增加文档服务地址。

5e安全网与语法核对：迁移前捕获cmd/user实际newRouter在disabled／simulation／wechat_direct三模式的完整42／43／43条路由，固化testdata/routes-before-user-split.json，记录工具已删除；新增装配测试逐项核对。`.tmp/batch5e-move-proof.log`覆盖72个原生产函数，65个函数语法匹配及7个明确路由拆分／助手内联／删除，并核对全部41个原方法／路径到处理器的绑定；只规范化移动类型、原分页／鉴权助手及字段命名，不忽略请求或响应值。`.tmp/batch5e-live-user-before.json`记录开发栈原3条公开和13条所属查询；测试用户为既有tester-1、零余额，登录元数据及会话按原规则更新，注销完成。全仓、精确暂存和开发迁移后退出核验继续，不提前关闭组合组。

5e工作区及开发退出：`.tmp/batch5e-{check,integration,build,tidy}.log`全部通过，完整集成只使用既有独立三库3307／Redis6381；迁移后的跨族实际注册回归和29×9单信封认证拒绝均通过。`.tmp/batch5e-test-move-proof.log`核对原55个测试／fixture及cmd装配函数，除明确的业务族消费者改名和装配拆分外，断言、造数及清理语法保持；函数／字段中文注释清单无缺项。重启user后首次连接在进程就绪前失败，读取启动日志及ready后重跑通过，未把首次失败记为验收成功。`.tmp/batch5e-live-user-after.json`与迁移前全部3条公开、13条所属查询逐项一致，既有用户仍零余额、注销完成；未新增用户或资金写入，登录元数据仍按原规则更新。OpenAPI原内容保持，公开字段、前端、schema及依赖未改，最近完整前端验收仍为5b。精确暂存退出核验待补，H-01所在组合组继续X／G／WK，不提前标记整个目标完成。

5e最终退出：精确暂存快照`.tmp/batch5e-validation`的`.tmp/batch5e-snapshot-{check,integration,build,tidy}.log`全部通过；1207份暂存文件中的695份Go／模块与原blob及工作区一致，tidy无差异、本地Markdown链接有效。user OpenAPI原HEAD与新暂存blob相同，跨族测试的编译标签与业务断言保留。最终只读复核未发现本批具体阻断，旧HTTP根包／别名／全量容器已移除；H-01的目录和薄适配子项完成。最终仅补本退出记录及状态，不重复无源码变化的业务测试。17组维持15组完成约88%，继续X-01／G-01／WK-01／N-gateway／N-worker及DEP-01评估，不代表整个目标完成。用户授权的46个Go技能及锁文件仍在独立提交295f5c4，并包含在本快照中。

批次5f（X-01资金任务契约）实施：先建立central已注册计费／退款／停机触发的Request／Reply、具名客户端及实际注册回归，worker消费者继续下一独立批次。HTTP只以共享常量和DTO替换原路径与回执字面量，原业务服务、令牌先行、状态／消息、JSON字段、退款20秒／停机25秒context保持。客户端保留无正文或null的原请求形状、受管理Transport及30秒上限，禁止重定向或重试。仅完整成功或实503／5003未完成回执能返回进度；缺失／null／负数／溢出、矛盾状态、非503伪进度、超限或多个JSON不作为合法完成。退款成功必须明确enabled，失败仍只需原processed，不给原503增加字段。指定验证及完整退出证据待补，组合组不提前关闭。

5f指定及工作区证据：`.tmp/batch5f-targeted.log`及`.tmp/batch5f-{check,integration,build,tidy}.log`全部通过，tidy无差异。回环客户端覆盖原wire、合法部分进度、HTTP／业务错误分类、enabled缺失性、正文截断／4096上限及多个JSON、30秒期限和共享Transport不变、重定向／取消／短deadline、送达后断连不补发或换身份；期限通过实际请求context及更短deadline观测，不等待真实30秒超时，不作为资金执行证据。实际Register默认回归核对停机成功／未完成、退款禁用和三路由服务令牌先行。`.tmp/batch5f-billing-registered.log`在独立真实MySQL验证BillingService原队列SQL、已确认一笔／失败一笔的completed=1、原attempts及延后时间、到期前重扫0、推进原到期后同身份恢复1及完成重放0。资金Apply为窄边界fixture，不扣款或替代既有真实资金幂等验收；造数及推进外层回滚。9份新增／改动Go源码的函数、字段及接口中文注释清单无缺项。

5f开发对照：只读确认既有开发充电／计费／待投递费用／到期退款队列为零，钱包合计1999／冻结0、账单1；重启central后，容器网络原三个端点六次拒绝及三次合法空任务回执与迁移前逐项一致，读回队列／钱包／账单数量相同。证据为`.tmp/batch5f-live-finance-{before,after}.json`、`.tmp/batch5f-dev-finance-{before,after}.log`及`.tmp/batch5f-live-comparison.log`。没有映射8082、创建订单、触发模拟支付或使用真实渠道，空队列协议验证不当作完整资金流程或设备验收。公开JSON、前端、schema及依赖未改，最近完整前端验收仍为5b；精确暂存退出继续，worker旧消费者暂留至下一独立迁移批次。

5f暂存退出中的真实失败及定位：初次完整快照integration在导出fixture清理发生1213，原失败保留`.tmp/batch5f-snapshot-integration-original-failure.log`。独立测试MySQL的`.tmp/batch5f-innodb-status.log`记录三方循环：导出actor索引范围删除等待主键，设备导入按无索引request_id删除扫描主键并等待新审计，运营插入等待导出所持actor索引间隙。业务客户端及注册计费测试通过，失败不能作为整仓验收通过。仅修测试清理：先非锁读取自身审计的id／created_month，再按完整分区主键确定顺序删除，保留身份筛选和零残留检查；不改生产SQL、schema、业务断言或全仓并行方式。导出回归在明确RR清理事务未提交时另一连接仍可插入相邻actor；设备回归在明确RR外部事务锁住无关请求审计时清理自身请求仍完成、自身0且无关行1。原actor及request_id范围DELETE两项变异分别在对应deadline断言失败并已恢复，`.tmp/batch5f-{export,device}-cleanup-mutant.log`保留；恢复后两完整包通过`.tmp/batch5f-cleanup-restored.log`。设备4处清理使用同一测试助手，原业务及零残留断言保留，15份本批Go源码中文函数／字段／接口注释清单无缺项；最终精确暂存退出继续。

5f最终快照验收：`.tmp/batch5f-validation`的`.tmp/batch5f-snapshot-{check,integration,build,tidy}.log`均通过；完整integration保留默认包并行及独立三库／Redis，未用串行或重试掩盖原清理死锁。1215份暂存文件中的703份Go／模块与原blob及工作区一致，tidy无差异、Markdown本地链接有效。修复后工作区默认检查与两完整包真库回归通过；原失败及两项变异日志保留。本批共享路径、Request／Reply与具名客户端完成，worker消费者窄接口和旧通用调用删除继续下一独立批次；前端及公开字段、schema、依赖不变，最近完整前端验收仍为5b。最终只补本退出记录，不提前关闭X／G／WK组合组或DEP评估，17组仍完成15组约88%。

批次5h（发布站点隔离修复，先于5g提交）：5g消费者与实际注册资金循环指定回归通过，但工作区完整integration复现先前4g未定位的同请求发布失败，本次明确1213。原失败`.tmp/batch5g-integration-original-pricing-failure.log`及独立测试MySQL `.tmp/batch5g-innodb-status.log`保留：PC首次设备发布INSERT与HP后台实际注册继承UPDATE，在pricing_rule的idx_station_status间隙互等。HP该次继承有设备规则，不能把捕获的UPDATE称为未命中；外层RR事务此前首次发布的范围更新已持索引间隙，PC同样先更新空目标后插入。生产两处停用都改为站点锁下非锁枚举自身active主键，再按主键逐行停用、空集合无UPDATE；保留原scope／active条件、版本、凭据、规则及审计完整事务，不变更隔离或增加重试。正常服务新事务视图在站点锁后建立；不宣称任意旧外层RR视图被刷新。

5h指定证据：`.tmp/batch5h-publication-isolation.log`四种真实RR双写连接闸门场景通过：发布／继承及空／已有目标，第一停用阶段后仍持原事务，第二站点必须先提交原version1规则。旧范围UPDATE变异四场景均在第二站点期限断言失败，`.tmp/batch5h-publication-gap-mutant.log`保留，恢复后pricing及实际后台注册路由两完整包通过`.tmp/batch5h-publication-restored.log`。造数提交属于自身站点／设备／请求，退出放行并等待第一事务，再完整主键清理并核对零残留；不改既有并发、公开JSON或审计断言。worker的5g改动继续保留未暂存，5h单独暂存pricing两文件及文档，最终全仓快照只使用已提交旧worker实现验证本修复；消费者完成须等下一批自己的完整退出验收，不提前关闭组合组。

5h最终快照验收：`.tmp/batch5h-validation`的`.tmp/batch5h-snapshot-{check,integration,build,tidy}.log`全部通过，完整integration保留默认包并行及独立三库／Redis；tidy无差异、快照本地Markdown链接有效。1216份暂存文件中的704份Go／模块与原blob一致；pricing改动与工作区一致，9份未暂存worker批次改动明确排除并以原暂存blob验证，3份未跟踪5g新增测试也未纳入本修复快照。两份pricing源码中文函数／字段注释清单无缺项。最终仅补本记录，公开字段、前端、schema及依赖不变，最近完整前端验收仍为5b；开发资金循环联调属于下一5g批次，不将其计为方案发布或真支付验收。17组仍完成15组约88%，继续消费者、G／WK和DEP评估。

批次5g（X-01资金任务消费者，5h修复后继续）：自动停机、计费、退款派发注入各自消费方窄接口，cmd用原受管理JobHTTP独立构造具名central客户端，原InternalHTTP查询／同步及资源退出保持。停机保留合法部分计数及原错误，两个派发保持Run(error)，未配置明确失败，不重试或换身份。已无消费者的worker/charge/sync_client.go、internaljob/http.go及platform/serviceclient旧Gin中间件删除，原worker运维API和实际HTTP鉴权适配保留；没有真实后台worker调用不新增虚构contracts/worker。原任务顺序、间隔和游标未修改。

5g指定证据：`.tmp/batch5g-targeted.log`原四默认包、`.tmp/batch5g-assembly.log`实际cmd装配均通过，三资金任务确实用JobHTTP Transport、query pool无调用且原Client配置不变。消费者回环测试保留原无正文／null形状、503部分数量与HTTP／业务错误、取消context及未配置；旧退款只含code0的空数据回执现在明确拒绝。`.tmp/batch5g-finance-registered.log`实际Register三端点、真实BillingService和独立MySQL原fee_delivery验证停机及计费失败仍到退款、停机合法部分2保留、原任务到期前不重投、推进同一到期身份后恢复及完成重放不再Apply。取消在实际停机边界触发，两个后续消费者入口计数0，不只依靠没有HTTP请求作证。停机和资金Apply是窄fixture，退款走真实禁用分支，不将此编排证据当作扣款或实机验收；原造数及推进外层回滚。原全仓pricing失败先独立提交77fdca4修复，消费者仍需自己最终完整验收。

5g开发循环证据：重启既有worker加载源码，只通过容器网络GET health／metrics观察新进程uptime约2.65→14.66秒内的自动轮次，central三个任务200指标各增1、非200没有增量，未手动POST。`.tmp/batch5g-worker-finance-{before,after}.json`保留；只读`.tmp/batch5g-dev-finance-{before,after}.log`核对充电／计费／费用投递／到期退款均0，钱包合计1999／冻结0、账单1相同。首次只读查询误用不存在billing_job表后已按真实repo改为charge_billing_job重跑；只将完整成功读取作为证据。没有映射8082、新建用户／订单／会话或触发模拟支付，空队列指标验证不代表资金或设备执行。公开字段、前端、schema及依赖不变，最近完整前端验收仍为5b；最终精确暂存验收继续，G／WK及DEP不提前完成。

5g最终快照验收：`.tmp/batch5g-validation`的`.tmp/batch5g-snapshot-{check,integration,build,tidy}.log`全部通过，完整integration使用独立三库／Redis并保留默认包并行，包含5h修复及两类新增资金装配／注册循环回归。1217份暂存文件中的705份Go／模块与原blob及工作区一致，tidy无差异、Markdown本地链接有效；10份本批Go源码中文函数／字段／接口注释清单无缺项。旧通用HTTP符号全仓零消费者；worker生产源码仅HTTP准入适配仍持服务令牌，三个业务派发不持有通用客户端或URL。最终仅补本记录及表中子项状态，不关闭G／WK组合组或DEP，17组仍完成15组约88%；继续gateway／worker持久化及运行时整理。

批次5i（N-worker／WK-01 repo 子项）：worker/store 两个生产文件及原 Outbox 并发测试迁入 worker/repo，三个调用方更新路径和别名；完整日志创建及任务完成列归 repo，Outbox 原表选择移入 repo。删除未消费 ReplayCursor、ListExecutionLogs，不保留旧包；不增加订单／资金表访问，不改变调度器、发布事务、来源／流／幂等身份、任务顺序或资源生命周期。AGENTS 目录图同步。N-worker 命名子项完成，WK／G 组合组仍需运行时与 gateway 收尾。

5i 定向证据：.tmp/batch5i-targeted.log 默认 worker repo／outbox／schedule 与 cmd 装配通过；.tmp/batch5i-scheduler-real.log 使用独立 worker 真库与实际注册 trigger 路由验证原成功回执、日志 ID 回填、NULL 错误、UTC 下轮及失败清零。有效租约期间另一次 force 不能进入处理器；取消后独立收尾持久 failed，原令牌失配不能覆盖 fixture 模拟接管后的新租约；第五次失败停用，force 执行后仍停用。fixture 只提交随机任务编码及其日志，所有触发先退出，再按自身完整主键清理并核对零残留；不触发已有业务处理器、不读写资金或开发数据。完整退出验收继续，17 组仍完成 15 组约 88%。

5i 退出证据：.tmp/batch5i-{check,integration,build,tidy}.log 全部通过，真实完整三库 integration 保留默认包并行，迁移后的 SKIP LOCKED／批次顺序／退避原回归与新增调度测试通过。最终暂存快照 .tmp/batch5i-validation 共1218份文件，其中706份 Go／模块与已通过上述检查的工作区源码及原 Git blob 一致，本地 Markdown 链接有效、tidy 无差异；本轮不重复执行同一份已核对源码的全量测试。七份变动 Go 文件的中文函数／字段／接口说明清单无缺项。重启既有 worker 后，.tmp/batch5i-worker-live.json 仅经健康／指标 GET 观察 uptime 9.04→21.05秒，原三个资金任务成功指标各增2、原失败指标未增加，手动任务POST为0；该空队列联调不代表资金或设备执行。没有开发清库、schema、公开形状、依赖或前端改动；前端最近完整验收仍为5b。最终只补退出记录及任务子项状态，G／WK 组合与 DEP 不提前完成。

批次5j（WK-01 运维准入独立修复）验收基线：原 OpsAPI 服务令牌失败时只 return、未 Abort，Gin 仍进入流统计。实际 cmd newRouter 的五种未配置／缺失／错误／空格失配身份都稳定产生一次 Redis 命令，并在原401后追加503 JSON；.tmp/batch5j-cmd-denial-initial.log 保留，ProcessHook 只记录并返回固定错误，没有外部网络或数据写入。API.Register 的旧实现定向证据为 .tmp/batch5j-ops-before.log。修复及真实 Redis 验收单列本行为批次，不与已提交5i目录迁移混合；组合组状态仍进行中。

5j 实施与定向验收：失败中间件使用既有 VerifyServiceToken 并 Abort；空配置与精确令牌语义、成功200、原 XLen 长度、redis.Nil 跳过和其他错误／取消503保持。修复后的 .tmp/batch5j-targeted.log、.tmp/batch5j-cmd-denial-restored.log 都通过，实际 cmd 原五种拒绝均零 Redis 命令、仅一个401 JSON。独立 Redis .tmp/batch5j-redis-registered.log 验证实际注册端点读取私有流两条、缺失流0、真实类型错误503且无内部细节泄漏；随机键 DEL 后 EXISTS=0，未写开发或默认业务流。四份变动 Go 文件中文说明清单无缺项，未改变 SQL、租约、调度、事件身份、默认流清单或资源生命周期。

5j 退出验收：全仓默认 .tmp/batch5j-check.log 与受影响 worker 构建 .tmp/batch5j-build.log 通过；相关真实 Redis、API 与 cmd 注册回归已如上验收，SQL／任务状态及依赖没有变化，本批不重复无关资金流程或前端测试。最终暂存快照 .tmp/batch5j-validation 的1221份文件中709份 Go／模块与已验证工作区及原 blob 一致，.tmp/batch5j-snapshot-source.log 保留，本地 Markdown 链接有效。重启既有开发 worker 后 .tmp/batch5j-worker-denial-live.json 只读核对健康，以及缺失／错误令牌均唯一401、code1001、data null；没有有效身份流写入、开发清库或资金修改。G／WK 组合仍进行中，17组完成15组约88%；下一批继续 gateway 仓储与控制层完整操作，之后部署评估及最终工作报告。
