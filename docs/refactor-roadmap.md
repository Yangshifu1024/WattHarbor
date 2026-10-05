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

来源：[HTTP 计费服务](../internal/interface/http/billing_service.go)、[计费入账](../internal/settlement/billing_job_repo.go)、[费用计算](../internal/settlement/fee_calculation_repo.go)、[账单](../internal/settlement/bill_repo.go)，以及 HTTP finance、device/meter_reviews、settlement_bill、user_invoice 的业务编排。

目标：billing.go 的 BillingService.Run；billing_job_repo.go、fee_calculation_repo.go、bill_repo.go；refund.go、invoice.go、meter_review.go 与各自 repo／schema。Calculate 等有业务行为的组件按职责命名，不因为原名 Store 就统一改成万能 Repo。

- [x] S-01：迁入 Run、deliverPending、人工计费复核入队和 defer；BillIssuer 等能力由 settlement 定义。HTTP 只绑定、鉴权、调用和映射结果。
- [x] S-01：明确 Calculate 持久结果、Apply 入账、MarkDelivered、账单 Issue 的不同完成事实；迁移先保留原算法与快照，再单列补出账恢复任务。
- [x] S-01：新增有界的“满足现有出账条件但没有账单”恢复查询，覆盖 MarkDelivered 后 Issue 失败及 backlog 投递路径；使用原结算回执与既有账单唯一键，不重算、重复扣款或创建影子账单来源。
- [x] S-02：迁入人工结算、退款申请／审核／执行、发票申请／审核、计量分段提案／复核；将操作人、范围、实时复核权限与审计放进用例事务。
- [x] S-02：ChargeFeeRecord／RefundRecord 模型迁到 settlement，必须先完成 F-01 和 PAY-02／W-01 的反向依赖消除；payment 不通过类型别名引用 settlement。
- [x] N-settlement：退款、账单、发票和计费 repo 统一名称；复用 RefundBalanceInTx，不新增另一套余额退还算法。

验收：相同来源费用重放一致，异内容冲突；并发退款占用不超实付；缺少计量／快照进入复核；补出账失败可下一轮恢复且不再入账；不同人员复核、首签权限变化、金额／链接／分段快照变化均按原约束处理；渠道未知结果仍留预占和原退款编号。

### 5.3 payment：F-01／PAY-01／PAY-02／N-payment

来源：[扫码支付入口](../internal/interface/http/payment_payment_http.go)、[支付意图](../internal/payment/payment_intent_repo.go)、[支付回调](../internal/payment/payment_callback_repo.go)、[设备运营读取](../internal/device/operation_repo.go)、[支付模型](../internal/payment/money_schema.go)。

目标：CheckoutService.Offers／Start、CallbackService.Apply；payment_intent_repo.go、payment_callback_repo.go、payment_order_repo.go 等支付数据能力。保留渠道验签／解密边界。

- [x] F-01：先定义充值请求读取／到账、迟到退款与钱包退款记录的事务内输入输出；补上 payment、wallet、settlement 的 cmd 注入，证明无反向 import。
- [x] PAY-01：把 Replay → 端口准入 → 有效方案／套餐核对 → Reserve → 预支付参数复用／申请／保存迁入 checkout；报价发现的匿名访问与提交支付的登录要求不变。
- [x] PAY-01：设备状态查询归 device，扫码运行端口事实由具名 gateway 客户端提供；用例返回业务错误，不依赖 ScanAPI 或 HTTP 状态码。
- [x] PAY-02：回调继续拥有 payment／intent／幂等摘要事务；同 tx 调用 wallet 到账、marketing 权益、settlement 退款记录、order 建单／快照及 ops Outbox 能力。
- [x] PAY-02：拆出 settleWalletRecharge 中的 wallet 表访问和 queueLatePaymentRefund 的 refund_record 写入；渠道金额／身份核验与支付状态推进仍归 payment。
- [x] N-payment：迁移文件、类型和消费者；money_schema.go 只留下 payment 所属模型，删除迁出的退款／结算模型旧定义。

验收：同 request_id 同内容返回原支付单与参数，异内容拒绝；过期意图付款退款且不正常建单；通知商户／应用／付款人／关联／金额／交易号不符拒绝；重复通知一次到账和发券；预支付响应未知可用原单恢复，客户端成功不推进设备状态。

### 5.4 wallet：W-01／N-wallet

来源：[钱包 HTTP](../internal/interface/http/user_wallet.go)、[账户能力](../internal/wallet/wallet_account_repo.go)、[退款预占](../internal/wallet/wallet_reservation_repo.go)、[后台风控入口](../internal/interface/http/admin/finance/wallet_risks.go)。

目标：RechargeService.Create／CreditInTx、RefundService.Request、RiskService.Review／Release；wallet_account_repo.go、wallet_ledger_repo.go、wallet_refund_repo.go、wallet_reservation_repo.go 和 risk repo。

- [x] W-01：迁入充值申请的用户／钱包检查、请求重放、支付创建和提交后预支付复用；payment 能力通过接口注入。
- [x] W-01：迁入退款申请的归属、余额减冻结额度、近期频次、冻结联动、请求内容核对与响应凭据；不能把风险批准视为渠道已到账。
- [x] W-01：ReserveRefund 保留钱包冻结、原充值来源分摊与 wallet_refund_part；其中 refund_record 创建调用注入的 settlement 能力，同事务提交。
- [x] W-01：定义充值到账的事务内能力，冻结钱包仍可接收已确认资金；账户版本与流水必须一致，首充奖励仍在支付回调原事务内执行。
- [x] W-01：后台风险审核／解除用例保留范围、复核、预占释放与审计，不能由 handler 任意改状态。
- [x] N-wallet：repo 与持久化模型的角色分开；钱包不 import payment／settlement，实现通过 cmd 注入。

验收：并发预占不超可用余额；申请／审批／渠道重试分别幂等；重复充值只一条资金动作；退款未知结果不释放 frozen；驳回／失败／成功按既有规则只释放一次；同请求不同金额／原因／用户冲突，响应丢失后重放返回原结果。

### 5.5 device：D-01／D-02／N-device

来源：[导入编排](../internal/interface/http/admin/device/device_imports.go)、[导入任务](../internal/device/import_repo.go)、[用户故障](../internal/interface/http/user_fault.go)、[通用 case](../internal/device/fault_repo.go)、[告警存储](../internal/device/alert_sync_store.go)。

目标：ImportService.Create／Retry、OperationReader、StationService、FaultService.Report／Dispatch／Resolve／History、AlertService.Sync／Acknowledge；import_repo.go、operation_repo.go、fault_repo.go 等。

- [x] D-01：先提供设备操作状态、站点／端口归属与能力事实，支撑 payment、card、pricing；接管 payment_device_store 中业务元数据读取。
- [x] D-01：迁入导入创建、任务锁、范围核对、gateway 预置、metadata 更新、失败／完成及审计；保留有界请求和失败状态可提交的原行为。
- [x] D-01：站点／设备新增、更新、启停、导入身份冲突检查迁回 device；厂商 CRUD 仍由 gateway 提供具名能力。
- [ ] D-02：从 casework 与通用 CaseStore 拆出设备工单；参数不再收任意表名。状态机沿用 device/fault.go，历史事件和用户可见字段与状态变更同事务。
- [ ] D-02：告警同步与确认归 device，用例调用 typed gateway 读取；后台 ops 目录中 ackAlert 只是 HTTP 路由位置，不拥有 alert_event。
- [ ] N-device：统一 repo 命名并消除接口／payment 包里的设备表写入。

验收：同导入内容重放、不同归属冲突、网关成功后中央失败可原任务恢复、网关失败留下可重试状态；不能默认为异步导入。用户／后台工单列表、详情、历史和状态变更范围一致；缺失设备证据不伪造故障恢复；告警重复同步无重复活动事件。

缩短导入持锁时间属于后续性能方案，需要持久领取、租约、结果恢复及取消设计，不与 D-01 文件搬迁合并。

### 5.6 pricing：PR-01／N-pricing

来源：[方案管理 HTTP](../internal/interface/http/admin/pricing/charging_schemes.go)、pricing_admin_repo.go、pricing_scope_repo.go、scheme_target_repo.go、metering_repo.go。

目标：SchemeService.Save／ChangeStatus、QueryService.List／Effective／Capabilities、PublicationService.Publish／Assign、有效方案只读能力及所属 repo；计算继续留 domain/charging。

- [x] PR-01：迁入方案格式／范围、目标重叠、设备计量兼容、乐观版本、发布与审计；后台 scope 转为明确用例输入。
- [x] PR-01：保留 pricing → device 单向读取，device 不反向 import pricing；payment／card 使用所需的方案读取能力。
- [x] PR-01：订单／支付意图／刷卡首次扣款继续冻结完整方案和套餐，发布变更不覆盖历史快照。
- [x] N-pricing：根包 repo 命名，不新增一套规则计算或每设备协议的计费包。

验收：不兼容设备、目标重叠、版本变化和无权范围被拒绝；发布事务失败不留下半发布；不同收费模式、单位与舍入继续回归已有 domain 测试；历史价格不受当前方案修改影响。

### 5.7 card：CA-01／N-card

来源：[刷卡 HTTP](../internal/interface/http/card_api.go)、[主事务](../internal/card/card_repo.go)、[后台在线卡](../internal/interface/http/admin/user/online_cards.go)、worker/charge/cards.go。

目标：SwipeService.Apply、CardStatusService.Change、CardAdminService.Bind／ChangeStatus；card_repo.go、charge_repo.go、start_result_repo.go 与窄读取能力。

- [x] CA-01：将事件时效、端口事实、活跃会话、方案与余额准入放入 SwipeService；现有 Swipe 事务原样收拢后再考虑拆函数。
- [x] CA-01：迁入用户卡状态和后台绑定／启停事务，绑定人与操作范围校验归用例。
- [x] CA-01：为 charging 提供会话 Lock／ConfirmStart／FailStart／ClearActivePort 的稳定 InTx 能力，避免跨模块自行映射 card 表。
- [x] CA-01：卡事件读取、决定冻结、reply、advance 迁入共享契约和具名客户端；冻结后的回复和原 event_key 重试不变。
- [x] N-card：更新 CardStore 等类型与调用方，不把纯卡号／端口校验搬出 domain。

验收：过期事件拒绝、同事件重放、卡归属及冻结限制、端口占用冲突；续费与截止冻结竞争保持已购时长一致；扣款／累计时长／操作凭据一起提交；回复丢失后重发原决定，金额与不足余额语义不变。

### 5.8 order 与 charging/query：O-01／CQ-01／N-order

来源：[用户订单查询](../internal/interface/http/charge_user_query.go)、[反馈提交](../internal/interface/http/charge_feedback.go)、[后台 casework](../internal/interface/http/admin/order/casework.go)、order 的 history／view／package repo，以及后台 order_live／order_process。

目标：order.QueryService.History／Detail、FeedbackService.Submit／Reply；charging/query.Service.Ongoing／Snapshot／Curve／Process。身份、范围及窗口作为业务输入。

- [ ] O-01：订单基础 repo 提供状态锁定、编号、定价快照和历史投影；付款／退款金额写入仍调用 payment 能力。
- [x] O-01：反馈提交、回复状态、订单归属与附件规则迁回 order，拆除通用 case 的表名选择能力；审计同事务。
- [ ] CQ-01：组合计费进度、cutoff、支付／费用摘要及 gateway 遥测／历史曲线／过程；定义窄 Reader，order 不 import settlement 或 charging。
- [ ] CQ-01：gateway DTO 转为本用例读模型，再由 HTTP 映射公开字段；不把当前遥测当最终结算，不将缺失值替换为零。
- [ ] N-order：history／state／view／package／snapshot repo 统一名称，纯展示规则与 SQL 投影分清。

验收：用户隔离、后台范围、稳定分页、非法／超限曲线窗口；已结算与待计费明确区分；网关不可用时按现有接口表达缺失／失败；套餐名称从冻结快照读取；反馈状态和重放规则不改变；组合查询无逐行外部调用。

### 5.9 marketing：M-01／N-marketing

来源：HTTP admin/marketing/coupons.go、coupon_activity.go、user_coupon.go、coupon_api.go；[核销存储](../internal/marketing/coupon_redeem_store.go)、[活动规则](../internal/marketing/coupon_activity_store.go)。

目标：CouponService.Create／Grant／Redeem、ActivityService.Create／Update／ApplyInTx，保留已有纯规则与同事务发放／核销能力。

- [ ] M-01：迁入管理员活动、券、发放目标／预算／范围、用户领取及核销的业务准入；HTTP 不组合预算判断和写入。
- [ ] M-01：沿用支付与计费事务中的权益能力，活动不适用与存储失败分别表达；marketing 不 import payment／settlement。
- [ ] M-01：冻结规则、金额和 event_key，保留确定编号、请求凭据与并发预算当前读。
- [ ] N-marketing：规则文件保留业务名称，持久化文件和类型改 repo。

验收：预算边界的两独立事务确定性测试；重复事件／领取／核销只生效一次；过期／冻结／不适用拒绝；优惠与实付一致，事务回滚不留下发券或核销结果。

### 5.10 identity：I-01／N-identity

来源：[认证适配](../internal/interface/http/auth.go)、[登录接口](../internal/interface/http/user_api.go)、phone_api.go、admin/user/charge_users.go、identity 的用户与微信能力、infrastructure/redis 的会话实现。

目标：SessionService.Login／Refresh／Logout／Authenticate、PhoneService.Bind、UserQueryService；用户 repo 与 session_repo.go 的 Redis 实现。HTTP auth 子包只提取凭据、调用身份能力及写入请求上下文。

- [x] I-01：登录交换 → 用户准入／创建 → 会话创建 → 签发，以及签发失败撤销会话的顺序迁入用例。
- [x] I-01：刷新、注销、冻结用户和会话归属核对进入服务；Redis／JWT 实现通过所需能力注入。
- [x] I-01：将 infrastructure/redis/session_store.go 及用户会话测试迁到 identity/session_repo.go；保持用户 ID 十进制字符串、旧数值记录兼容、Lua 原子轮换、previous 摘要与 KEEPTTL。通用令牌机制与 OP-01 共用，RT_ 和 user:session: 前缀留在 identity。
- [x] I-01：手机号正式绑定继续使用微信验证结果；开发模式单独注入，不能与正式绑定共享绕过分支。
- [x] I-01：后台用户列表／详情／状态业务归 identity，充值历史通过钱包读取能力提供；后台账号、角色和 MFA 留 ops。
- [x] N-identity：用户与用户会话的持久化类型／文件命名归 repo，保留既有稳定的 Repository 接口名和会话键格式；cmd 注入新实现，删除旧用户会话实现。

验收：会话过期／撤销／刷新竞争、冻结用户、跨用户凭据、签发失败清理；号码唯一性与归属、重复绑定、微信缺失结果；公开身份仍为十进制字符串，开发能力不能进入生产路径。

### 5.11 ops：OP-01／N-ops

来源：[账号管理](../internal/interface/http/admin_accounts.go)、[角色管理](../internal/interface/http/admin_roles.go)、[MFA](../internal/interface/http/admin_mfa_settings.go)、[审计辅助](../internal/interface/http/admin_resource.go)、[dashboard](../internal/infrastructure/database/admin_dashboard_store.go)、HTTP admin/ops/operations.go。

目标：AccountService、RoleService、MFAService、AnnouncementService、DashboardQueryService；account／role／audit／projection repo、session_repo.go 的后台会话实现及 mfa_repo.go。审核人实时状态能力供 settlement 使用。

- [ ] OP-01：先提取 Actor／AuditContext 值与 AppendAuditInTx，供前面批次复用；HTTP 不持有 AuditedTransaction 的业务提交责任。
- [ ] OP-01：账号创建／修改／删除／解锁／密码重置、角色和数据范围变更、MFA 设置迁入服务；保留最后管理员保护及角色集合的原串行化边界。
- [ ] OP-01：权限实时读取，不信任 JWT／前端菜单；首签人实时权限查询通过注入回调或窄接口提供，ops 不 import settlement。
- [ ] OP-01：Redis MFA pending／锁／频次 IO 由实现层提供，规则归 ops；审计和用户返回不得包含密码、token、密钥、验证码。
- [ ] OP-01：将 infrastructure/redis/admin_session.go 迁入 ops/session_repo.go，后台刷新、撤销与挑战记录由 ops 持有；保留 AuthVersion、ART_／admin:session:、绝对 TTL、轮换／撤销脚本和令牌格式。共用技术能力按第 2 节提取，不让 ops 与 identity 为复用会话实现互相引用。
- [ ] OP-01：公告业务归 ops；dashboard 移成有明确来源的跨模块只读投影，保留北京时间日界。admin/ops 中告警确认转 D-02。
- [ ] N-ops：业务 repo 统一命名；不要通过泛化“表名 + values”写入接口绕过所属业务模块。

验收：回归现有并发互删与角色锁真库测试；过期权限、越权范围、锁定／解锁、TOTP 重放、首签权限撤销；事务失败不留下审计成功记录；dashboard 查询失败不输出伪完整快照。

### 5.12 export：E-01／N-export

来源：[任务与清理／下载](../internal/interface/http/admin/export/exports.go)、[CSV／XLSX 格式实现](../internal/interface/http/admin/export/exports_format.go)、export_task_store.go、export/cleanup.go。

现状基线：PDF 生成、字体目录与 fpdf 依赖已移除，当前仅支持 CSV、XLSX。PDF 创建请求返回参数错误，历史 PDF 任务不再提供下载；资源清单、OpenAPI 和初始化 SQL 已同步。本任务以这一实现为准，不再安排字体迁移或恢复 PDF。现有数据库未因初始化 SQL 修改自动变更。

目标：ExportService.Create／Generate／GetDownload／CleanupExpired、FormatWriter、ExportRepo；HTTP 只流式输出被授权的文件。清理循环继续由 admin 受管理运行。

- [ ] E-01：迁入资源选择、范围快照、行数限制、生成、成功／失败推进与清理；数据源使用明确只读 repo，避免复制业务写入。
- [ ] E-01：首批保持当前同步执行／受管理生命周期，不添加脱离请求 context 的后台 goroutine，也不默认把导出搬给 worker。
- [ ] E-01：下载用例复查当前权限、申请人、范围与有效期；文件路径由可信目录与任务编号推导，不信任库中任意路径。
- [ ] E-01：迁入 CSV、XLSX 格式实现，保留转义、中文、缺失字段表达与 XLSX 流式写入；资源清单、格式校验、MIME、文件扩展名和 OpenAPI 保持一致。
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
| D-02／N-device | 待实施 | 导入、设备运营和站点 repo 已改名；报修状态与事件表访问已归 device/fault_repo，并回归实际后台指派／修复／关闭与3条历史。故障业务编排、告警及其余持久文件、用户站点组合查询仍待迁移 |
| PR-01／N-pricing | 完成 | SchemeService／PublicationService／QueryService 接管模板、发布、继承及读取组合；五个 repo 改名并核对中文契约。真库覆盖版本、范围、兼容、审计回滚及同请求两连接重放；注册 HTTP 覆盖创建、发布、继承、模板列表、设备能力及空方案原响应。设备普通事实及锁已使用所属能力，工作区及独立暂存快照默认检查、全仓真库、全部 cmd 构建和 tidy 已通过；现有双模拟器实际 TCP／心跳／摘要／扫码及完整方案读取通过 |
| CA-01／N-card | 完成 | SwipeService、CardStatusService、CardAdminService 接管准入及完整事务；所属 InTx 能力保持资金、订单及回执边界，六个 card store 改为 repo。共享卡契约与具名客户端贯通实际注册端点、worker 及 cmd。两连接绑定、同事件扣款、续费与截止双向竞争、旧 RR 当前读、审计回滚及冻结回复恢复通过；本地 TCP 首购／续购／停机／结算退款走通。工作区及精确暂存快照默认检查、独立三库、全部入口构建与 tidy 已通过 |
| O-01／CQ-01／N-order | 进行中 | FeedbackService 接管用户提交及后台回复／关闭审计，反馈与报修表访问分归 order/device，通用 case 表名选择已删除；真库、注册路由及旧 RR 当前读变异通过。订单基础持久命名与组合查询、CQ-01 尚待实施 |
| M-01／N-marketing | 待实施 | 既有规则／并发证据保留，接口编排待迁移 |
| I-01／N-identity | 完成 | SessionService、PhoneService、UserQueryService 及用户／会话 repo 归位，旧实现删除；真实三库与 Redis、实际注册路由及两连接开户／唯一号码通过。公开精度、旧记录连续轮换、冻结即时生效、签发失败清理、5次批量查询和充值归属已核对；工作区及精确暂存快照默认检查、全量 integration、全部 cmd 构建、tidy 与前端通过，本地 user/admin 流程通过 |
| OP-01／N-ops | 待实施 | 实时权限及最后管理员保护保留，后台会话与 HTTP 事务待迁移 |
| E-01／N-export | 待实施 | 清理循环已有；PDF／字体移除已完成，CSV／XLSX 用例与文件生成仍待迁移 |
| H-01／X-01／G-01／WK-01／N-gateway／N-worker | 待实施 | 现有具名客户端、runtime 和资源管理保留 |
| INF-01 | 待实施 | dashboard、case、用户与后台会话归位后，删除 infrastructure 及残留引用 |
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
