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

## 下一阶段的组织原则

当前主要问题已从「业务代码都在 database」转为「存储移回模块了，但业务入口仍在 HTTP」。例如 `billing_service.go` 的 `Service.Run`、`payment_payment_http.go` 的支付准备，以及 `user_wallet.go`、后台管理处理器中的事务，仍要求读者跨接口层和业务模块才能理解完整用例。

先让一个用例在所属模块中有可调用的业务入口，再细分目录。默认在现有模块根包中增加按行为命名的用例文件与 repo 文件；只有职责、调用方和依赖已经可以独立说明时才新增子包。不要把所有模块统一切成 service/repo/model，也不要为每张表建一个业务包。

依赖和事务按以下方式收敛：

1. 用例方法收 `context.Context`、业务输入、操作人及数据范围，返回业务结果和可识别的错误；不收 `*gin.Context`，不返回 HTTP 状态码。HTTP 保留身份提取、参数绑定、协议校验和错误映射；资源归属、额度、状态准入等业务检查进入用例。
2. 一个业务事务由一个用例拥有。跨模块通过所属模块的 `…InTx` 函数访问数据，显式传 `*gorm.DB`，保持锁顺序、隔离级别、幂等记录、资金、审计和 Outbox 的原子性。不把同库事务替换成服务间 HTTP。
3. 已有单向引用只在有业务理由时保留；新增引用先检查反向关系。若消费者和提供方已有相反依赖，由消费者定义窄接口、cmd 注入实现，不能靠把持久化模型移入 domain 或新建万能 common 包打破循环。
4. 对外支付、设备调用和文件 IO 与数据库提交的先后次序逐用例保留；需要改变重试或锁持有时间时另列行为变更，不能混入搬文件的提交。
5. 模型按写入归属定位。跨模块只读视图返回明确的业务投影；带表名和列映射的结构留在所属模块的 schema／model／repo 文件中，不作为跨服务 DTO。

## 必须先处理的依赖交叉

最容易把新结构改出循环的是支付、钱包和退款。当前已有 `settlement → payment/wallet/card/marketing/order/ops`、`card → payment/wallet/order`、`payment → marketing/order/ops`、`identity → wallet`、`pricing → device` 等单向引用。迁移服务前要把新增箭头与这些关系一起核对。

- **迟到付款退款**：payment 不直接 import settlement，因为 settlement 已 import payment。由 payment 定义 `LateRefundRecorder` 一类的窄能力，方法接受事务和退款业务输入；cmd 注入 settlement 的实现。payment 回调在原事务内调用，settlement 写 refund_record 和相关退款事件；payment 更新支付状态。契约不暴露 `settlement.RefundRecord` 或 HTTP DTO。
- **充值到账**：payment 可以调用 wallet 的事务内到账能力，但 wallet 的充值申请不能因此反向 import payment。wallet 定义所需的支付来源／创建能力，cmd 注入 payment 适配；所有需要同库原子性的调用沿用同一个事务。先确定锁顺序和输入契约，再迁 SQL。
- **钱包退款预占**：当前 wallet 的预占路径直接创建 refund_record，迁移后由 wallet 定义事务内 `RefundRecorder` 能力，cmd 注入 settlement 实现。wallet 保留冻结金额与来源分摊，settlement 创建退款记录；共同沿用原事务。settlement 已依赖 wallet，因此 wallet 不反向 import settlement，也不搬走钱包自己的资金写入。
- **结算模型归属**：`payment/money_schema.go` 中 charge_fee_receipt／refund_record 的模型最终迁 settlement。先把 payment 的直接退款写入改为上述窄能力，再移动模型与消费者；不能以 payment 中的别名 import settlement，否则立刻形成反向依赖。跨模块查询使用各自的投影或所属模块读取函数。
- **订单组合查询**：order 提供自己的基础视图；组合支付、结算和实时计量的入口定义读取接口并由 cmd 注入。不为做一个列表让 order 再 import settlement，后者已经依赖 order。
- **审计与运营**：ops 提供事务内审计能力和通用操作人／范围值；不要让 ops 的账号服务 import 每个业务服务。dashboard 和运维组合操作与基础账号能力分开，跨业务调用采用窄接口，避免「所有模块都依赖 ops，ops 又依赖所有模块」。

这些接口只用于反向依赖风险或可替换的 IO 边界。单向且有业务理由的事务内函数调用继续保留，不给每个 repo 套一层同形接口。

## HTTP、worker 与 gateway 的配套收敛

**HTTP 目录在业务入口迁出后整理。** 用户侧按 `user/payment`、`user/wallet`、`user/order`、`user/card`、`user/identity` 等实际接口族逐批归类；central 内部端点与 admin 接口保持各自注册入口。现有 admin 业务子目录继续复用，业务事务移走后结构体只持有所需服务、鉴权器与 HTTP 配置。

先确定授权上下文和错误映射的公共适配，再移动处理器，避免子包重新 import 一个包含业务实现的巨大 http 父包。仅真正通用的响应／middleware 能力进入 httpapi 或接口层小包，不创建通用 `Deps` 容器。生产路由注册、用户／后台 OpenAPI 和前端请求形状一起核对。

**服务间调用延续已经落地的 contracts／integration 方式。** 下一批是用户充电状态、历史曲线、采样、设备摘要的 gateway 读取，以及刷卡同步、计费／退款派发、告警与后台调用。按实际 base URL 判断目标服务；用户遥测端点属于 gateway。每条端点先核对真实注册和响应，再迁共享 DTO、具名客户端、窄消费者接口和 cmd 注入；逐条消除剩余通用 `sync_client` 与业务代码持有 URL／令牌的情况。

worker 保留任务顺序、游标、重试与投递编排，计费和退款的金额／状态写入仍由业务用例执行；不把它们搬进 worker。gateway 的协议接入、持久命令、端口占用与会话运行时继续独立；control 中剩余 SQL 逐步改为 gateway/repo 的完整操作，不向控制器返回可继续拼查询的 GORM builder。协议解析不引入数据库。

`infrastructure/database` 当前剩余的 dashboard 和 case 查询按业务用途收敛：dashboard 是 ops 的跨模块只读投影；case 的反馈／故障写入分归 order／device。允许明确的跨模块读模型，不以「文件全部搬空」作为正确性证据。

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
