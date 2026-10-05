# 架构边界与组织约束

依据：2026-10-05 的源码检查。五个部署进程及 charging 的生命周期拆分已经落地；本文关于业务入口下沉、剩余表访问与服务调用收敛的内容是后续实施约束。

配套文档：[charging 检查结果](charging-review.md)、[模块重构计划](refactor-roadmap.md)。

## 数据访问命名

项目自定义的数据访问组件统一使用 `repo`，表示 Repository。下列名称是目标约定，现有源码随对应模块迁移；引用当前源码时仍使用真实文件名和类型名。

| 对象 | 目标命名 | 迁移示例 |
|---|---|---|
| 数据访问文件 | `<业务对象>_repo.go` | `payment_intent_store.go` → `payment_intent_repo.go` |
| 对应测试文件 | `<业务对象>_repo_test.go`，保留现有 integration 标记与测试辅助文件职责 | `payment_intent_store_test.go` → `payment_intent_repo_test.go` |
| 持久化类型 | `XxxRepo` | `PaymentIntentStore` → `PaymentIntentRepo` |
| 注入的数据访问字段 | `Repo` 或按职责命名的 `XxxRepo` | `Store PaymentIntentStore` → `Repo PaymentIntentRepo` |
| 已有独立的数据访问包 | `repo` | `gateway/store` → `gateway/repo`；`worker/store` → `worker/repo` |

业务模块根包内直接放 `*_repo.go`，不为命名统一额外创建 repo 子包。SQL 和数据库写入位于所属模块的 repo 文件；持久化模型继续使用 `*_schema.go`／`*_model.go`。业务入口按行为命名，例如 `checkout.go`、`refund.go`；完整的事务归属、锁顺序和模块职责保持既定边界。

窄接口仍按消费者需要的能力命名，例如 Reader、Authorizer、Gateway，不强制全部改成 Repo。第三方库的 Store 名称不改。源码迁移时同步文件名、包名、类型、字段、import、测试和调用方；不保留新旧名称并行的兼容别名。

名称迁移覆盖 charging 的现有子包及其它模块，不改变数据库表名、列名、JSON 字段或服务 API。行为修复和命名迁移分开核验。

## 中文注释规范

详细注释是每个重构任务的交付要求，与代码迁移一同完成。覆盖任务范围内新增、修改和迁移的代码，包括未导出的类型、字段、函数、方法、接口方法及局部持久化投影；不能因函数短或文件仅改名而省略核对。

注释使用中文，语言严肃、简练、准确。标识符、协议名称、单位及编译指令保留原文。删除口语、比喻、自问自答、主观强调、实施过程和失效历史；不逐行复述赋值、循环等语法，不用模板句重复函数名。

| 对象 | 必须说明的内容 |
|---|---|
| 类型与接口 | 职责、使用场景、数据归属；涉及持久化或外部契约时说明适用边界 |
| 结构体字段 | 每个字段的含义；适用时补充来源、单位、取值范围、零值／空值含义、可选性、快照／实时值及敏感性。依赖字段说明能力与必要的生命周期／并发约束；字段组中的每个字段也须能对应到自己的说明 |
| 常量与枚举 | 业务含义、单位、状态或边界；不得只把标识符翻译成中文 |
| 函数与方法 | 行为及前置条件；逐项解释入参含义、约束与所有返回值的语义；接口方法同样说明契约，接收者有可变状态时说明影响 |
| 错误与特殊返回值 | error 的失败条件、业务冲突、结果未知及可重试边界；说明 bool、nil、零值、空集合、部分结果分别代表什么，以及结果与 error 的组合是否有效 |
| 核心逻辑 | 在对应代码段说明业务依据、执行顺序、必须保持的条件及异常恢复；重点解释决策原因与改变顺序的影响 |

函数注释首句以函数／方法名开头并概括行为，随后可按“入参”“出参”“事务与副作用”分段。`ctx` 说明取消影响的操作，`tx` 说明调用方是否已开启事务、谁负责提交／回滚及所需锁；外部调用说明持久化受理与实际完成的区别。无业务参数或无返回值的函数如实说明，不虚构契约。

核心逻辑至少覆盖所涉及的事务边界、锁顺序与隔离级别、幂等身份与内容比对、资金及状态约束、金额／计量单位与舍入、历史快照与当前事实、权限／范围／双人复核、提交与外部调用顺序、未知结果与重试／补偿、租约／游标、取消与资源释放。规则注释放在决策处，字段释义放在定义处，避免同一规则多处复制后失去一致性。

注释必须与真实实现一致，不能将设计目标、计划修复或未验证假设写成已有保证。修改逻辑、字段、契约或顺序时同步更新注释；迁移时复核原注释。保留 `go:build`、`go:embed`、`go:generate` 等指令和 `Deprecated:` 标记。字段含义与 SQL COMMENT、JSON／服务契约、OpenAPI 保持一致，注释补齐本身不改变运行行为或数据库结构。

## 下一阶段的组织原则

资金批次已将计费调度、checkout、充值／退款申请、钱包风险及结算审核的事务入口迁入所属模块。仍待迁移的业务包括方案发布、卡前置准入、导入、工单、活动、账号及导出；这些用例仍要求读者跨 HTTP 和模块才能理解完整流程。资金模块的注释、锁图与路由验收继续按路线图完成，不以文件改名代替任务验收。

先让一个用例在所属模块中有可调用的业务入口，再细分目录。默认在现有模块根包中增加按行为命名的用例文件与 repo 文件；只有职责、调用方和依赖已经可以独立说明时才新增子包。不要把所有模块统一切成 service/repo/model，也不要为每张表建一个业务包。

依赖和事务按以下方式收敛：

1. 用例方法收 `context.Context`、业务输入、操作人及数据范围，返回业务结果和可识别的错误；不收 `*gin.Context`，不返回 HTTP 状态码。HTTP 保留身份提取、参数绑定、协议校验和错误映射；资源归属、额度、状态准入等业务检查进入用例。
2. 一个业务事务由一个用例拥有。跨模块通过所属模块的 `…InTx` 函数访问数据，显式传 `*gorm.DB`，保持锁顺序、隔离级别、幂等记录、资金、审计和 Outbox 的原子性。不把同库事务替换成服务间 HTTP。
3. 已有单向引用只在有业务理由时保留；新增引用先检查反向关系。若消费者和提供方已有相反依赖，由消费者定义窄接口、cmd 注入实现，不能靠把持久化模型移入 domain 或新建万能 common 包打破循环。
4. 对外支付、设备调用和文件 IO 与数据库提交的先后次序逐用例保留；需要改变重试或锁持有时间时另列行为变更，不能混入搬文件的提交。
5. 模型按写入归属定位。跨模块只读视图返回明确的业务投影；带表名和列映射的结构留在所属模块的 schema／model／repo 文件中，不作为跨服务 DTO。

## 必须先处理的依赖交叉

最容易把新结构改出循环的是支付、钱包和退款。当前已有 `settlement → payment/wallet/card/marketing/order/ops`、`card → payment/wallet/order`、`payment → wallet/marketing/order/ops`、`wallet → ops`、`identity → wallet`、`pricing → device` 等单向引用。迁移服务前要把新增箭头与这些关系一起核对。

计费管理入口由 `pricing.SchemeService`／`PublicationService` 负责，新增 `pricing → ops` 只调用同事务审计能力，ops 不反向引用 pricing。显式 `operations.DataScope` 是实时范围输入，厂商归属经 `device.OperationRepo.VendorID` 单向读取；device 不引用 pricing。发布默认事务先锁站点，随后读取原请求、共享锁模板、核对目标兼容与当前版本，发布规则、原请求凭据及审计一起提交。相同请求恢复原结果，不重新读取当前模板或替换任何订单／支付／刷卡快照；HTTP 输入输出和错误码沿用原约定。

设备导入由 `device.ImportService` 拥有原两段事务。受理冻结从 gateway 厂商公开字典解析的协议及能力，设备身份、任务与受理审计同事务；执行前通过注入的窄范围读取重读当前范围。执行同步保持任务锁，最多十秒等待开通；网关失败提交 failed，中央元数据或完成审计失败回滚执行事务，原 pending／failed 任务可重试。`ImportCompatibility` 由 cmd 注入 `pricing.Repo`，device 无 pricing import；新增 `device → ops` 仅用于所属变更的同事务审计。gateway 的具名 Vendor／Provision 客户端只引用共享契约和通用传输，厂商 CRUD 仍归 gateway。实际两库提交、真实注册 gateway 路由及 loopback 网络证明原身份恢复，测试身份按自身清理为零；部署栈／实机与并发导入验收仍待批次完成。

- **迟到付款退款**：payment 不直接 import settlement，因为 settlement 已 import payment。由 payment 定义 `LateRefundRecorder` 一类的窄能力，方法接受事务和退款业务输入；cmd 注入 settlement 的实现。payment 回调在原事务内调用，settlement 写 refund_record 和相关退款事件；payment 更新支付状态。契约不暴露 `settlement.RefundRecord` 或 HTTP DTO。
- **充值到账**：payment 可以调用 wallet 的事务内到账能力，但 wallet 的充值申请不能因此反向 import payment。wallet 定义所需的支付来源／创建能力，cmd 注入 payment 适配；所有需要同库原子性的调用沿用同一个事务。先确定锁顺序和输入契约，再迁 SQL。
- **钱包退款预占**：wallet 定义事务内 `RefundRecorder` 能力，cmd 注入 settlement 实现。wallet 保留冻结金额与来源分摊，settlement 创建 refund_record；共同沿用原事务。settlement 已依赖 wallet，因此 wallet 不反向 import settlement，也不搬走钱包自己的资金写入。
- **结算模型归属**：charge_fee_receipt／refund_record 模型已归 settlement。payment 的迟到退款调用注入能力，钱包请求通过自己的接口注入结算记录；payment 不留结算类型别名。跨模块查询使用各自的投影或所属模块读取函数。
- **订单组合查询**：order.QueryService 提供所属用户归属、历史、后台基础档案与事件；结算 query_fee_repo 实现 order.FeeReader 并由 cmd 注入，order 不 import settlement。用户历史固定读取总数、当页与批量费用三次 SQL，缺失或无效费用回执保持 null，有效零费用保留非空零。后台订单沿用 order.read 全局权限及显式站点筛选；站点名称是一次只读跨域投影，明确读取 station 表，原冻结套餐不被当前价格替换。订单金额及退款写入仍属 payment／settlement。charging/query.LiveService 已接管后台实时估算：每20单一批复用 gateway ChargingEvidence 契约，所有有完整观测的订单各一次读取 order.SnapshotRepo 和 card.PurchaseQueryRepo。query → order 只读原冻结 JSON，卡购买通过窄接口注入，查询业务服务不持有 URL、令牌或 GORM；没有反向依赖。原完整性为 false 时保留观测量并隐藏估算费，不翻页假装完整；保持原10080行证据上界。整页受4秒取消期限管理，无未管理任务；一批网关失败使该批不可读，后续独立批仍可执行，同批错误范围最大20单。快照或卡读取故障保留观测量并标记不可读。后台过程已由 charging/query.ProcessService 组合 order.QueryService 的已授权规范身份和具名 gateway.OrderProcess；订单身份投影从 ops/admin_audit_store 归 order/admin_query_repo。公开额外设备／业务号参数不能覆盖原单，order.read 与缺行在网络调用前判定，游标保持原整数精度；网关DTO转换为用例 ProcessPage，再输出原公开形状。HTTP不持有过程代理或连接池，cmd注入现有受管理客户端。queryData为查询强制5秒期限、禁止重定向及完整信封字段校验，复制Client配置但复用Transport；遥测／历史共用DTO已经与实际网关注册端点核对，用户消费者已使用具名能力。charging/query.UserService 的每个组合入口先通过 order.UserOrders 核对当前用户归属，再读取费用与原冻结状态；UserStateRepo 只调用 order、settlement、card、stop、payment 各所属读取函数，不直接访问其他表。只读组合不引入新的写入事务，原卡操作、支付、退款与 cutoff 分别转换为本用例模型；order 基础包不反向引用 query 或 settlement。费用／支付存储故障返回503，无有效回执／无支付记录分别保留原省略；网关故障只使快照遥测不可用，曲线则返回503。确认零费用、DECIMAL电量文本与观测零值保持独立，最终电量优先于聚合摘要。窗口枚举与配置允许集合、三年与2000桶边界在调用前核对；未知或禁用窗口返回400，取消不派生后台任务。公开接口形状与真实注册响应同步到 user OpenAPI；网关平均功率列显式映射 average_value，原count加权规则与DECIMAL字符串保持。
- **审计与运营**：ops 提供事务内审计能力和通用操作人／范围值；不要让 ops 的账号服务 import 每个业务服务。dashboard 和运维组合操作与基础账号能力分开，跨业务调用采用窄接口，避免「所有模块都依赖 ops，ops 又依赖所有模块」。

这些接口只用于反向依赖风险或可替换的 IO 边界。单向且有业务理由的事务内函数调用继续保留，不给每个 repo 套一层同形接口。

## HTTP、worker 与 gateway 的配套收敛


卡用例的入口由 SwipeService、CardStatusService 和 CardAdminService 拥有。新增 card → device/identity 的单向读取分别核对真实端口与协议、活跃绑定用户；card 的当前方案仅通过 cmd 注入的窄接口读取 pricing。支付与意图写入委托 payment/card_funding_repo，订单写入、快照、事件及续购订单锁委托 order 所属 InTx 能力；wallet 金额仍由钱包账本写入，所有卡扣款与时长、原操作共用完整事务。card/interface_query_repo 的余额和有效性是单条只读联表投影，不写用户或钱包表。卡绑定没有站点列，沿用实时 online_card.manage 全局用户管理权限，不把设备范围误用为卡归属。首次绑定先竞争卡号唯一键，再当前读锁定身份，避免缺行 RR 间隙锁；同人已启用重放不重复审计。

续购依次锁卡、钱包、卡会话、订单，再通过 cmd 注入的 stop.CutoffRepo 窄接口当前读截止；card 不 import stop，避免 stop → card 的反向循环。先前 RR 快照不会因后续行锁刷新，因此原操作、会话身份、待确认状态和截止使用当前读。两真实连接的截止提交晚于快照场景已复现漏拒绝，当前读修复后无扣款或时长增加。首次扣款继续冻结完整方案和套餐，当前发布改变不影响续购。

卡事件协议位于 contracts/central/card 与 contracts/gateway/card，具名客户端位于 integration 对应服务；worker 只持有所需窄接口，不持有 URL、服务令牌或通用 HTTP。卡回复先按原事件首写冻结，竞争失败采用权威回复，发送后未知结果沿用原冻结 JSON 和 event_key 重试；余额随后改变不能重算回复。明确 HTTP409／2009 才映射业务拒绝，不将网络未知结果或畸形响应映射为卡无效。五秒期限、受管理连接池和禁止重定向由 integration 与 cmd 管理，真实设备消费仍依赖后续流程验收。

**HTTP 目录在业务入口迁出后整理。** 目标直接采用 `internal/http`，移除只有 http 子目录、没有独立职责的 interface 层。用户侧按 `user/payment`、`user/wallet`、`user/order`、`user/card`、`user/identity` 等实际接口族逐批归类；central 内部端点与 admin 接口保持各自注册入口，auth 提供共享 HTTP 鉴权适配。现有 admin 业务子目录继续复用，业务事务移走后结构体只持有所需服务、鉴权器与 HTTP 配置。

通用 HTTP 响应与服务器工具仍归 platform/httpapi，服务间出站客户端归 integration；gateway／worker 的进程特有入口按既定运行职责组织。以后新增入站协议时依据实际职责增加目录，不预建只有分组含义的 interface／transport 层。路径迁移同步 import、cmd、OpenAPI 嵌入资源及测试，当前源码引用在迁移完成前保持真实路径。

先确定授权上下文和错误映射的公共适配，再移动处理器，避免子包重新 import 一个包含业务实现的巨大 http 父包。仅真正通用的响应／middleware 能力进入 httpapi 或接口层小包，不创建通用 `Deps` 容器。生产路由注册、用户／后台 OpenAPI 和前端请求形状一起核对。

**服务间调用延续已经落地的 contracts／integration 方式。** 下一批是用户充电状态、历史曲线、采样、设备摘要的 gateway 读取，以及刷卡同步、计费／退款派发、告警与后台调用。按实际 base URL 判断目标服务；用户遥测端点属于 gateway。每条端点先核对真实注册和响应，再迁共享 DTO、具名客户端、窄消费者接口和 cmd 注入；逐条消除剩余通用 `sync_client` 与业务代码持有 URL／令牌的情况。

worker 保留任务顺序、游标、重试与投递编排，计费和退款的金额／状态写入仍由业务用例执行；不把它们搬进 worker。gateway 的协议接入、持久命令、端口占用与会话运行时继续独立；control 中剩余 SQL 逐步改为 gateway/repo 的完整操作，不向控制器返回可继续拼查询的 GORM builder。协议解析不引入数据库。

`infrastructure/database` 当前剩余 dashboard 待迁为 ops 跨模块只读投影；case 文件已拆除，反馈表访问归 order/feedback_repo，报修状态与事件归 device/fault_repo。报修 HTTP 业务编排仍待 D-02，INF-01 不能提前完成。允许明确的跨模块读模型，不以「文件全部搬空」作为正确性证据。

FeedbackService 拥有订单评价、后台读取、回复、关闭及审计事务。order → ops 仅使用所属 AppendAuditInTx 能力，ops 不反向依赖 order；反馈原用户与设备身份来自已锁订单。提交按订单锁 → 已有反馈当前读 → 插入，旧 RR 快照不能漏掉另一连接已提交的反馈；重复提交仍为冲突，关闭重放不重复审计。反馈无站点范围列，后台沿用 feedback.read／feedback.reply 全局权限，普通设备范围不能据页面位置改变反馈归属。回复和关闭的状态／审计共用事务，审计失败整体回滚。

**目标结构同样移除 infrastructure 层。** 现有 `infrastructure/redis` 持有用户／后台会话的记录、寿命、刷新、撤销与键布局，按业务归属分别迁到 identity/session_repo.go 与 ops/session_repo.go；MFA 的 Redis 数据访问归 ops。Redis 实现沿用 repo 命名，不另建顶层 redis 业务包，也不把带用户／后台规则的会话实现整体放入 platform。

无 IO 的随机源保留 platform/auth；令牌布局、编码、摘要及共用原子注销集中 platform/sessiontoken，仅接收调用方的前缀、key 与摘要，不引用 identity／ops 模型。RT_／user:session: 留 identity，ART_／admin:session: 留后台会话所属实现。用户／后台令牌与键格式、TTL、Lua 原子性、旧记录兼容和权限版本校验保持原契约。database 与 redis 职责全部迁出、装配和测试通过后删除空的 infrastructure，避免仅换目录名留下混合职责。

身份用例由 SessionService、PhoneService、UserQueryService 拥有。用户开户通过 identity → wallet 的所属 InTx 能力保持 user 与零余额钱包同事务；openid 唯一锁行先提交以串行恢复，不删除重试所需身份。手机号来源在 cmd 分别注入正式微信交换和开发输入，正式能力拒绝明文号码，唯一键最终仲裁两连接同时绑定。

identity/user_repo 与 charge_user_repo 的钱包、订单、优惠与报障字段是既有只读投影，保留公开金额、空值、用户身份精度及固定批量查询次数，不写其他领域表。后台用户充值页通过 identity → wallet 的窄读取接口装配 payment/recharge_history_repo；payment → wallet 仅依赖充值历史 DTO／读取契约，不反向引用 identity。充值表和金额确认事实由 payment 提供，未删除的冻结用户仍可查原历史；后台 user.read 沿用全局范围，权限在注册鉴权及用例入口校验。钱包风控的冻结写入仍属已定义风控事务，不创建第二条用户冻结后台端点。

用户 SessionRepo 已迁入 identity；Lua 只允许当前摘要轮换，previous 最多32项仅用于注销，KEEPTTL 保留七天绝对寿命。旧数值身份从原 JSON 十进制文本恢复后存字符串，连续轮换保持精度；Redis Lua 失败是存储错误，不伪装为失效令牌。用户冻结及签发失败沿用尽力撤销同一会话的原清理顺序，取消导致的清理失败不宣称已撤销。真实会话集成测试使用独立 Redis 6381，按本测试 SID 清理，开发 Redis 数据不参与测试。

## 服务拆分的判断

五个进程目前有可解释的运行职责：user／admin 的身份和对外权限不同，gateway 管设备长连接与协议，worker 管重试与定时，central 提供内部用例执行端点。共用同一套业务包、由各自 cmd 装配是合理的；入口拆分、资源释放和受管理停机也已经完成，本阶段保持该部署结构。

它们仍是共享业务代码与 central 数据库的多进程系统，不代表五个具有独立数据所有权的微服务。当前最有价值的是把用例入口、事务和恢复协议整理清楚；目录混乱不能通过再拆进程解决，也不需要为了整理目录先合并 central。是否合并或进一步分离部署，要在这些边界稳定后根据发布频率、资源隔离、故障范围和实际内部调用成本另行判断。

### central 的后续评估

最初第四步提出的合并候选是 `user + central`：一个业务 API 进程承载用户接口和受保护的内部用例端点，保留 admin、gateway、worker，形成四个部署单元。这是待比较的方案，本阶段仍保持五个进程。

评估应在用例入口、事务和恢复协议稳定后单独进行：

1. 核对 central 是否需要独立扩容、发布、资源限制或安全隔离，并比较共享进程后的凭据可见范围与故障影响。存在明确隔离需求时，应保留独立部署。
2. 逐条列出 central 内部调用、实际调用方、鉴权和重试语义，区分适合改成本地调用的操作与 gateway／worker 仍需远程访问的端点。不能把减少进程理解为取消持久化确认或授权。
3. 比较两种部署的调用耗时、网络失败与补偿成本、资源使用、发布和排查成本；以实测或明确运维要求作决定，不按代码行数裁决。
4. 若选择合并，单独制定配置、路由、服务令牌、网络暴露面、资源生命周期及 Compose 的切换与回退方案。内部端点继续受服务令牌和网络入口限制；先验证支付到账、启动与拒绝、断线重试、主动／自动停止、结束重放、计费／退款以及停机排空，再调整部署。

部署决策与批量代码迁移分开交付；当前的模块重构计划不执行进程合并。

## 批次 0 的依赖核对

stop 通过 card 的已购时长与会话行锁能力读取 card_charge，通过 order.LockChargingState 当前读取并锁定订单；截止点仍由 stop 写入。卡会话→订单锁顺序与刷卡续购一致，首写截止点及提交由 stop 的完整事务控制。card／order 不反向引用 stop；恢复使用原截止点和订单身份向具名 gateway 客户端重试。

## 资金批次的实现边界

| 用例 | 所属写入与协作 | 事务及外部调用 |
|---|---|---|
| payment.CheckoutService | PaymentIntentRepo 写支付、意图、预支付参数；device.DiscoveryService 合并 gateway 端口与中央运营状态 | 原请求先重放；首次意图事务提交后申请渠道参数，以支付唯一键保存并重读首写参数 |
| payment.CallbackService | 支付、意图、摘要归 payment；钱包到账调用 wallet；迟到退款通过注入 RefundRecorder 归 settlement；订单、快照、事件调用 order；Outbox 调用 ops | 原回调 READ COMMITTED 事务不拆分，任一协作失败全部回滚；正常到账和迟到退款分支保持原身份 |
| wallet.RechargeService／RefundService／RiskService | 钱包、请求、风险及来源分摊归 wallet；支付创建／来源通过接口注入 payment；退款记录通过接口注入 settlement | 申请及资金预占同事务，风险动作与审计同事务；批准表示已登记待执行退款，不代表渠道成功 |
| settlement.BillingService | FeeCalculator 保存计算回执，BillingOrders 原子应用资金与费用回执，BillRepo 按原订单幂等开账单 | 原费用先投递；已入账缺账单由 Missing 有界恢复，不重新扣款 |
| settlement.InvoiceService／审核服务 | 发票、计量核实、退款及审计归所属完整用例；order 提供开票来源行锁，payment 提供原支付锁与已确认退款更新 | 开票按用户／订单锁串行一单一张，使用 READ COMMITTED；计量核实存在性与原凭据采用当前读，不能假定任务行锁刷新旧 RR 快照 |

wallet 不 import payment 或 settlement，payment 不 import settlement；cmd 注入窄能力。退款输入位于 domain/money，不带 ORM 映射。资金模型按所属模块移动，不保留旧 payment 类型别名。现有跨表只读视图仍留 repo；用户身份表访问将在 I-01 收拢，不能将它们描述为已全部消除。

本批次单独验证的行为修复包括：账单投递后失败可仅补账单；并发预支付返回首写参数；钱包退款列表读取实际审核结论并按当前页批量补查；发票同请求拒绝不同用户／订单／内容，同订单不同请求串行防止重复申请；计量核实在旧 RR 快照下仍读取已提交的原凭据。验证范围是实际 MySQL、注册 HTTP、具名 loopback 客户端及受控渠道／身份 fixture，尚不代表真实支付或实机验收。

资金入口的锁与隔离已按实际辅助函数展开：

| 路径 | 锁／隔离与恢复依据 |
|---|---|
| 充值申请 | READ COMMITTED；身份普通读取后 user→wallet，原申请及支付只读重放，不反向取得回调持有的支付锁。申请提交后准备参数，原 request_id／payment_order_id 恢复未知响应 |
| 充值回调 | READ COMMITTED；payment→recharge_request→wallet，付款人 openid 同原用户固定身份核对。已支付重放在钱包写入前返回；摘要、金额、交易身份必须一致 |
| 钱包退款申请 | 钱包锁后才建立默认 RR 快照，申请重放是只读；冻结→候选已支付 ID→按 PRIMARY 索引锁候选支付→在途退款当前读。候选读取与锁定共两次 SQL，避开待支付回调的行锁 |
| 风险批准／解除 | READ COMMITTED；request→wallet→支付／退款，或 request→wallet→关联冻结／其他生效冻结；原申请路径不会在已有请求重放时取得请求写锁。批准、解冻、原回执和审计同事务 |
| 人工退款／计费 | 支付→订单／退款；余额计费先钱包→支付→订单。早期普通读取形成的 RR 快照不刷新，在途退款聚合、原申请、首签及费用回执使用当前读；稳定退款号与原载荷仲裁重复投递 |
| 渠道退款 | claim 及成功确认使用适用的钱包→支付→退款；失败确认只使用适用的钱包→退款，不改支付金额。原成功回执及锁后分摊／余额使用当前读，流水余额对应实际出账；持久 processing／next_attempt_at 先提交，网络未知后查询原单，确认回执后才出账或释放预占 |
| 开票／计量核实 | 开票 READ COMMITTED 且锁所属订单；审核锁发票申请。计量提案／裁决锁计费任务后当前读原核实凭据，不用任务锁推断旧快照已刷新 |

新增真库证据覆盖：充值申请在身份读取后等待用户锁仍复用原支付；待付支付锁未释放时退款能完成已支付来源锁定；风险批准等待钱包锁后能使用新到账来源；充值余额 UPDATE 后取消请求会回滚支付、摘要、余额和流水。两独立连接固定旧 RR 快照及提交先后，还验证人工退款重放、接续首签、费用投递重放、钱包退款成功重放和流水余额；修复前分别出现冲突、重复键或余额流水错误，修复后通过。把相应隔离改回 RR、把候选扫描改为加锁扫描、删除付款人核对的变异分别稳定失败，变异均已恢复。这些证据验证上述指定路径，不宣称整个系统不存在任何死锁。

设备与计价子批次新增的实际边界：设备管理由 ManagementService 处理范围过滤、完整资料版本、运营启停及同事务审计；StationService 对已有站点详情和更新使用显式范围，补齐原列表受限但详情／更新无范围校验的缺口。新建沿用 station.create 权限，不自动增加账号站点范围。OperationRepo 提供设备归属、适配器和计量能力，pricing 的普通设备事实及站点锁使用 device 所属能力。scheme_target_repo 的目标兼容检查保留一次跨表只读投影：排除 pricing 当前自有规则并 SHARE 锁目标 device_meta，只供同事务发布准入，不写设备配置。

导入受理先以原 import_id 唯一键登记 pending，再当前读原任务；身份、方案兼容和受理审计失败仍整体回滚登记。两段同步事务继续保留，执行前重读范围。设备身份登记、网关开通批次和中央元数据使用一致设备号顺序取得锁，持久原清单仍保持原数组顺序，重放内容与操作人比对不变。指定两连接证据覆盖同请求首次竞争和反向清单重叠，恢复缺行锁或请求顺序的变异稳定失败；不据此宣称全部导入路径无死锁。

厂商字典仍归 gateway。VendorService 接管中央账号范围、公开查询／写入编排和审计，integration/gateway 的具名四操作保持十秒、一 MiB、禁止重定向与原公开错误映射，并核对详情／更新结果的原厂商身份。写入网络结果未知时不自动重试或更换编码；中央审计仍沿用网关提交后的尽力记录，失败只记录日志，不能回滚网关提交，也没有新增持久审计补偿。注册真库测试明确验证这一继承边界。后台具名客户端复用 cmd 管理的 HTTP 池，受管理请求退出后释放闲置连接；真实支付、实机以及完整模拟器流程仍需后续验收。
