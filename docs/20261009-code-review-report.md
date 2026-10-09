# 代码审查最终复核报告（2026-10-09）

本报告汇总原审查的逐项复核与再次审查，记录结论、证据边界、修复计划和验收标准。原条目编号仅供追踪，不再保留已被纠正的旧结论。**第 1～8 节保留 2026-10-09 审查时点的证据与方案；F01～F08 已在 2026-10-10 工作区实施，最终变更、验证和剩余边界见第 9 节。** 历史段落中的「尚未实施／未修改源码」不代表修复后的状态。

## 1. 范围与结论

| 项目 | 内容 |
| --- | --- |
| 源码基线 | `1731e16`；依据当前源码、路由、初始化 SQL、Compose、业务契约及测试。 |
| 复核范围 | 原报告涉及的充电、协议、任务恢复、订单、结算、钱包、营销、导出、后台鉴权及前端展示路径。 |
| 方法 | 追踪可达路径、锁与状态转换；数据库范围经独立只读复核，再核对关键源码；结合默认测试与最小复现。 |
| 方案核验 | 历次核对 F01–F08 的修改落点与调用链；此前执行金额解码穷举及零功率方案 overlay。本次重点复核新增论断，并执行 F06 批内释放失败的 overlay 对照验证。仅更新报告，业务源码未修改。 |
| 未完成验证 | 真库／Redis 集成、生产审计与隔离配置、EXPLAIN／性能压测、浏览器交互、实机及真实支付。 |

**需要修复的主要问题**：出账吞支付查询错误、设备配置金额精度损失、退款入口不显示、可信代理配置、TOTP 时间步消费缺失、批处理窗口饥饿、历史异常阻断零功率新窗口，以及旧计费派发覆盖人工金额重新排队。

**应撤销的主要指控**：当前跨管理员草稿串号、人工结算期间继续续购改变金额上限、钱包 N+1／充值误入邀请资格 COUNT、账单查询不能使用复合主键，以及 TOTP URI 空格未编码。

深分页与导出长事务需要测量后优化；管理员代登记 MFA 的验证码预填需要明确产品规则。它们不能与已确认的行为问题混为一类。

证据含义：

- **源码确认**：可达控制流与描述一致；并发或外部环境仍需专项验证。
- **最小复现确认**：实际函数或内存请求表现出所述行为；不代表真库、生产拓扑或实机验收。
- **待验证风险**：有结构依据，但耗时、故障概率或完整交错尚未测得。
- **误报／维护建议**：原缺陷指控不成立，或只需改善表达、诊断和兼容性。

## 2. 真实问题与修复要求

### F01 出账吞支付查询错误（原 10.4）

**证据：源码确认；尚未故障注入。优先处理金额正确性。**

[BillRepo.Issue](../backend/internal/settlement/bill_repo.go)第 81–96 行读取关联支付的实付与退款，只在查询成功时赋值，其余错误被丢弃。后续操作若成功，可能用零实付、零退款写错 `prepaid_cents`、`refund_cents` 和 `shortfall_cents`。同订单重放复用已有账单，不自动纠正金额。

修复：有支付关联时查询失败直接返回错误，由补账入口重试；明确关联支付缺失的错误，不当作没有支付。已读状态查询的展示降级独立处理。

**实现约束（新增）**

- **方案可行且有效，错误返回应只落在 `BillRepo.Issue` 的支付读取处。** [BillingService.Run](../backend/internal/settlement/billing.go)的计数表示费用成功投递／入账，不是成功出账数。出账失败记录到 `first` 后仍 `count++`，末尾继续 `recoverBills`；`deliverPending` 也使用同一语义。不能为出账失败提前退出整轮、减少已入账计数，或把订单重新转为待扣款。
- 补账入口为 [`recoverBills`](../backend/internal/settlement/billing.go)（第 147–156 行，经 `Bills.Missing`），它只选出「已入账但缺少账单」的订单。**已写错的账单不会被 `Missing` 再次选中**，因此修复必须保证失败时不落库，而不是依赖补账纠正已写错的行。

验收：仅对支付金额查询注入故障，账单及 `bill_issued` 事件均不生成；解除故障后金额正确；成功重放不生成第二份账单或事件。

上述「不生成」应在 Issue 单次调用或支付读取持续失败条件下断言。Run 末尾可能同轮补账：首次读取失败、之后恢复时可以当轮成功出账，但仍汇总首次错误。分别覆盖正常派发、待投递回执恢复和 Missing 补账；不要把同轮成功恢复误判为修复失效。既存错账若需纠正，应另行审计，Missing 不承担历史修账。

### F02 DC589 配置金额不能保真（原 3.6）

**证据：最小复现确认；未证明平台入账因此错误。**

[配置编解码](../backend/internal/protocol/dc589/config.go)仅检查范围，编码使用 `/10`。实际往返为 **255 分 → 字节 25 → 250 分**；字节 25 表示 2.5 元，不能解释成 25 分。

修复：`Validate` 要求 `CardAmountCents%10 == 0`，拒绝协议不能表示的金额，保持现有范围限制。

**方案核验：编码修复正确；此前新增的解码说明和验收需改写。**

- [EncodeConfig](../backend/internal/protocol/dc589/config.go)调用 `Validate`，在这里增加 `%10` 校验即可消除输入金额截断。仿真器构造配置上报的 `BuildConfigReport` 同样经过 EncodeConfig。
- `decodeConfigTable` 第 208 行是 `uint16(data[offCardAmount]) * 10`。此前穷举全部 256 个字节，无一解码为非 10 分整数倍；**解码侧不可能得到 255 分**。不能把「两侧均拒绝 255 分」列为验收。
- 解码确实没有调用 Validate；例如金额字节 255 被接受并得到 **2550 分**，随后 `Validate` 会拒绝越界。这是独立的上报范围硬化候选，不是 `%10` 精度修复的遗漏。
- 若实施解码硬化，明确是否采用下发用的全部 Validate 规则，并核对真实设备上报兼容性；不要宣称当前 Validate 已覆盖所有载荷格式／数值精度。
- C6 上报失败由 [adapter](../backend/internal/protocol/dc589/adapter.go)第 332–336 行记录 `ResultCode=0xFF`，正常继续记录事件；`ParseConfigAck` 处理的是 C4 下发应答，与 C6 解码错误不是同一路径。[仿真器 applyConfig](../backend/internal/gateway/simulator/dc589/simulator.go)还先用原始载荷 `configCode` 校验，解码失败时回 C4 失败应答。

验收：编码 255 分拒绝；合法值往返保真；如实施解码范围硬化，金额字节 251–255（2510–2550 分）拒绝，字节 0、25、250 分别正确得到 0、250、2500 分；测试 C6 解析失败仍记录事件并保持连接。协议测试不替代实机验收。

### F03 人工退款入口不显示（原 2.2）

**证据：源码与引用核对确认；未执行组件交互。首轮并行恢复功能。**

[OrderView](../backend/internal/order/order_view_repo.go)的 `RefundApplicantID` 标为 `gorm:"-"`，当前未找到赋值；[QueryService.Detail](../backend/internal/order/query.go)也未回填。[OrderDetailsDrawer](../frontend/admin-web/src/pages/orders/OrderDetailsDrawer.tsx)第 248 行以该字段非空为渲染条件，入口无法显示。

修复：按实时权限回填当前管理员的十进制字符串 ID，保留现有字段契约。原 2.3 所称 null 草稿串号当前不可达，因为 `ManualRefund` 未创建；转为入口恢复后的回归项。

**方案可行；后端回填有效，前端响应保护还需落实。** 可在 `QueryService.Detail` 完成 order.read 校验后，按 `actor.HasPermission("order.refund.create")` 回填 `strconv.FormatUint(actor.ID, 10)`；无创建权限保留 nil。现有父组件已把该字段传给 actorId，不必为显示入口另造身份来源。后端退款服务继续从实时审计身份取得 ActorID，不接受客户端替代身份。

[ManualRefund](../frontend/admin-web/src/pages/ManualRefund.tsx)提交前检查 epoch，但自己的 catch／finally 没有再次检查；当前组件 key 只有 order_id。同订单切换账号时应重置组件身份，并在响应、错误及清理回写前核对请求 epoch／组件代际。[API 客户端](../frontend/admin-web/src/api/client.ts)已有成功响应 epoch 检查与刷新保护，但不能替代组件错误回写保护。恢复入口后，这些路径将实际可达。

验收：有权限可见、无权限不可见；两管理员草稿隔离；网络中断／响应丢失复用原请求；会话切换后旧响应不能污染新会话；重放及双人审核约束保持。

人工退款回执在 [RefundReviewRepo.ManualCreate](../backend/internal/settlement/refund_review_repo.go)；[manual_repo.go](../backend/internal/settlement/manual_repo.go)处理人工最终金额，两者不能混用。

### F04 代理后的客户端 IP 配置不匹配（原 2.4）

**证据：源码、部署配置与内存请求确认；未查询生产日志。**

[NewRouter](../backend/internal/platform/httpapi/gin.go)使用 `SetTrustedProxies(nil)`，[Caddy](../docker/Caddyfile)反代 admin。内存请求带 `X-Forwarded-For` 仍取得代理地址 `172.18.0.9`；在该拓扑下，IP 限流与审计来源聚合到代理地址。

修复：配置明确可信代理，核对 admin 配置加载、cmd 使用、Compose 注入、模板和部署网络边界。仅改「IP＋用户名」不能恢复审计来源，也不能替代来源 IP 限流。

**方案可行，须完成装配才能有效。** 当前 [config.Admin](../backend/internal/platform/config/config.go)没有可信代理字段，[admin newRouter](../backend/cmd/admin/routes.go)直接使用通用 NewRouter。增加 admin 专属配置，在启动注册路由时应用并检查 SetTrustedProxies 的错误；配置错误启动失败，空配置保留不信任转发头。生产 admin 仅 expose、开发绑定回环的边界见 [生产 Compose](../docker/compose.prod.yaml)与[开发 Compose](../docker/compose.dev.yaml)。明确可信 Caddy 地址／网段与网络隔离，不能直接信任所有私网或整个共享容器网段。

验收：可信代理请求正确取客户端 IP；非可信来源伪造转发头不改变身份；不同客户端限流独立。部署后验证生产审计来源。

### F05 TOTP 登录未消费验证码时间步（原 3.1）

**证据：源码确认；尚未执行并发消费验证。**

[CompleteMFA](../backend/internal/ops/account_repo.go)只消费挑战，未记录已使用的时间步，新挑战仍可能接受同一步验证码。[MFA 设置](../backend/internal/ops/mfa.go)的启用确认已原子消费 pending，禁用会清密钥并提升认证版本，不能宣称这两个动作可无限重复。

修复：[VerifyTOTP](../backend/internal/platform/auth/totp.go)返回命中的时间步，按账号与密钥版本原子记录消费；覆盖登录及需要验证码证明的设置／管理确认路径。明确启用确认后立即登录的等待及提示。

**方案可行，但需补齐消费事务与密钥代际定义。** 三类操作已持有 admin_user 行锁，可沿用这些事务，将成功时间步消费与账号安全状态／审计一起提交。返回命中步只完成验证，不能单独防重放；所有 VerifyTOTP 调用点和测试必须同步核对。

密钥代际不能直接用 AuthVersion：改密／强制下线会提升认证版本，却未必换密钥，不能据此清空消费记录并再次接受同一步。仅换密钥时创建新的消费代际。若使用「最后消费时间步」，在锁内拒绝小于或等于该步的输入；保留完整容差扫描与恒定时间比较，规定多个时间步恰好生成相同六位码时的确定性匹配策略。保留 Redis 挑战／pending 消费失败不签发会话，以及消费后 SQL 失败需要重新挑战的现有恢复语义。

验收：不同挑战不能复用同一码，并发仅一个成功；新时间步、允许时钟偏差及密钥更新正常；失败不签发会话，不破坏挑战与版本约束。

### F06 异常记录与释放失败阻断批处理推进（原 2.1）

**证据：源码确认；尚未执行真库批处理验证。**

[批查询](../backend/internal/gateway/repo/worker_sync_repo.go)固定读取前 50 条；[启动同步](../backend/internal/worker/charge/sync.go)和[结束同步](../backend/internal/worker/charge/end_sync.go)存在跳过异常但未标记的路径。这类 continue 路径中，一条异常不会阻断全部，前 50 条都无法完成时后续持续饥饿。`254` 可由[正常启动补偿](../backend/internal/gateway/repo/compensation_repo.go)产生，不能只当作脏数据。

**公平扫描方案有条件可行，还必须覆盖释放失败提前退出。** end_sync 第 106–111 行在 ReleasePort 失败或 Released=false 时立即返回，后续记录不处理，此前成功项也未到批量标记步骤。一条持久释放失败就可能阻断本批后续，不能用「所有异常都只会 continue」解释。

修复：区分永久异常、暂时缺依赖与重试失败，增加公平扫描、退避及可恢复的人工核实状态。未知结果不能直接标成启动失败或已处理。

**两类缺陷需分开处理（第二轮补充）**

- **`continue` 类**（[sync.go:62-66](../backend/internal/worker/charge/sync.go) 与 [end_sync.go:58-104](../backend/internal/worker/charge/end_sync.go) 的跳过分支）：异常行不标记且排序键不变，逐步占满批窗口。公平扫描与退避解决后续记录饥饿；异常行自身仍需分类、依赖恢复或人工核实，不能据此将 `254` 等未知结果直接终结。
- **`return` 类**（[end_sync.go:106-111](../backend/internal/worker/charge/end_sync.go) 的 `ReleasePort` 失败）：直接返回，本批此前已完成结束确认和端口释放的事件也未写入 `processed` 标记，下一轮会再次提交 central。**只有 central 确认且端口释放确认的成功项才进入批量标记；释放失败或结果未知的项不标记并独立退避，其失败不得阻断其他独立事件。** central 确认仅表示结束请求被接收，不表示计费、扣款或退款已经完成。

失败／重试状态应由 gateway 持久化，worker 经具名接口更新，不依赖进程内游标跨重启维持公平性；需要同步查询选择条件与 contracts／integration，而不是只改 worker 循环。投递重试／人工核实元数据不能直接覆盖 START 命令或端口的业务状态。已获 central 确认但释放失败时，保留冻结的结束请求，独立退避后继续其他事件；仅两项均确认的成功项标记，释放失败或结果未知的项不标 processed。不得重建请求或提前释放其他订单占用的端口；请求 context 取消时仍应停止处理。

验收：前 50 条异常时后续仍推进；一条释放失败不阻断其他独立事件；服务恢复及 worker 重启后仍可继续；未知结果不产生「无充电证明」；冻结结束请求、端口释放和处理标记重放幂等。

### F07 历史异常持续阻断零功率新窗口（原 3.4、11.1）

**证据：源码与最小复现确认。原「完全撤销」判断过度，应保留恢复问题。**

[evidence](../backend/internal/charging/stop/gateway.go)每轮从订单 `StartedAt` 读取完整历史；[心跳查询](../backend/internal/gateway/repo/telemetry_curve_repo.go)按入库 ID 排序，[collectChargingSamples](../backend/internal/gateway/control/charging_samples.go)返回原始 `ReceivedAt`。[noPowerForMinute](../backend/internal/charging/stop/meter.go)第 72–73 行遇重复／乱序立即 false，旧异常不会随轮次退出历史。

最小复现：两小时前的重复或乱序，后接距当前 70、55、40、25、10、2 秒的零功率心跳，整段历史 false，后续独立窗口 true。duplicate time 测试只证明异常窗口拒绝，不能证明后续恢复。问题限于零功率分支，不表示时长、预算或用户停机全部失效；未验证实机故障率。

修复：保留异常不能证明连续时间的规则，明确独立新窗口恢复策略；重新证明完整一分钟，保留间隔上限和末次新鲜度，禁止继承异常前累计。原建议「跳过异常并保留累计」仍不可采纳。

**方案可行，纯函数原型已验证；须定义异常后的时间边界。**

- 查询本就不读取 StartedAt 之前的历史，无需增加所谓预读限制。翻页游标仅单轮使用；可以保持现有查询与纯函数接口，在全量输入中建立新的连续后缀，不依赖跨轮缓存。
- 重复／乱序时清空 since，但保留异常前已接受的最大时间；异常样本不作为新窗口起点。后续样本必须严格晚于该时间才重新累计，不能排序、去重后拼接异常前后，也不能用乱序样本向后挪动时间基准。
- 前次 overlay 原型采用上述规则，未来／零时间仍返回 false；缺端口、正功率、超过 MaxChargingEvidenceGap 的间隔重置累计，最终仍检查末次新鲜度及完整一分钟。现有两个零功率测试函数与四个新增恢复子场景均通过。
- 「异常当轮不得误停」指尚未重新获得完整证据的候选窗口，不是任何扫描只要读到历史异常都必须 false，否则再次形成永久阻断。
- `stopOrder` 的预算判定也使用同一批全量 samples 生成 measuredMeter。不要为零功率恢复全局缩短 StartedAt 查询窗口，导致预算分段缺失；前次原型未改变查询、预算、截止或 STOP 路径。

验收：旧重复／乱序后的完整新窗口按明确规则恢复；异常当轮、缺端口、长间隔、未来时间及陈旧末次不能误停；截止首写与 STOP 重试幂等保持。

### F08 旧派发覆盖人工金额重新排队（原 11.2）

**证据：源码可达的恢复竞态；尚未执行真库交错验证。**

[BillingAPI](../backend/internal/http/central/billing_service.go)允许合法请求直接调用 `Run`；[Due](../backend/internal/settlement/billing_job_repo.go)读取 pending 但不认领；`Defer` 第 40–41 行仅排除 done。[BillingService.Run](../backend/internal/settlement/billing.go)根据此前读取的来源决定转人工。

触发交错：

1. 派发 A、B 都读取同一 pending 订单及计量不足的来源（[Due](../backend/internal/settlement/billing_job_repo.go) 第 33–37 行读取但不认领）。
2. A 将任务改为 manual_review（[Run 第 68 行](../backend/internal/settlement/billing.go) 的 `Defer`）。
3. 财务通过 [ResolveAmount](../backend/internal/settlement/manual_repo.go) 登记人工金额（第 67 行重置为 `status='pending'`），尚未入账。
4. B 同样判定需人工核实，调用 [`EnqueueManualFeeReview`](../backend/internal/settlement/billing_job_repo.go)（第 346–353 行）——该写入是 `DoUpdates` 自赋值 no-op upsert，**B 会成功**，随后 B 的 `Defer(ctx, id, "manual_review", ...)`（[第 40–42 行](../backend/internal/settlement/billing_job_repo.go)，仅排除 `done`）覆盖第 3 步的重新排队。
5. [`Due`](../backend/internal/settlement/billing_job_repo.go) 不再选择该任务；同人工请求重放因已有记录直接成功（[ResolveAmount 第 42–47 行](../backend/internal/settlement/manual_repo.go)），不重新排队。

**路径修正说明（新增）**：原描述为「B 执行 `Defer` 覆盖重新排队」而未交代前置步骤。实际覆盖成立的前提是 B 先成功完成 `EnqueueManualFeeReview`（no-op upsert 不报错），再进入 `Defer`。结论不变，但实现时应同时关注这两步。

单 worker 轮次串行降低发生机会，但多 worker 或运维与 worker 并发派发可达。影响是人工金额已登记而结算停止推进；无重复扣款证据。此问题独立于原 3.3 的续购上限指控。

修复：任务认领版本／CAS，或在任务锁内重查来源与人工决策后更新。仅加 `status='pending'` 不足以区分确认前后的两代 pending。不得删除／覆盖 `fee_receipt` 绕过来源一致性。

**两种方案均有条件可行，当前说明不足以直接编码。**

- 采用代际／CAS 时，Due 必须返回读取的代际，人工最终金额与[计量审批重新排队](../backend/internal/settlement/meter_review_repo.go)均推进代际。所有 Defer，包括 Run 第 92 行的 pending 兜底，都须带预期代际并核对 RowsAffected。失配表示旧执行失效，应跳过，不能再进入无条件 pending 兜底。
- 人工队列入队与 manual_review 转状态应在同一短事务中校验代际／当前来源；不能只保护最后 Defer，却让旧请求先修改人工队列。
- 更小的方案是新增 settlement 仓储操作：短事务锁 job，用当前读核对人工金额／批准计量及来源，仅在当前仍需核实且未被新决策替代时入队并转人工状态。发现新决策就保留重新排队，交给新轮执行。一般失败的 pending 更新也不能撤销 manual_review／done 或覆盖新一代重排。
- 上述重读须采用实际当前读或明确 RC 事务，重新执行纯规则 PriceSource；不能只加锁后继续使用旧 Source，或用普通 RR 旧快照。状态更新／入队失败必须传播或汇总；现有 Run 忽略 Defer 错误的路径不能被新实现继续用来隐藏恢复元数据写入失败。
- 任务锁方案限于失败状态转换，不要把 job 锁跨越整个 Calculate／Apply；Apply 已有钱包／支付→订单→任务锁序，扩大持锁范围可能引入反向等待。任一方案都保留费用回执来源一致性。

验收：独立真库连接与确定性闸门暂停 B，完成 A 转人工及人工金额登记，再放行 B；旧执行不能撤销重新排队，最终只有一份入账回执，重放幂等。

## 3. 已撤销的指控

| 原条目／指控 | 最终判断与边界 |
| --- | --- |
| 2.3 当前跨管理员草稿串号 | 入口未渲染，null 键路径未执行。字段原意为当前操作人；作为 F03 回归项，不认定已发生资金串号。 |
| 3.3 结算期间继续续购改变上限 | [结算来源](../backend/internal/settlement/billing_job_repo.go)要求订单已结束；[续购](../backend/internal/card/card_repo.go)经[会话／订单锁](../backend/internal/order/card_session_repo.go)只接受 charging。所述交错不可达，不代表 F08 等其他时序安全。 |
| 3.8 钱包 N+1／充值执行邀请资格 COUNT | [充值回调](../backend/internal/payment/payment_callback_repo.go)使用 first_recharge；[资格检查](../backend/internal/marketing/activity_grant.go)仅在 invite_reward 调用。[钱包 Move](../backend/internal/wallet/wallet_ledger_repo.go)是单笔 UPDATE＋INSERT，无所述查询循环。 |
| 3.9 缺月份无法使用已读主键 | [DDL](../backend/migrations/central_db/0001_init.sql)主键为 `(bill_id,created_month)`，bill_id 是左前缀；该表无实际分区。计划仍须 EXPLAIN；死变量／注释另行清理。 |
| 4.4 map 值副本共享状态 | [value](../backend/internal/gateway/control/telemetry.go)返回独立局部值符合语义；`&values[key]` 不能编译。 |
| 4.9 TOTP URI 空格未编码 | [TOTPURI](../backend/internal/platform/auth/totp.go)使用 URL.String，实测空格为 `%20`。结论限于所述编码指控。 |
| Redis 限流故障放行 | [会话服务](../backend/internal/ops/session.go)传递存储错误，HTTP 登录路径返回 503，属于故障拒绝。 |
| Profile 出错仍向 issue 传 nil | [Refresh](../backend/internal/ops/session.go)错误时提前返回，所述路径不可达。 |

## 4. 风险、产品决策与维护建议

### 4.1 需要测量或明确规则

| 原条目 | 最终判断与处理 |
| --- | --- |
| 3.2 深分页 | 已有页码 100 万、每页 100 的[后台上限](../backend/internal/platform/httpapi/gin.go)，「无上限」误报。深 OFFSET 成本是风险，未证明单请求拖垮进程。先测执行计划、规模及连接等待，再定限制／游标改造。 |
| 3.5 Live 与错误并存 | [Fill](../backend/internal/charging/query/live.go)清旧视图，依赖失败未填旧费用；[测试](../backend/internal/charging/query/live_test.go)要求保留有效计量。改善计量可用／费用不可用层级和两端文案，保留观测。 |
| 3.7 导出长事务 | [Generate](../backend/internal/export/generation.go)事务内取数、编码、发布；[lockedActor](../backend/internal/export/service.go)锁同一 customer_admin 角色，跨管理员可串行等待。耗时、吞吐影响和死锁未测量。先测大 CSV/XLSX、慢存储与锁等待，再安全拆分。 |
| MFA 管理员代登记预填 | [自助响应](../backend/internal/ops/mfa.go)无 code，但[管理服务](../backend/internal/ops/account_management.go)、[接口](../backend/internal/http/admin/account/admin_accounts.go)返回 code，[Users](../frontend/admin-web/src/pages/Users.tsx)预填。管理权限与禁止本人操作限制存在，不能仅凭回传认定登录绕过。明确目标账号本人验证器持有证明要求，统一行为与文案。 |

导出拆分须建立生成所有权和独立临时产物，事务外编码，最终短事务重查权限、范围／脱敏、过期、任务状态与快照。范围或脱敏规则与编码时不同，应丢弃产物并拒绝／重生成；核验到发布仍须保护这些规则，不能只确认 export.create 后发布旧文件。验收覆盖权限撤销、取消、过期、同任务恢复、提交结果未知及文件清理。

**源码级核验：方向可行，但须先形成完整生成协议，不能直接搬出事务。** [artifact](../backend/internal/export/artifact.go)按任务号使用同名 `.partial`、最终文件与 proof，RequestedBy 只证明申请人，不证明当前生成执行所有权。同一申请人的并发恢复仍会竞争，须增加生成 token／代际、领取及失效恢复；临时产物隔离，最终提交和清理均核对执行所有权。

提交结果未知时，重新锁读任务：确认 completed 产物属于本次 token 则保留并成功；确认未提交且仍属自己才清理／转失败；数据库仍不可读则保留文件和恢复状态，不能按任务号整体删除新执行的产物。同步核对命名、expectedPath、proof、下载和过期清理；保留 [generationTransaction](../backend/internal/export/transaction.go)在取消后不提前释放保护文件的事务锁语义。权限范围变化时直接拒绝／重生成是保守可行方案；若允许权限扩大后复用，须按现有 containsScope 等规则另行证明安全，不能只比较权限字符串。

### 4.2 小范围维护

| 原条目 | 处理结论 |
| --- | --- |
| 3.9 months／注释 | [ListBills](../backend/internal/settlement/bill_repo.go)删死变量、纠正寻址说明；若查复合键，不能把 IssuedAt 当月首键。 |
| 4.1 协议字面量 | 随协议维护整理常量，无确认行为缺陷，不声称固件错位。 |
| 4.2 设备金额 | [AmountCents](../backend/internal/protocol/dc589/measurements.go)保留诊断信息，注明不参与平台结算。 |
| 4.3 鉴权收敛 | 可减少重复，未发现由此漏鉴权，不扩大为鉴权重构。 |
| 4.5 时钟注入 | 未证明故障；测试时钟与真实 socket deadline 分别设计，不机械替换。 |
| 4.6 默认隔离 | [DSN](../backend/internal/platform/dbconn/mysql.go)未固定默认隔离，可作为硬化候选；关键事务已有显式 RC，不能据 RC 推断 FOR UPDATE 失效或统一强改资金隔离。 |
| 4.7 fallback 上限 | 当前传入 10／20，未发现越界，仅属防御性建议。 |
| 4.8 错误映射 | 随维护整理，未确认具体误映射案例。 |
| 4.10 pending 残留 | 坏密码／坏码保留短期 pending 支持重试，成功消费／清理，TTL 限寿命。未确认缺陷，不采用失败即清理。 |
| 4.11 空闲连接 | 未测得频繁建连／吞吐瓶颈，有测量收益时调参。 |
| 4.12 Fees JSON | [Fees](../backend/internal/settlement/money_schema.go)已有三个顶层字段的小结构体，不构造完整 Result；先测 JSON 大小、CPU 和分配。 |
| 4.13 VALUES 弃用 | [参数化 SQL](../backend/internal/settlement/invoice_repo.go)无注入证据，弃用兼容性问题成立。最小替换 `reject_reason=VALUES(reject_reason)` 为 `reject_reason=?` 并重复绑定理由。驱动 OnConflict 仍可能生成 VALUES，通用替换不保证消除弃用。 |
| 4.14 公式注入 | [编码](../backend/internal/export/format.go)已有 [SafeSpreadsheetCell](../backend/internal/domain/operations/export.go)，测试覆盖 `=`、`+`、`-`、`@` 四类非数字开头。无需重复转义；未验证所有软件／导入变体。 |
| 4.15 STOP 来源 | [DTO](../backend/internal/contracts/gateway/charging.go)接收 Source，但[处理器](../backend/internal/gateway/control/user_stop.go)未传给用例／持久化命令。补诊断记录；截止原因不能完整替代来源。 |

## 5. 修复顺序、归属与验收

按下表先后轮次推进，同轮按文件归属并行，分别实施和评审。详细验收以第 2、4 节为准，不混成一个大改动。

| 轮次 | 工作项与归属 | 交付与验证 |
| --- | --- | --- |
| **1：金额与不可用入口** | F01：settlement；F02：protocol/dc589；F03：order 查询、admin 适配与 admin-web。三项独立并行。 | 支付读取失败阻止出账／事件，保留已入账计数与补账；编码拒绝不可表示金额，解码硬化独立验收；入口按权限显示，组件身份与异步回写保护保持。 |
| **2：鉴权与来源** | F04：admin 配置、cmd、Compose、模板及网络边界；F05：platform/auth、ops 与账号数据定义。 | 正确取得客户端 IP，拒绝伪造头；按账号／密钥版本原子消费 TOTP，验证并发与新挑战重放。 |
| **2：任务恢复** | F06：gateway/repo、worker/charge、contracts/integration；F07：charging/stop 与 gateway 证据契约；F08：settlement 任务及人工金额恢复。与鉴权项按归属并行。 | 50 条异常不饿死后续；独立零功率窗口恢复且不误停；旧派发不能覆盖重新排队，真库验证单份入账。 |
| **3：测量与优化** | 深分页接口、export、运维文档。 | 给出执行计划／锁等待及前后性能数据；权限、范围／脱敏、所有权、恢复和清理保持。 |
| **独立决策** | 管理员代登记 MFA：ops、后台账号管理与产品规则（含 [Users 页面](../frontend/admin-web/src/pages/Users.tsx) 预填文案）。 | 先定持有证明要求，再改接口／文案，保持重置权限契约。 |
| **随维护处理** | months／注释、设备金额说明、弃用 SQL、STOP 来源等。 | 小范围修改并核对所属契约；无热点证据的连接池／JSON 优化不提前实施。 |

**并行时的文件归属提醒（新增）**

- F03 须联合验收后端字段、[`OrderDetailsDrawer.tsx`](../frontend/admin-web/src/pages/orders/OrderDetailsDrawer.tsx)与[`ManualRefund.tsx`](../frontend/admin-web/src/pages/ManualRefund.tsx)；已有 actorId 传参可复用，不要求为联调机械修改两个文件，但身份重置与异步回写保护须同批解决。
- 「独立决策」的 `Users.tsx` 虽同属 admin-web，但与 F03 的两个文件无重叠，可与批次 1 并行。

实现与验证入口：

- 后端默认检查：`node tasks.mjs check`。
- 锁、幂等与持久恢复：`node scripts/db/test-integration.mjs`。先核对开发库夹具、并发和清理边界；关键交错使用独立连接与确定性闸门。
- 前端：各工程 README 所列 test／lint／build，补退款入口、草稿隔离和会话切换的组件或端到端场景。
- 行为／运行方式改变时同步[业务契约](business-contracts.md)和[运维文档](operations.md)；数据库定义按所属 `0001_init.sql` 维护。

默认测试、mock 或静态推演不能替代真库、生产代理、实机及真实支付验收。资金修复保留锁序、首写快照、回执一致性和未知结果恢复语义。

## 6. 已执行验证与覆盖缺口

以下保留历次复核记录，并补充最新 F06 方案验证。Windows／PowerShell 下缓存和临时目录为 `backend/.tmp/go-cache`、`backend/.tmp/go-tmp`；旧轮测试不记作本轮重跑。

| 验证 | 命令／方式 | 结果与边界 |
| --- | --- | --- |
| 第一轮：7 个 Go 包 | backend：`go test ./internal/charging/stop ./internal/charging/query ./internal/protocol/dc589 ./internal/platform/httpapi ./internal/platform/auth ./internal/worker/charge ./internal/ops` | stop、query、auth、ops 首次通过；其余受回环连接沙箱限制。解除限制后 `go test ./internal/protocol/dc589 ./internal/platform/httpapi ./internal/worker/charge` 重跑通过。未启用 integration tag，不是全仓检查。 |
| 第一轮：导出格式 | backend：`go test ./internal/export -run '^TestExportFormats$' -count=1` | 内存 CSV/XLSX 编码及读取通过，不是整个导出包／并发导出验收。 |
| 第一轮：前端 | admin-web：`node --test tests/orderPresentation.test.mjs tests/session.test.mjs` | 16 项通过，不是组件渲染／浏览器 E2E。 |
| 第一轮：金额 | BuildSetConfig／DecodeConfig，255 分往返。 | 编码成功却回读 250 分，确认 F02；未连接设备／支付。 |
| 第一轮：URI | TOTPURI 的 issuer／account 含空格后解析 URL。 | EscapedPath 空格为 `%20`，支持撤销所述编码缺失。 |
| 第一轮：代理 IP | NewRouter 内存请求设代理 RemoteAddr 与 X-Forwarded-For。 | ClientIP 为 `172.18.0.9`，确认 F04；未查生产日志。 |
| 第一轮：字段引用 | backend：`gopls references internal/order/order_view_repo.go:46:2`，结合文本检索、调用链与前端门控。 | 无 RefundApplicantID 引用／赋值，支持 F03；不证明 null 草稿键执行。 |
| 第二轮：零功率恢复 | backend：临时 overlay 执行 `go test -overlay=<临时映射文件> ./internal/charging/stop -run '^TestReviewHistoricalAnomalyAndLaterCleanWindow$' -v -count=1`。 | 两子用例通过复现断言：旧重复／乱序使整段历史 false，新窗口 true。原测试／源码未改，临时文件已删除。 |
| 第二轮：重新排队 | 追踪 HTTP、Due／Read／Calculate／Defer、ResolveAmount 与重放。 | F08 静态交错可达，尚无真库执行证据。 |
| 前次：配置方案 | 临时 Go 程序调用实际 EncodeConfig／DecodeConfig，穷举金额字节 0–255，并分别调用 Validate、ParseConfigAck。 | 解码非 10 分整数倍数量为 0；字节 255→2550 分，当前解码成功但 Validate 拒绝越界；编码 255 分当前仍成功；C4 解析器不接受 C6。程序已删除。 |
| 前次：零功率方案 | 临时 overlay 替换判定分支并追加用例，命令见下方。 | 现有两个测试函数及四个新增场景通过：旧重复／乱序后恢复、近期重复不误停、历史未来仍拒绝。只证明纯函数方案，业务源码未改，未验证真库、预算或 STOP 链路。临时文件已删除。 |
| 本次：结束批处理 | 临时 overlay 增加两个不同订单的事件，先测当前源码，再仅将释放失败的提前返回改为记录首错后继续；命令见下方。 | 当前源码的批首失败、批尾失败、Released=false 三场景均不能满足其他成功项正常标记的断言；修改分支的原型中，三场景及原有三个 EndSync 测试函数全部通过，失败项均未标记。只证明批内隔离及标记前提，不覆盖持久退避、公平查询、真实端口或真库恢复。临时文件已删除。 |

前次零功率 overlay 命令在 backend 执行，映射与测试均为临时文件，未替换工作区源码：

```powershell
go test "-overlay=$overlayPath" ./internal/charging/stop -run '^Test(NoPower|ReviewProposedRecovery)' -v -count=1
```

本次 F06 对照命令同样在 backend 执行：第一条针对未修改的源码预期失败，以复现缺陷；第二条针对临时修改的分支通过。两份映射均不改工作区源码。

```powershell
go test "-overlay=$probeRoot/current.json" ./internal/worker/charge -run '^TestReviewReleaseFailurePreservesOtherEvents$' -v -count=1
go test "-overlay=$probeRoot/proposed.json" ./internal/worker/charge -run '^Test(EndSync|ReviewReleaseFailurePreservesOtherEvents)' -v -count=1
```

已有测试应复用，新增范围需明确：

| 已有覆盖 | 尚需补充 |
| --- | --- |
| [截止首写／恢复](../backend/internal/charging/stop/recovery_integration_test.go)：独立连接、首次快照保持。 | 用户与自动停机真实入口交错；历史异常后新窗口恢复。 |
| [充值请求幂等](../backend/internal/settlement/recharge_request_integration_test.go)。 | 按修复影响增补，不认定充值幂等完全无覆盖。 |
| [人工退款并发](../backend/internal/settlement/refund_review_integration_test.go)：累计额度约束。 | 入口、草稿、会话切换与双人审核 UI 编排。 |
| [会话](../frontend/admin-web/tests/session.test.mjs)和[订单展示](../frontend/admin-web/tests/orderPresentation.test.mjs)：慢响应、登录切换、零值与状态映射。 | 具体组件和跨浏览器标签页场景。 |
| [公式转义](../backend/internal/export/format_test.go)：CSV/XLSX 编码读取。 | 扩大软件／导入变体保证时，另定支持范围并验证。 |
| [人工决策当前读](../backend/internal/settlement/review_current_read_integration_test.go)与[并发计算回执](../backend/internal/settlement/fee_calculation_integration_test.go)。 | F08「旧派发暂停→人工重新排队→旧派发继续」完整交错。 |

未完成的外部验证仍包括真库／Redis、生产 IP 与隔离查询、性能、浏览器、实机及真实支付。原全仓「无问题」清单不作为本报告重新认证的结果；有限复核不能推出全仓无注入、越权、竞态或凭据风险。

## 7. 规则依据

仓库行为以本地实现为准。以下资料解释规则，不替代可达路径及执行证据：

- [Gin 可信代理](https://gin-gonic.com/en/docs/server-config/trusted-proxies/)。
- [RFC 6238 §5.2：成功验证后不再次接受同一 OTP](https://www.rfc-editor.org/rfc/rfc6238#section-5.2)。
- [MySQL 复合索引左前缀](https://dev.mysql.com/doc/refman/8.4/en/multiple-column-indexes.html)。
- [MySQL INSERT ON DUPLICATE KEY UPDATE 与 VALUES 弃用](https://dev.mysql.com/doc/refman/8.4/en/insert-on-duplicate.html)。

## 8. 方案可行性结论

历次逐项核对第 2 节修改落点，并核验第 4 节主要优化边界。以下「正确／有效」均限于所述问题，不是已经实施或全链路验收。

| 工作项 | 可实施性 | 能否解决问题 | 正确性与剩余条件 |
| --- | --- | --- | --- |
| F01 出账吞错 | 可直接实施小范围修复。 | 支付读取失败在 Issue 中返回即可阻止错账。 | 保留资金投递计数、错误汇总与同轮补账；持续故障／即时恢复分别验收，尚未故障注入。 |
| F02 金额精度 | 编码校验可直接实施；解码硬化独立安排。 | `%10` 消除编码截断。 | 「解码拒绝 255 分」不可执行；改为越界字节验收，正确区分 C4/C6。真实上报兼容性未验证。 |
| F03 退款入口 | 复用现有字段与 actorId 可实施。 | 后端按创建权限回填恢复入口。 | 前端组件身份、错误／清理回写仍需保护，不能仅补字段就声称会话验收通过。 |
| F04 可信代理 | 需新增配置与路由装配。 | 正确可信代理边界可恢复来源识别。 | 核对配置错误、动态容器地址及网络隔离；生产验证尚未完成。 |
| F05 TOTP 消费 | 当前账号行锁提供实现基础。 | 验证与持久化消费同事务才能防重放。 | 需定义独立密钥代际、匹配策略及各调用点，不能以 AuthVersion 重置同密钥消费历史。 |
| F06 批处理推进 | 批内隔离可局部修改；公平退避需扩展 gateway 元数据、查询和接口。 | 释放失败隔离及成功项标记已有局部原型证据；公平选择与持久退避仍待实施。 | central 与端口释放均确认才 processed；不覆盖业务状态，保持冻结回执与重启恢复；尚未真库验证。 |
| F07 零功率恢复 | 现有纯函数内可实施，局部原型已通过。 | 新连续后缀可恢复旧重复／乱序后的判定。 | 保留时间基准与证据规则，避免缩短预算查询；纯函数通过不等于设备／STOP 验收。 |
| F08 计费重新排队 | CAS 或短任务锁方案均可实施，但须先选定完整状态协议。 | 全部状态写入与人工决策代际一致才有效。 | 入队与转状态原子；失配不能再无条件 pending；覆盖两类人工重排，保持锁序与回执一致性。 |
| 深分页／导出 | 可先测量，拆分需额外协议设计。 | 未测量前不能承诺性能收益。 | 导出所有权、未知提交恢复、权限与清理闭合后才可实施事务拆分。 |

**实施判断**：F01 和 F02 编码修复的落点已明确；F03、F04 必须完成装配与边界保护；F05、F06、F08 仍需把状态／代际协议具体化；F07 有局部执行证据。报告不再把这些不同证据层级统称为「方案已验证通过」。

### 8.1 最新方案审查的源码级确认

**主要修复方向可行，不能认定新增论断全部正确或完整方案已验证通过。** 本次确认了 F06 的提前返回问题，并用临时 overlay 验证批内隔离方向有效；同时纠正 processed 前提、F05 引用和 F01 验收范围。F08 与导出保留的协议约束未发现新的明确缺口，但完整实现与真库交错仍未验证。

| 新增论断／方案 | 本次判断 | 源码依据与修正 |
| --- | --- | --- |
| F06 释放失败提前退出 | 成立，局部修复有效。 | [end_sync.go](../backend/internal/worker/charge/end_sync.go)第 106–111 行返回，跳过第 116–120 行标记。对照原型中，批首／批尾释放失败及 Released=false 三场景均能继续处理其他订单，只标记成功项；见第 6 节。 |
| F06 central 确认即可标记、已完成结算丢失标记 | 表述不正确，已修正。 | 同文件第 37 行明确确认不代表计费完成；第 113 行仅在 central 与释放都成功后追加 ID。释放可明确失败（[worker_sync.go](../backend/internal/gateway/control/worker_sync.go)第 166 行返回 409），也可结果未知，两类都不能 processed。此前成功项是尚未写入标记，不是已提交的标记丢失。 |
| F06 `continue` 类只需公平扫描 | 不完整，已补齐约束。 | [worker_sync_repo.go](../backend/internal/gateway/repo/worker_sync_repo.go)固定窗口造成后续饥饿；公平扫描只使其他记录推进，不使异常行自行恢复。仍需 gateway 持久分类／退避、人工核实及重启恢复，不能直接终结未知业务结果。 |
| F03 前端 epoch 保护不足 | 成立。 | [client.ts](../frontend/admin-web/src/api/client.ts)第 41–62 行已有请求／成功响应检查；[ManualRefund.tsx](../frontend/admin-web/src/pages/ManualRefund.tsx)第 32–35 行 catch／finally 未复核组件身份。入口恢复时须同步保护这些回写。 |
| F04 配置须完成装配 | 成立。 | [config.Admin](../backend/internal/platform/config/config.go)无可信代理字段；[admin/routes.go](../backend/cmd/admin/routes.go)第 46 行直接调用通用 NewRouter。需配置加载、启动错误检查及部署注入共同生效。 |
| F05 登录、自助和管理确认已有账号锁 | 成立，提供事务落点。 | [CompleteMFA](../backend/internal/ops/account_repo.go)第 141 行直接 FOR UPDATE；[MFA 设置](../backend/internal/ops/mfa.go)第 118 行 LockActiveAdmin；[ManageMFA](../backend/internal/ops/account_management.go)第 306 行 LockManagementAccount，经[仓储](../backend/internal/ops/account_management_repo.go)第 148 行 FOR UPDATE。存在锁不等于已经消费时间步。 |
| F05 AuthVersion 不能作密钥代际 | 结论成立，原引用错误，已替换。 | [ChangePassword](../backend/internal/ops/account_repo.go)第 233–241 行只更新密码散列与 auth_version；[ResetManagementPassword](../backend/internal/ops/account_management_repo.go)第 136–156 行同样不更新密钥。原引 [mfa.go](../backend/internal/ops/mfa.go)第 177 行实际写入 pending.Secret，不能证明保留旧密钥。 |
| F02 解码侧 `%10` 与 C4/C6 区分 | 当前方案正确。 | [config.go](../backend/internal/protocol/dc589/config.go)第 208 行固定乘 10，不可能解码出 255 分；撤回的是解码精度验收，并不否定独立范围校验。调用全部 Validate 须另核兼容性。[adapter.go](../backend/internal/protocol/dc589/adapter.go)第 332–336 行 C6 DecodeConfig 与 C4 ParseConfigAck 分开。 |
| F01 同轮补账与「无账单」验收 | 同轮恢复正确，原验收批评须限定故障范围。 | [billing.go](../backend/internal/settlement/billing.go)第 81–85 行记录首个出账错误但保留投递计数，第 98 行继续补账。仅首次读取失败、随后恢复时，不能断言整个 Run 无账单；Issue 单次或读取持续失败时，无账单及事件是正确要求。 |

**实施条件**：F01、F02 编码修复可按既定落点实施；F03、F04 完成响应保护和配置装配；F06 批内隔离有执行证据，但公平退避仍须完成持久协议。F05、F08 和导出拆分应先具体化代际、原子转换、所有权及未知结果恢复，再做确定性故障／交错验收。F07 保留此前纯函数证据，不扩大为预算、STOP 或实机通过。本次业务源码未修改，也未重跑真库／Redis或全仓检查。

## 9. 实施结果与验收记录（2026-10-10）

本节对应用户授权的报告修复，代码与初始化结构已在工作区修改，未提交、推送或部署。使用当前源码、实际命名客户端、MySQL／Redis、前端组件事件处理器及 Compose 展开验证；下表优先于前文审查时点的实施状态。

| 项目 | 最终行为与落点 | 已执行证据 |
| --- | --- | --- |
| F01 | `BillRepo.Issue` 的关联支付查询失败或缺失直接返回包装错误，写账单前终止；调度费用投递计数与末尾 `Missing` 保持。 | [支付查询故障回归](../backend/internal/settlement/bill_payment_integration_test.go)：缺支付、持续读取故障、金额恢复、幂等账单／事件；正常派发、已有投递、仅 Missing 和首次故障后同轮恢复。后四项只在资金入账边界计数，其余计价、投递标记、出账与 Missing 使用真实 SQL。 |
| F02 | 配置校验拒绝非 10 分整数倍，保留 0～2500 分上限；没有扩大上报解码校验。 | [编码回归](../backend/internal/protocol/dc589/config_test.go)：合法值往返保真，1／255／2499 分等拒绝，越界拒绝。 |
| F03 | 订单详情按实时创建权限返回当前管理员十进制字符串 ID；组件 key 包含订单、申请人及会话；异步成功、catch、finally 都核对会话与组件代际。 | [订单权限真库回归](../backend/internal/order/query_integration_test.go)；[8 项组件事件回归](../frontend/admin-web/tests/manual-refund-interaction.test.mjs)：草稿隔离、网络失败同号重试、会话切换后旧成功／400／409／422／网络响应、卸载保护及当前成功清理。TSX 宿主执行真实处理器，未做浏览器 E2E。 |
| F04 | admin 配置加载可信代理，非法代理配置阻止路由装配；生产只信任 Caddy 单一固定 IPv4，Caddy 经专用内部网络别名访问 admin。 | [路由回归](../backend/cmd/admin/routes_test.go)覆盖可信代理两个客户端、非可信来源伪造头、空配置直连与非法值；配置加载回归；两套 Compose 模板展开与 IP／子网覆盖值传播；部署检查器 42 项通过。未在生产启动 Caddy。 |
| F05 | TOTP 返回真实命中步，全窗口比较，碰撞取最高步；登录、自助确认／关闭、管理员确认在账号锁内消费；密钥版本独立于认证版本。 | RFC／偏移时间步默认测试；同挑战、不同挑战并发真库＋Redis 仅一次成功会话／登录审计；认证版本变化不重用旧码、下一步有效、新密钥重置；注册 HTTP 启用→登录→关闭与重放拒绝。 |
| F06 | gateway 持久保存 START／END 投递退避，新增具名内部请求／回复及客户端；失败项分类进入核实或重试；首投真实就绪时间与到期重试共同排序；端口释放失败继续批内独立项。 | [51 项公平性真库测试](../backend/internal/gateway/repo/sync_retry_integration_test.go)：异常首批不阻塞后续、另一连接重建、旧到期重试不被新项挤掉、未知码及事件处理状态保持；首／尾释放失败及负确认默认测试；真实 HTTP 结束流程验证冻结载荷重放、中央已提交但响应丢失、新占用者不能被旧事件释放。 |
| F07 | 重复／乱序清空窗口并保留最大已接受时刻，后续独立完整一分钟可恢复；未来、零时间仍拒绝，原预算查询与截止／STOP 逻辑保持。 | 新增旧异常后恢复、近期重复不能提前累计、时间基准不能倒退、未来／零时间测试；既有停止和计量测试通过。没有实机零功率验收。 |
| F08 | Due 返回预期代际，失败重排只作用于同代际 pending；人工入队与状态转换短事务原子执行；人工金额和计量批准推进代际；旧执行失配停止，不无条件兜底。 | [两连接确定性计费交错](../backend/internal/settlement/billing_generation_integration_test.go)：旧 Read 后暂停→另一连接转人工→人工金额或双人计量批准重排→旧执行恢复；新 pending 与代际保持，后续真实 Calculate／Apply 重放仅一份计价、入账回执、余款退款。夹具和独立退款 Outbox 显式清理并断言零残留。 |

随维护完成：删除账单无用月份集合并纠正联合键注释；说明配置金额单位；发票驳回 SQL 用重复参数绑定替代手写 `VALUES(reject_reason)`；STOP 受理增加来源、订单与命令号诊断日志，来源不作为授权证据。驱动其他 OnConflict 仍可能使用 VALUES，未宣称全仓消除。

### 9.1 验证汇总

| 命令／范围 | 结果 |
| --- | --- |
| `node tasks.mjs check` | 最终全仓通过：格式、`go vet ./...`、61 个有默认测试的包通过。日志 `.tmp/all-tests-check.log`。 |
| `node tasks.mjs build-all` | 最终全仓通过：全部 `cmd` 入口构建。日志 `.tmp/all-tests-build.log`。 |
| `go test -tags=integration -count=1 ./internal/settlement ./internal/ops ./internal/order ./internal/gateway/repo ./internal/http/integration ./internal/worker/runtime`（backend，按既有入口设置三库与 Redis 环境） | 六个相关包通过，真实 MySQL／Redis；最终增加退款 Outbox 清理后另重跑两个计费交错子用例通过。 |
| admin-web `npm test`、`npm run lint`、`npm run build` | 86／86 测试通过、0 跳过；类型检查与 Vite 生产构建通过。现有 Ant Design 体积提示不阻止构建。日志 `.tmp/all-tests-admin-{test,lint,build}.log`。 |
| miniprogram `npm run typecheck`、`npm test`、`npm run build:h5`、`npm run build:weapp` | 89／89 测试通过、0 跳过；类型检查、H5 与微信小程序构建通过。按现有锁文件安装依赖，锁文件无变化；现有 Sass 弃用提示不影响退出码。日志 `.tmp/all-tests-miniprogram-*.log`。 |
| `node --test scripts/tasks.test.mjs scripts/ops/check-deploy.test.mjs scripts/db/provision-runtime.test.mjs` | 58／58 测试通过、0 跳过，含任务入口、运行账号和部署检查器的 42 项回归。日志 `.tmp/all-tests-scripts.log`。 |
| `docker compose --env-file .env.example -f docker/compose.dev.yaml config --format json`、生产同等命令 | 模板展开通过；生产 admin 无公网端口、Caddy 与 admin 专网配置及覆盖值传播核验通过。根 `.env` 不存在，不能代表真实生产配置。 |
| 本地 `caddy:2.11-alpine` 镜像，`docker run --rm --network none` 中 `caddy adapt --config /etc/caddy/Caddyfile --validate` | 通过；只读挂载当前配置，以 `PUBLIC_DOMAIN=example.test` 验证语法／适配。无端口、无网络，临时容器自动删除；现有格式提示不影响退出码。 |
| `node scripts/db/upgrade-design.mjs --review-fixes` | 补齐后预检 0 项。先备份，再只增加 MFA 两列、计费任务代际和投递退避表；开发数据与 Goose 版本保留。 |
| `node scripts/db/upgrade-design.mjs --apply`，随后无参数预检 | 用户要求全部测试通过后，按现有入口执行其余 39 项兼容更新；完成后预检 0 项。自动备份经三库及非空校验，保留业务数据与 Goose 版本。日志 `.tmp/all-tests-upgrade.log`、`.tmp/all-tests-schema-precheck.log`。 |
| `node scripts/db/test-integration.mjs` | **最终全仓通过**：`go test -tags=integration -count=1 ./...`，67 个有测试的包通过，0 个失败包；实际使用开发 MySQL／Redis。device、gateway/control、identity、marketing 及 export 均通过。日志 `.tmp/all-tests-integration.log`。 |

审查修复结构的备份为 `.tmp/db-design-backups/chargepilot-20261009T154332Z.sql.gz`；全量兼容更新前另备份 `.tmp/db-design-backups/chargepilot-20261009T162709Z.sql.gz`，验证为 49,542 字节并包含三库。首次全仓失败由旧开发库结构差异和营销固定夹具冲突导致，现已完成兼容更新及夹具隔离，无须重建开发库或删除既有营销数据。营销测试使用随机编号空间，仅限定本夹具的活动候选、统计私有券；业务时间、状态、金额门槛、排序与行锁仍执行生产查询。并发用例在提交前登记清理，并断言规则、模板、发券及幂等凭据零残留；只读审查未发现削弱核心断言。

一次新增测试清理顺序错误留下的私有订单，以及最终复核发现的退款 Outbox，已按核实主键与确定性事件身份清理，后续回归包含零残留检查。上述全仓通过是最终工作区的实际执行结果；没有以相关包通过代替全仓验证。

### 9.2 保留边界

- 深分页与导出长事务没有新增性能证据，本次未做推测性性能改写；仍按第 5 节的测量阶段处理。
- 管理员代登记 MFA 的验证码预填与持有证明规则仍属于独立产品决策，未擅自改变现有重置权限。
- Caddy 语法已在无网络临时容器验证；未执行真实生产启动、网络连通、来源 IP／限流／审计日志验收，模板和内存路由测试不能替代这些结果。
- 组件事件回归不等于浏览器／跨标签页 E2E；模拟或真库测试不等于实机、真实微信支付或第三方平台验收。
- 未审计或修正历史错账；`Missing` 仅补账单，不纠正已存在的错误金额。
