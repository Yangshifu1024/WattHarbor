# 状态与枚举类型收敛路线图

状态：P0、P1 与 P2 批次均已实施，并已通过**真实数据库**集成验证。P0 依据 2026-10-10 对 `backend/` 的全量盘点，覆盖三个数据库初始化文件、66 处 ENUM 列、227 处候选字符串字段与 111 个文件；P1 于实施前以当前 HEAD 的完整 import 图逐条复核了第 5.2 节的归属结论，修正结果见 8.3.1 节；P2 的三项决策与实施结论见 8.4 节。真库运行找出并修复的六个回归见 9.2 节。**前后 schema 与数据基线已执行**（三库 schema 逐字节一致、60 张表中 58 张数据逐字节一致），但接口响应层面的严格逐字节比较未完成，缺口原因与已取得证据见 9.3 节。

本文负责状态与枚举类型的记录维度、放置规则、依赖可行性、逐项清单、分批顺序与验收标准。经四轮复审修订，各项设计决定已就地写入正文对应章节；尚待核验的事实与待独立处理的缺陷分别记在第 10 节。仓库维护规则见 [AGENTS.md](../AGENTS.md)，代码边界见 [架构边界与组织约束](architecture-boundaries.md)。

## 1. 目标与范围

Go 层以裸 `string` 承载状态与分类取值，同一枚举常在多个文件重复声明，没有单一事实来源。数据库侧已有大量 ENUM 列，类型安全的下游已经建立，但有限集合同样存在于 VARCHAR 列与无数据库列的派生场景，因此**不能以「数据库已全部使用 ENUM」描述全部枚举语义字段**。本方案将 Go 层收敛为自定义字符串类型，同时保持对外 JSON 响应不变。

**范围内**：Go 侧枚举的类型化、常量组补齐、判定方法提取、重复声明合并、裸字面量收敛、目标包归属校正。

**范围外**：数据库结构调整；接口语义变更；第 10 节列出的独立缺陷修复；`backend/internal/gateway/simulator/` 的行为类型（工具私有，无数据库列与对外路径，保持在包内）。

## 2. 现状事实

统计口径与复现命令（仓库 `backend/` 下执行）：

```bash
# 数据库 ENUM 列，仅匹配行首列定义，排除 SQL 注释
grep -hE "^\s*\`[a-z_]+\` enum\('" migrations/*/0001_init.sql | wc -l        # 66
# Go 侧候选字段：先列出匹配文件并排除测试文件，再计数
PAT='^\s+(Status|Type|Kind|Source|Channel|Direction|Scope|Severity|Category|BizType|DiscountType|TriggerType|GrantSource|ReportSource|FaultType|ExecutionPolicy|InvoiceType|ReviewStatus|EventType|FileFormat|Protocol|Gender|Outcome|PayMethod|Reason)\s+string'
grep -rlE "$PAT" --include='*.go' internal/ | grep -v '_test.go' | xargs grep -hE "$PAT" | wc -l   # 227
grep -rlE "$PAT" --include='*.go' internal/ | grep -v '_test.go' | wc -l                          # 111
grep -rlE "$PAT" --include='*.go' internal/ | grep -v '_test.go' | xargs -n1 dirname | sort -u | wc -l   # 30 个 Go 包目录
```

| 项 | 数量 | 说明 |
| --- | --- | --- |
| 数据库 ENUM 列 | 66 | `central_db` 55、`gateway_db` 9、`worker_db` 2 |
| Go 侧候选字符串字段 | 227 | 带 `gorm:"column:"` 61 处、带 `json:` 116 处、两者皆无 71 处 |
| 涉及文件 | 111 | 分布于 30 个 Go 包目录、20 个 `internal` 顶层目录 |

**包数口径**：本文统一使用「Go 包目录」（30），`internal` 顶层目录数（20）供对照，不作为「包数」表述。

**该正则只是候选发现工具，不能替代语义盘点。** 它遗漏多字段同行声明、指针字段与非单字段 `string` 形式的枚举，也会把 `Reason` 一类自由文本计入。因此 227 是扫描规则的候选结果，既不表示这些字段全部是枚举，也不表示未匹配项已类型化或属于范围外；第 8 节的逐项清单才是范围依据。

已有类型化样板：`internal/domain/charging/mode.go`、`internal/domain/charging/spec.go`、`internal/domain/operations/scope.go`、`internal/protocol/adapter.go`。样板已证明路径可行：`charging.ModeServerEnergy` 进入业务判定 `internal/charging/query/user_display.go:27`，`charging.ChargeMode` 进入 gorm 仓储投影 `internal/pricing/metering_repo.go:91`。**本方案不引入新范式，只把已有范式推广到其余字段。**

## 3. 对外契约不变的依据

以下四项均已实测。**保持 JSON 标签与字符串表示是兼容的必要条件之一，不是充分条件**；解码、绑定、可空值、映射与存储路径的完整兼容由第 9 节回归验证，不以标签未变代替核验。

**语言层**：`type X string` 的底层 `reflect.Kind` 仍为 `string`，Go 无类型字符串常量可隐式转换。经编译验证，类型化后以下写法继续合法：

```go
row := Row{Status: "refunded"}           // 结构体字面量赋值
if row.Status == "refunded" { }         // 与字面量比较
switch row.Status { case "refunded": }  // case 标签
m := map[string]Status{"a": "paid"}      // map 字面量
```

唯一的编译中断点是字符串**变量**流入自定义类型参数：`cannot use plain (variable of type string) as Status value`。这类转换边界由编译器指出；同时也说明**类型化本身不拦截拼写错误**——`Status: "拼写错误"` 仍可编译，第 7 节据此设定完成标准。

**序列化层**：底层为 string 且未定义 `MarshalJSON` 时，编码结果仍是 JSON 字符串。

**校验层**：`go-playground/validator` v10.30.1 的 `isOneOf` 对 `reflect.String` 取 `field.String()`，`hasValue` 对值类型走 `field.IsZero()`。自定义字符串类型行为与 `string` 一致，`internal/contracts/gateway/charging.go:78` 与 `internal/contracts/gateway/card.go:52` 的两处 `oneof` 校验不变。

**HTTP 层**：`internal/http/` 下没有独立 DTO 层，handler 直接将用例结构体交给 `httpapi.OK`（见 `internal/http/user/wallet/wallet.go`）。

**配套红线**：不得为状态类型定义 `MarshalJSON`，不得改动 JSON 标签，不得对出参挂接取值校验，不得调整已有常量的字面量取值，不得新增全局 `UnmarshalJSON` 校验。

## 4. 枚举的三个记录维度

分类须区分**字段**与**取值**，二者不在同一层级。以下三个维度相互正交，任何枚举都应同时记录三者。

**维度一 · 存储形式**：ENUM 列、VARCHAR 列、无数据库列。

**维度二 · 值的产生方式**：业务写入、数据库生成、内存或 SQL 展示派生、外部契约输入。

**维度三 · 各操作允许读写的集合**：同一字段在不同操作下的合法集合可以不同。

由此得到几处必须区分的结论：

- **有限 VARCHAR 集合同样可类型化**。`device_meta.charge_mode` 是 `varchar(32)`（`migrations/central_db/0001_init.sql:1038`），取值正是 `charging.ChargeMode` 的六个值，本轮不因此修改数据库结构。`device_fault_report_event.from_status` 与 `to_status` 是 `varchar(24)`（同文件 `:439-440`）。
- **存储形式与产生方式可以重叠**。`charge_order.business_status` 是 ENUM 列，但由数据库生成（`:202` 的 `GENERATED ALWAYS AS`），Go 侧只读不写。
- **持久化合法值与展示派生值可以指向同一取值**。`coupon_grant.expired` 既是数据库合法值，也由 `internal/marketing/query.go:84` 按时间派生展示。定义持久化常量不意味着新增写入路径。
- **外部契约输入独立于本地持久化状态**。`internal/payment/channel_refund.go:30` 的渠道退款状态来自渠道响应并原样透传，既非展示合成值，也不与本地退款状态合并。
- **开放标签按实际语义判断**。`internal/worker/outbox/publisher.go:16` 的 `Source` 注释为「非空即可」，属开放标签，保持 `string`。
- **「预留／当前无写入路径」是具体取值的属性，不是字段类别**，应作为常量注释说明核查范围。

## 5. 类型放置规则与依赖可行性

### 5.1 基本规则

`internal/domain` 不引用业务实现、GORM 或 HTTP 框架，是跨用例共享类型的唯一安全落点。

1. **跨用例共享且消费方无法直连属主包的类型，置于 `internal/domain/<域>/`。** 判定见 5.2。
2. **contracts 层可复用 domain 与 protocol 类型**，但**不得引用业务实现包**。先例：`internal/contracts/central/results.go:4` 已导入 `internal/domain/charging`，`internal/contracts/gateway/charging.go:54` 已使用 `protocol.EventType`。
3. **进程私有持久化状态保留在所属包**；若该类型需经 contracts 对外传递，类型归属必须落在 contracts 或 domain，由 gateway 引用。
4. **取值相同而语义不同的枚举必须分立**。`coupon.status` 与 `coupon_activity_rule.status` 同为 `active`／`disabled`，但分属两个业务域。
5. **已有类型不重复建模**。`internal/domain/charging/spec.go:65` 已有 `type Channel string` 及 `ChannelDefault`／`ChannelTemp`／`ChannelCard`（`:69-72`），`internal/pricing/pricing_repo.go:76` 已在使用；定价通道复用该类型，不新建 `pricing.PricingChannel`。
6. **已有常量组就地升级为类型**。`internal/domain/operations/scope.go:5-6` 已有 `ScopeStation`／`ScopeVendor`，`:32,34` 已在 switch 中使用，`DataScope.ScopeType` 同在该包；数据范围类型直接在 `domain/operations` 类型化，不放 `ops`。

### 5.2 依赖可行性判定

**判定前必须检查完整依赖路径**：新增消费方 `C → T` 之前，确认不存在 `T → … → C` 的传递路径。**任何常量引用都产生 Go 包依赖，与是否声明结构体字段无关**——包括把 SQL 参数、函数实参、比较表达式中的字符串换成常量。SQL 聚合不构成例外：`internal/ops/dashboard_repo.go:89` 的 `status IN ('active','acknowledged')` 按第 7 节收敛为常量后即需导入目标包。

已核实会成环的组合（⚠），均须上提至 domain：

> **本表为 2026-10-10 的判定。P1 实施前以当前 import 图复核后发现四行已不成立**（`wallet`、`settlement`、`ExecutionPolicy`、`gateway.*`），逐条修正与证据见 [8.3.1 P1 实施前的依赖复核修正](#831-p1-实施前的依赖复核修正)。复核后本表仍成立的行继续适用。

| 若类型放在 | 冲突消费方 | 既有反向依赖 |
| --- | --- | --- |
| `payment` | wallet、order、marketing | `payment → wallet`（`payment/recharge_history_repo.go:5`） |
| `settlement` | wallet、payment、ops | `settlement → wallet`（`settlement/balance_refund_repo.go:8`） |
| `marketing` | identity | `marketing → identity`（`marketing/coupon.go:6`） |
| `identity` | wallet | `identity → wallet` |
| `card` | order、payment、contracts | `card → order`、`card → payment`（`card/card_repo.go:6-7`）；`contracts/central/card.go:21-32` 的 `CardOperationReply` 必须消费该类型 |
| `device` | ops | `device → ops`（`device/alert.go:8`、`user_station.go:10`、`vendor.go:10`、`import.go:15`） |
| `gateway` | contracts、domain | `gateway → contracts`（`contracts/gateway/charging.go:86,99` 的 `StopReply`／`StartReply` 携带命令状态） |

**domain 包不得反向依赖业务实现包**。`internal/domain/money/scan.go:11,19` 与 `internal/domain/money/card_port.go:12` 已用 `DeviceStatus string` 承载中央运营状态；因此该类型必须落在 domain（如 `domain/devices`），**不得统一为 `device.DeviceMetaStatus`**，否则 domain 将引用 `device` 实现包。

**无冲突、可留在属主包**（括号内为**已存在**的「消费方 → 属主包」导入）：`wallet`（card、identity、http）、`ops`（device、http、settlement、wallet）、`order`（charging、settlement、payment）。

**须区分方向**：`pricing → device` 已存在（`internal/pricing/pricing_repo.go:5`）。因此 device 的类型可被 pricing 直接消费；但**新增 `device → pricing` 会成环**，device 不得引用 pricing。定价自身状态的消费方须逐一核对完整依赖路径后，才能决定是否留在 `pricing`，不得因业务关联就认定两包可互相引用。

### 5.3 `biz_type` 必须拆为两组

现有同名字段的取值集合不同，禁止采用取值并集：

| 组别 | 取值 | 所属列 |
| --- | --- | --- |
| 支付关联业务类型 | `charge` / `wallet_recharge` | `payment_order`、`refund_record`、`invoice_request`（`:507`、`:576`） |
| 钱包流水业务类型 | `recharge` / `pay` / `refund` / `gift` / `freeze` / `unfreeze` / `adjust` | `wallet_txn`（`:847`），写入见 `internal/wallet/wallet_ledger_repo.go:73` |

`coupon_redemption.biz_type` 是 `varchar(32)`（`:404`）。已核实 `internal/marketing/coupon_redeem_repo.go:64,78` 的读写均限定 `charge`（`WHERE biz_type='charge'`、`Create` 写 `"biz_type": "charge"`），其**可写子集仅 `charge`**；共享类型的合法集合不等于该操作的可写集合，不因复用而开放钱包充值核销，也不据此修改 VARCHAR schema。该表 `biz_id` 保存支付主键，不套用其他表的 ID 语义。

### 5.4 `internal/domain/devices` 的准入与边界

该包职责确定为**设备相关的共享值对象与状态词汇**。本轮已决定扩展现有包，容纳以下内容：遥测值对象、设备运营状态、厂商状态与厂商传输配置、告警状态、充电与停机命令状态。下列四条为其准入条件，新增内容须逐条比对。

**一、类型准入：只接收设备相关的共享事实。** 本轮范围限定为上述五类。新增类型须说明来源、语义与共享消费者；**仅因字段包含 `DeviceID`，不足以归入该包**。私有类型继续留在所属实现包。

**二、判定粒度：只判断取值与自身不变量。** `Known()` 及固定集合的 `ManageWritable()` 属本包范围。「设备是否可以启动」涉及订单、钱包、权限与运行状态，由充电用例决定，不下沉到本包。判定方法**不得读取当前时间、配置或可变全局状态**，也**不得顺带推进状态**。

**三、依赖方向：保持在依赖底层。** 本轮新增枚举不引入任何项目内包依赖——已核实 `internal/domain/devices` 当前零内部依赖，新增内容均为值类型、常量与纯判定。持续禁止引用业务实现、contracts、integration、persist 与框架；**尤其禁止引用 `internal/domain/charging`**，因其已在 `internal/domain/charging/measured_segments.go:4` 导入本包。将来若需新增内部依赖，须重新核对完整依赖图。

**四、语义独立：同包收纳不代表状态可互换。** 中央设备状态与网关设备状态、充电命令与停机命令、厂商传输配置与会话传输方式继续分立，不合并为取值并集。**按语义分文件**，避免统一的 `Status`、通用状态转换器。每个类型须注明数据来源、空值与未知值规则。

当前这些类型同处一包是合理的；将来若出现独立的状态机、策略或领域算法，再按行为归属评估拆分。

## 6. 可写集合与读取透传的硬约束

**不能以单一 `Valid()` 收口。** 同一字段在不同操作下的合法集合不同，须按操作边界分别建模。

以中央库 `device_meta.status` 为例，该列合法集合是四值 `enabled`／`disabled`／`retired`／`fault`：

- 管理端操作 `PUT /api/v1/admin/devices/:id/status` 的用例 `internal/device/management.go:107` 只接受 `enabled` 与 `disabled`，这是**该管理操作的可写集合**，不是数据库合法集合。
- 仓储函数 `internal/device/admin_repo.go:30` 的 `SaveDeviceStatus` 将传入值原样写入中央表，**本身不做取值限制**。不能由管理用例的约束推断「所有写入路径都不产生 `fault`」。
- 读取路径 `POST /api/v1/user/scan/resolve` 原样回显该列。`internal/http/user/payment/scan_test.go:107` 断言响应包含 `fault`，`:110` 断言非 `enabled` 状态必须不可选。该用例覆盖的是**数据库已知值的回显**，不是任意未知字符串的透传；未知值透传需另行补充用例，不得以 `fault` 代替。

网关库另有独立的 `device.status`，仅三值。**判断字段合法性必须先确认其数据源**，不得用网关列推断中央字段。

```go
// DeviceMetaStatus 是中央库 device_meta.status 的运营状态，位于 domain/devices。
type DeviceMetaStatus string

// Known 判断是否属于该列的数据库合法集合（四值），供读取路径判断是否需要降级处理。
func (s DeviceMetaStatus) Known() bool

// ManageWritable 判断是否属于管理端设备启停操作允许写入的集合（两值）。
func (s DeviceMetaStatus) ManageWritable() bool
```

**读取路径不得因 `Known()` 返回 false 就新增拒绝、丢弃或替换行为。**

## 7. 收敛完成标准

类型定义完成且 `go build` 通过后仍会遗留以下问题，编译全绿不构成完成的证据。

**每批必须同时收敛的对象**：结构体字段、函数参数与返回值、集合元素、局部状态变量、分支判定、SQL 参数与写入常量。只改结构体字段不构成完成。

**编译器无法捕获的四类残留**：

1. **Go 裸字面量**。无类型常量仍可赋给自定义类型，`Status: "拼写错误"` 仍能编译；显式转换也不自动验证取值集合。生产代码的枚举判定与写入须复用所属类型常量。保留裸字面量的例外须逐项登记理由；验证非法输入、未知值或序列化形状的测试可以保留字面量。显式转换用于保留边界，不得代替输入准入校验。
2. **SQL 字符串内的裸字面量**。类型系统不检查 SQL 文本。已确认：`internal/identity/charge_user_repo.go:241` 的 `status = 'unused'`、`internal/ops/announcement_query_repo.go:29` 的 `status='published'`、`internal/device/user_station_repo.go:32,37,44` 的三处 `status='active'`、`internal/marketing/coupon_repo.go:50` 的 `c.status = 'active'`、`internal/ops/dashboard_repo.go:89` 的 `status IN ('active','acknowledged')`。改为常量时按第 5.2 节复核完整依赖路径——**这正是告警状态必须上提 domain 的原因**。
3. **`map[string]any` 与动态写入**。`internal/wallet/wallet_ledger_repo.go:73` 以 `"biz_type": biz` 写入，局部变量不会因其他投影字段已类型化而获得枚举约束。动态与反射路径以行为回归验证。
4. **跨包重复声明**。同一枚举多处独立声明，编译器不会提示；同一列被登记在两个批次或两个归属包，同样是此类残留。

**注释校正须先核对字段来源与操作边界**，不得机械对齐整列取值：`internal/device/device_repo.go:30` 的四值注释读自中央 `device_meta`（投影见同文件 `:51`），与其所属列一致，**无需修改**；`internal/device/fault_repo.go:67` 的 `EventType` 注释列四个值是事件表六值列的可写子集，不是错误；确需修正的是 `internal/wallet/wallet_account_repo.go:230`（注释自钱包账户复制而来）与 `internal/device/alert_repo.go:72`（遗漏 `resolved`）。

## 8. 逐枚举清单与分批

每项范围内枚举必须分配批次；排除项登记理由，不以未列入批次隐式排除。完成状态按本清单逐项关闭，不以批次完成或候选字段数量作为覆盖证据。**验证入口统一见第 9 节**。

**各列来源与可信度**：「数据库列」与「存储」取自 `migrations/*/0001_init.sql`，已逐项核对；P0 行的「Go 字段」已于首批实施时逐条复核并更正，P1／P2 行仍为 2026-10-10 盘点定位，**实施前须逐条复核**；「目标类型与归属」已按第 5.2 节依赖路径校正。

### 8.1 P0 · 高重复声明，先验证模式

> **本批已实施并通过第 9 节基础入口**。行号为本批改动后的位置，后续改动会让其漂移；核对时请以「文件 + 字段名」定位，不要以行号为准。

| 枚举 | 数据库列与存储 | 产生方式 | Go 字段（已复核） | 目标类型与归属 | 批次 | 完成 |
| --- | --- | --- | --- | --- | --- | --- |
| 订单状态 | `charge_order.status` ENUM 8 值 | 业务写入 | `order.ChargeOrderRecord.Status`、`order.LifecycleView.Status`、`order.ChargingState.Status`、`order.FeedbackOrder.Status`、`order.OrderView.Status`、`settlement.InvoiceApplyOrder.Status` | `domain/order.OrderStatus`（无冲突） | P0 | ☑ |
| 订单业务状态 | `charge_order.business_status` ENUM | **数据库生成，只读** | `order.ChargeOrderRecord.BusinessStatus`、`order.OrderView.BusinessStatus` | `domain/order.OrderBusinessStatus` | P0 | ☑ |
| 订单支付状态 | `charge_order.payment_status` ENUM 4 值 | 业务写入 | `order.ChargeOrderRecord.PaymentStatus`、`order.OrderView.PaymentStatus`、`payment.PaymentOrderView.PaymentStatus`、`wallet.AdminRechargeRow.PaymentStatus` | `domain/order.OrderPaymentStatus` | P0 | ☑ |
| 支付关联业务类型 | `payment_order`／`refund_record`／`coupon_redemption` `biz_type` | 业务写入 | `payment.PaymentOrderRecord.BizType`、`settlement.RefundRecord.BizType`、`settlement.RefundRecordInput.BizType`、`wallet.Settlement.BizType`、`money.RefundDraft.BizType`、`payment.PaymentOrderView.BizType` | `domain/money.PaymentBizType` | P0 | ☑ |
| 支付单状态 | `payment_order.status` ENUM 6 值 | 业务写入 | `payment.PaymentOrderRecord.Status`、`payment.RefundPayment.Status`、`payment.OrderPaymentBrief.Status`、`wallet.RechargePayment.Status`、`wallet.WalletRechargeRow.Status`、`wallet.AdminRechargeRow.Status`、`charging/query.PaymentBrief.Status` | `domain/money.PaymentStatus` | P0 | ☑ |
| 退款单状态 | `refund_record.status` ENUM 5 值 | 业务写入 | `settlement.RefundRecord.Status`、`settlement.RefundRecordInput.Status`、`wallet.WalletRefundPartRow.Status` | `domain/money.RefundStatus` | P0 | ☑ |
| 券发放状态 | `coupon_grant.status` ENUM 3 值 | 业务写入；`expired` 另由 `marketing/query.go` 内存派生展示 | `marketing.CouponGrant.Status`、`marketing.UserCoupon.Status` | `domain/marketing.GrantStatus` | P0 | ☑ |
| 券发放来源 | `coupon_grant.grant_source` ENUM 5 值 | 业务写入 | `marketing.activityGrant.Source`（内部结构体）；`grantSourceFor` 返回该类型 | `domain/marketing.GrantSource`（与状态分立，语义不同） | P0 | ☑ |
| 故障工单状态 | `device_fault_report.status` ENUM 4 值 | 业务写入 | `device.FaultRow.Status`、`device.FaultResolution.Status`、`device.FaultSubmissionResult.Status`、`device.FaultEventInput.FromStatus`／`ToStatus` | `device.FaultStatus`（原地升级既有无类型常量，判定在 `fault.go`） | P0 | ☑ |
| 刷卡操作类型与状态 | `card_operation.kind`／`status` ENUM 2 值／3 值 | 业务写入 | `card.CardOperation.Kind`／`Status`、`contracts/central.CardOperationReply.Kind`／`Status`、`charging/query.CardOperation.Kind`／`Status` | **`domain/card.CardOperationKind`**／`CardOperationStatus`（contracts 不得引用 card） | P0 | ☑ |
| 扫码结果种类 | **无数据库列** | **仓储逻辑派生** `device`／`port` | `money.ScanResult.Kind`、`money.PortRef.Kind`、`contracts/gateway.ScanReply.Kind`、`gateway/repo.ScanResult.Kind` | `domain/money.ScanKind`（与端口状态分离，二者不建立相互映射） | P0 | ☑ |
| 设备端口状态 | `gateway_db.device_port.status` ENUM 5 值 | 业务写入＋设备上报 | `money.ScanPort.Status`、`contracts/gateway.ScanPort.Status`、`gateway/repo.ScanPort.Status`／`scanPortRow.Status`／`devicePortRow.Status`／`SyncPortRow.Status` | `domain/money.PortStatus`（**单一归属**） | P0 | ☑ |

### 8.1.1 P0 实施后新增与核对的维护规则

以下规则是本批实施中确认的，后续批次沿用：

1. **同名不同义的列必须逐处确认后再改**。本批至少四处字面量相同但列不同，若按字面量统一赋值会静默写错列：`coupon.status` 与 `coupon_grant.status`（均为 `Status`，取值完全不同）、`payment_order.status` 与 `charge_payment_intent.status`（`initiated`／`paid` 共有）、`refund_record.status` 的 `rejected` 与复核流程返回的 `rejected`、`device_port.status` 与 `charge_process.port_status`（后者是协议原始码 tinyint）。
2. **同一取值串不等于同一语义，拆成不同的常量**。`PaymentStatus` 的 `paid refunded partial_refunded`（到账类）与 `paid partial_refunded`（可退类）必须分开：退款准入**不得**接受已全额退回的支付单。已分别定义为 `PaidLikeStatuses` 与 `RefundablePaymentStatuses`，各带注释说明差异。
3. **子集合要按业务口径提取具名集合函数**，不要在各处重复罗列字面量：`unsettledRefundStatuses()`、`paymentBizTypes()`、`refundablePaymentStatuses()`、`paidOrChargingOrders()`、`idleOrChargingPorts()`、`activityGrantSources()`。集合口径变化时只改一处。
4. **入库白名单串按字面量逐字节保留**，不随类型化改写：`domain/order.AllStatuses`／`AllBusinessStatuses`／`AllPaymentStatuses`／`AllChannelRefundableStatuses`、`domain/money.AllPaymentBizTypes`／`AllRefundStatuses`。新增枚举取值**不会**自动进入白名单，需按业务准入规则显式补充，注释已写明。
5. **`Contracts` 允许依赖 `internal/domain/*`，不允许依赖业务实现包**。本批新增了 `contracts/central` → `domain/card`、`contracts/gateway` → `domain/money` 两条依赖，与既有 `contracts/central/results.go` → `domain/charging` 一致；`internal/domain/*` 不反向依赖任何业务实现包。

### 8.1.2 P0 保留裸字面量的例外登记

以下位置经核对**不属于**本批 12 项枚举，或位于 SQL 语法内无法参数化，保留字面量并已在代码处标注理由：

| 位置 | 保留理由 |
| --- | --- |
| `order/order_view_repo.go` 的 `PaymentStatusSQL`、订单退款派生 `CASE` 表达式 | `CASE` 分支值与聚合表达式是 SQL 语法的一部分，不是可绑定参数 |
| `marketing/admin_coupon_repo.go` 的 `SUM(status='unused' …)` 等聚合 | 同上，聚合条件内嵌在表达式里 |
| `identity/user_repo.go`、`identity/charge_user_repo.go` 的 `coupon_grant` 计数子查询 | `Raw` SQL 跨包直读库，无法用 Go 常量绑定；已在原处加注释指向 `GrantStatus` 词表 |
| `payment` 各处的 `charge_payment_intent.status`（`initiated`／`expired`／`closed`） | 该列词表为 `initiated/paid/expired/closed/refund_required`，与 `payment_order.status` **不同**；已在 `payment/money_schema.go` 字段注释与使用处标注 |
| `gateway` 各处的 `charge_command.status`（`pending/sent/acked/stopping/rejected`）、`device.status`、`vendor.status` | 分别是命令、网关设备与厂商表的词表，不在本批范围；`gateway/repo/sync_models.go` 已加注释区分 |
| `settlement` 的 `charge_billing_job.status`、`charge_meter_review.status`、`charge_bill.status`、`execution_policy`、`invoice_request.review_status` | 均为独立列的枚举，不在本批范围 |
| `marketing` 的 `coupon_activity_rule.trigger_type`、`coupon.status`、`coupon_activity_rule.status` | 独立列的枚举；`trigger_type` 的 `invite_reward` 与 `GrantSourceInviteReward` 同值但不同义，不合并 |
| `http/user/order/query.go` 的 `case "expired"`、`http/central/card_api.go` 的 `case "expired"`、`charging/query.UserInputError.Kind`、`card` 的失败阶段 | 是错误分类与阶段标识，不是数据库状态列 |
| `protocol.CommandKind` 的 `CommandStart` | 设备协议命令码，与 `CardOperationKind` 同值不同义，两个包各自定义并在注释中互相点名 |
| `cmd/devseed/**` 的订单、支付与意图状态 | 开发演示造数工具，不参与业务判定；其 `status` 同时喂给 `charge_order.status` 与 `charge_payment_intent.status` 两个不同词表，单一类型化会掩盖该差异。留待后续批次统一处理 |
| `persist.PageQuery.Status` | 跨业务共用的查询筛选项，被 fault／vendor／account／marketing 等多家复用且各自白名单不同，不能直接改为单一枚举类型；各用例在自己的校验处转换 |

### 8.2 P1 · 主体持久化枚举

> **本批已实施并通过第 9 节全部入口**（`node tasks.mjs check`、`build-all`、`go vet -tags integration` 与真实 MySQL 集成测试；真库运行结果见 9.2 节）。行号已随改动漂移，核对时以「文件 + 字段名」定位，不要以行号为准；「目标类型与归属」列的四行经复核后已修正，与当前代码一致。

| 枚举 | 数据库列与存储 | 产生方式 | Go 字段（已复核） | 目标类型与归属 | 批次 | 完成 |
| --- | --- | --- | --- | --- | --- | --- |
| 用户状态与性别 | `user.status`／`gender` ENUM 2 值／3 值 | 业务写入 | `identity.UserAccount`、`charge_user_repo.go`；跨域 card／ops／payment／wallet | `domain/identity.UserStatus`／`Gender` | P1 | ☑ |
| 设备运营状态 | `device_meta.status` ENUM 4 值 | 业务写入 | `device.Device.Status`、`operation_repo.go`、`money.ScanResult.DeviceStatus` | **`domain/devices.DeviceMetaStatus`**（`Known()` 四值 + `ManageWritable()` 两值） | P1 | ☑ |
| 告警状态与级别 | `alert_event.status`／`severity` ENUM 4 值／3 值 | 业务写入 | `device.DeviceAlert.Status`；`ops/dashboard_repo.go` 与 `device/alert_repo.go` 绑定参数 | **`domain/devices.AlertStatus`**／`AlertSeverity` | P1 | ☑ |
| 充电命令状态 | `charge_command.status` ENUM 5 值含 `stopping` | 业务写入＋设备回执 | `gateway/repo/gorm_models.go`、`sync_models.go`；经 contracts `charging.go` 与 `sync.go` | **`domain/devices.ChargeCommandStatus`** | P1 | ☑ |
| 停机命令状态 | `charge_stop_command.status` ENUM 4 值无 `stopping` | 同上 | `gateway/repo/gorm_models.go`、`user_stop_repo.go`；经 contracts `charging.go` | **`domain/devices.StopCommandStatus`**（独立类型，未取并集） | P1 | ☑ |
| 数据范围类型 | `admin_data_scope.scope_type` ENUM 2 值 | 业务写入 | `domain/operations/scope.go`（就地升级）；`ops/scope_repo.go` 的投影行 | **`domain/operations.ScopeType`** | P1 | ☑ |
| 刷卡事件投递 | `card_event_delivery.status` ENUM 2 值 | 业务写入 | `gateway/repo/card_delivery_status.go`；`card_delivery_repo.go` 绑定参数与写入 | **`gateway/repo.CardDeliveryStatus`**（修正：原 `gateway.CardDeliveryStatus` 无处落地，见 8.4） | P1 | ☑ |
| 网关设备状态 | `gateway_db.device.status` ENUM 3 值 | 业务写入 | `gateway/repo/backfill_repo.go`、`provision_repo.go` | **`domain/devices.GatewayDeviceStatus`**（改名避开中央四值，见 8.4） | P1 | ☑ |
| 厂商状态 | `vendor.status` ENUM 2 值 | 业务写入 | `gateway/repo/provision_repo.go`；**经 contracts** `contracts/gateway/provision.go`；`device/vendor.go`；`integration/gateway/vendor.go` | **`domain/devices.VendorStatus`**（`Filtered()` 独立表达「空串=不筛选」） | P1 | ☑ |
| 厂商传输配置 | `vendor.protocol` ENUM 3 值含 `hybrid` | 业务写入 | `gateway/repo/provision_repo.go`、`gateway/provision/vendors.go`；经 `contracts/gateway/provision.go` | **`domain/devices.VendorProtocol`**（不与会话传输取并集） | P1 | ☑ |
| 会话传输方式 | `device_session.protocol` ENUM 2 值 `tcp`／`mqtt` | 业务写入＋连接建立 | `gateway/repo/session.go`、`session_repo.go` | **复用 `protocol.Transport`**（本批补 `Known()`） | P1 | ☑ |
| 会话关闭原因 | `device_session.close_reason` VARCHAR 开放集合 | 业务写入 | `gateway/repo/session.go`、`session_repo.go`；`protocol.SessionRecord.CloseReason` | **复用 `protocol.CloseReason`**（本批新增 `CloseAbandoned`） | P1 | ☑ |
| 设备事件类型 | `device_event.event_type` VARCHAR 开放集合 | 协议上报 | `gateway/repo/gorm_models.go` 与 `sync_models.go`（同列两处声明） | **复用 `protocol.EventType`** | P1 | ☑ |
| 事件 Outbox | 三张表 `event_outbox.status`／`admin_event_outbox.status` ENUM 3 值 | 业务写入 | `ops/event_outbox_repo.go`；`worker/repo/outbox_repo.go` | **`ops.OutboxStatus`**（修正：见 8.4） | P1 | ☑ |
| 设备计费方式 | `device_meta.charge_mode` **VARCHAR** | 业务写入 | `device/device_repo.go`、`device_import.go`、`import_repo.go` | 复用 `charging.ChargeMode`（本批连带将 `ValidDeviceChargeMode` 形参改为该类型） | P1 | ☑ |
| 站点状态 | `station.status` ENUM 3 值 | 业务写入 | `device/station_repo.go`、`station.go`、`user_station_repo.go` 四处 SQL、`import_repo.go` | `device.StationStatus` | P1 | ☑ |
| 故障类型与来源 | `device_fault_report.fault_type`／`report_source` ENUM 5 值／3 值 | 业务写入 | `device/fault_report.go`、`user_fault_repo.go` | `device.FaultType`／`ReportSource` | P1 | ☑ |
| 设备导入任务 | `device_import.status` ENUM 3 值 | 业务写入 | `device/device_import.go`、`import_repo.go` | `device.ImportStatus` | P1 | ☑ |
| 支付方式 | `payment_order.pay_method` ENUM 4 值 | 业务写入 | `payment/money_schema.go`；跨域 wallet／order | **`domain/money.PayMethod`** | P1 | ☑ |
| 支付意图状态 | `charge_payment_intent.status` ENUM 5 值 | 业务写入 | `payment/money_schema.go`、`payment_intent_repo.go`、`payment_callback_repo.go` | **`domain/money.PaymentIntentStatus`** | P1 | ☑ |
| 退款执行策略 | `refund_record.execution_policy` ENUM 2 值 | 业务写入 | `settlement/money_schema.go`、`refund_record_repo.go` 等；跨域 charging 一处 | **`domain/money.ExecutionPolicy`**（修正：「wallet 冲突」不成立，见 8.4） | P1 | ☑ |
| 计费任务状态 | `charge_billing_job.status` ENUM 3 值 | 业务写入；**跨域读模型** | `settlement/billing_job_repo.go` 等；`charging/query/user_repo.go` 与 `user.go` | `settlement.BillingJobStatus`（charging 侧 `BillingStatus` 已同步类型化，**空串语义原样保留**） | P1 | ☑ |
| 账单状态 | `charge_bill.status` ENUM 3 值 | 业务写入 | `settlement/bill_repo.go` | `settlement.BillStatus` | P1 | ☑ |
| 计量核实状态 | `charge_meter_review.status` ENUM 3 值 | 业务写入 | `settlement/meter_review_repo.go` | `settlement.MeterReviewStatus` | P1 | ☑ |
| 人工兜底单状态 | `manual_fee_review.status` ENUM 2 值 | 业务写入 | `settlement/refund_review_list_repo.go` | `settlement.ManualFeeReviewStatus` | P1 | ☑ |
| 发票类型与审核 | `invoice_request.invoice_type`／`review_status`、`invoice_admin_review.review_status` | 业务写入 | `settlement/invoice.go`、`invoice_repo.go`、`invoice_review.go` | `settlement.InvoiceType`；两套 ReviewStatus **分立**（命名含表归属） | P1 | ☑ |
| 钱包账户状态 | `wallet_account.status` ENUM 2 值 | 业务写入 | `wallet` 五处投影与写入；跨域 card／identity | **`domain/wallet.AccountStatus`**（修正：「无冲突」不成立，见 8.4） | P1 | ☑ |
| 钱包流水方向与业务类型 | `wallet_txn.direction`／`biz_type` ENUM 2 值／7 值 | 业务写入 | `wallet/wallet_ledger_repo.go` | **`domain/wallet.TxnDirection`／`TxnBizType`**（见 5.3；与 `PaymentBizType` 分立） | P1 | ☑ |
| 风险冻结记录 | `risk_freeze_log.status` ENUM 2 值 | 业务写入 | `wallet/admin_wallet_risk_repo.go`、`risk.go` | **`domain/wallet.FreezeStatus`** | P1 | ☑ |
| 在线卡状态 | `online_card.status` ENUM 4 值 | 业务写入 | `card/card_schema.go`、`status.go`、`admin.go`；跨域 http | `card.CardStatus`（`UserWritable()` 两值 + `ManageWritable()` 四值） | P1 | ☑ |
| 营销模板与活动状态 | `coupon.status`、`coupon_activity_rule.status` ENUM 2 值 | 业务写入 | `marketing/coupon.go`、`activity.go` 等 | **分立**为 `marketing.CouponStatus`／`ActivityStatus` | P1 | ☑ |
| 优惠类型 | `coupon.discount_type` ENUM 3 值 | 业务写入 | `marketing/coupon.go`、`query.go` | `marketing.DiscountType` | P1 | ☑ |
| 活动触发类型 | `coupon_activity_rule.trigger_type` ENUM 4 值 | 业务写入 | `marketing/rules_coupon_activity.go`；跨域 payment／settlement 各一处 | `marketing.TriggerType`（与 `GrantSourceInviteReward` 同值不同义，未合并） | P1 | ☑ |
| 反馈状态与分类 | `feedback.status`／`category` ENUM 3 值 | 业务写入 | `order/feedback_repo.go`、`feedback.go` | `order.FeedbackStatus`／`FeedbackCategory`（列表读取仍不设白名单） | P1 | ☑ |
| 公告状态与范围 | `announcement.status`／`scope` ENUM 3 值 | 业务写入 | `ops/announcement_repo.go`、`announcement.go`、`announcement_query_repo.go` | `ops.AnnouncementStatus`／`AnnouncementScope` | P1 | ☑ |
| 后台账号状态 | `admin_user.status` ENUM 3 值 | 业务写入 | `ops/account_repo.go`、`account_management_repo.go`（三处投影） | `ops.AdminUserStatus`（入参 `*string` 指针语义保留） | P1 | ☑ |
| 定价规则与模板状态 | `pricing_rule.status`、`pricing_template.status` ENUM 2 值 | 业务写入 | `pricing/pricing_scope_repo.go`、`scheme.go`、`query.go` | `pricing.PricingRuleStatus`／`PricingTemplateStatus`（删除路径空串行为未变） | P1 | ☑ |
| 导出任务状态与格式 | `export_task.status`／`file_format` ENUM 5 值／2 值 | 业务写入 | `export/export_task_repo.go`、`service.go`、`artifact.go` | `export.ExportStatus`／`ExportFileFormat`（枚举外 `"pdf"` 历史兼容值保留） | P1 | ☑ |
| 任务执行状态与触发来源 | `task_execution_log.status`／`triggered_by` ENUM 4 值／2 值 | 业务写入 | `worker/repo/schedule_repo.go` | `worker/repo.TaskExecutionStatus`／`TaskTriggeredBy`（写入路径仅测试调用，见 8.4.3） | P1 | ☑ |

### 8.3 P1 · 故障事件：按操作分别登记可写集合

`device_fault_report_event.event_type` 为六值 ENUM（`migrations/central_db/0001_init.sql:439`，原记 438 为 `actor_id` 行）。**四值不是全局可写集合**：用户报修入口 `internal/device/user_fault_repo.go` 实际写入 `reported`，且 `from_status` 写空串。

| 集合 | 取值 | 依据 |
| --- | --- | --- |
| 数据库合法集合 | `reported`、`dispatched`、`reassigned`、`fixed`、`closed`、`migration_baseline` | `migrations/central_db/0001_init.sql:439` |
| 用户报修操作写入 | `reported`（`from_status` 空串、`to_status` `open`） | `internal/device/user_fault_repo.go` |
| 后台处理操作写入 | `dispatched`、`reassigned`、`fixed`、`closed` | `internal/device/fault_repo.go` 注释与写入 |
| `migration_baseline` | **来源待核**：当前无写入路径，保留为兼容读取 | — |

- 目标类型 `device.FaultEventType`。**不提供以四值为准的统一写入校验**；若增加判定方法，按具体操作命名（如 `FaultEventType.BackgroundWritable()`）。
- 事件的 `FromStatus`／`ToStatus`（`internal/device/fault_repo.go`，VARCHAR `:439-440`）**复用 `FaultStatus`**，保留现有空串与可空表示，**不得新增拒绝或转换为 `null`**。

### 8.3.1 P1 实施前的依赖复核修正

第 5.2 节以 2026-10-10 的 import 图写成，P1 实施前以当前 HEAD（`5a5ac2c`）重新核对传递路径，**四处归属结论已不成立**。以复核结论为准，原表仅作历史记录。

| 项 | 原结论 | 实测证据 | 本批处置 |
| --- | --- | --- | --- |
| 钱包账户状态 | 留 `wallet` 包「无冲突」 | `card/card_repo.go` 已有 `card → wallet`；`card/interface_query_repo.go` 的 SQL 需消费该列的 `'active'` | **上提 `domain/wallet.AccountStatus`** |
| 结算五项 | 「均须上提至 domain」 | `wallet`、`payment`、`ops` 均未 import `settlement`；唯一跨包消费方是 `charging/query`（单向） | **留在 `settlement` 包**，仅同步 `charging/query` 的读模型字段 |
| 退款执行策略 | 「wallet 冲突」 | `wallet` 的 `manual_review` 属 `wallet_refund.status` 另一词表（`wallet/query.go`、`wallet/refund.go`），与 `refund_record.execution_policy` 零类型交互；跨域只有 `charging/start` 一处写入 | **按实测归 `domain/money`**，并登记该词表不得与 `wallet_refund.status` 合并 |
| `gateway.CardDeliveryStatus`／`gateway.DeviceStatus`／`gateway.OutboxStatus` | 留 `internal/gateway` 包 | **`internal/gateway` 是纯目录，没有根包**，三处归属无可落地的包 | 分别改判为 `gateway/repo.CardDeliveryStatus`、`domain/devices.GatewayDeviceStatus`、`ops.OutboxStatus` |

**另外两项实测修正**：

1. **中央四值与网关三值必须改名分立**。`domain/money` 已存在承载中央 `device_meta.status` 的 `DeviceStatus` 字段，网关 `device.status` 另建 `GatewayDeviceStatus`，两者在注释中互相点名，不得互相赋值。
2. **`device/user_station_repo.go` 的三处 `status='active'` 查的是 `station` 表**，不是 `device_meta`（另有第四处在 `import_repo.go`）。原第 7 节把它们与其他设备字面量并列列举，未标表名，易被误读。已归入 `StationStatus`；同文件 `status<>'retired'` 才是 `device_meta`。

**两处枚举在原盘点中被漏登，本批已补**：

- `refund_review` 表**没有审核状态列**，只有两个签署人与 `approved_at`。`settlement/refund_review_repo.go` 的 `rejected`／`awaiting_second`／`approved` 是**由签署人字段推导的返回值**，不是数据库列；已定义 `settlement.RefundReviewOutcome` 描述「一次复核动作返回什么」，注释明确它不是列、且与 `money.RefundStatus` 分立。
- `admin_data_scope.scope_type` 在生产代码中**只有读取**（唯一访问点 `ops/scope_repo.go`），写入全在测试夹具。原表标为「业务写入」不准确。

### 8.3.2 P1 保留裸字面量的例外登记

下列位置不属于本批枚举，或位于 SQL 语法内无法参数化，保留字面量并已在代码处标注理由：

| 位置 | 保留理由 |
| --- | --- |
| `marketing/coupon_repo.go` 的 `c.status = 'active'`、`coupon_activity_repo.go` 的两处 `AND status='active'` | JOIN 与锁查询条件；拆分会改变查询计划与锁语义 |
| `marketing/admin_coupon_repo.go` 的 `SUM(status='unused' …)` 等聚合 | `CASE`／聚合表达式内嵌在表达式里，不是可绑定参数；跨 `coupon_grant.status`（P0 的 `GrantStatus`） |
| `settlement/refund_executor_repo.go` 的 `execution_policy='automatic'`、`meter_review_repo.go` 的 `status IN (...)`、`invoice_repo.go` 的 ON DUPLICATE 子句 | 同上，且与占位符同处一条 Raw SQL |
| `identity/user_repo.go`、`identity/charge_user_repo.go` 的 `coupon_grant` 计数子查询 | `Raw` SQL 跨包直读库；P0 已登记 |
| `device/user_station_repo.go` 四处 `status='active'`、`import_repo.go` 的一处 | `station` 表字面量，保留并注释指向 `StationStatus` |
| `device/user_station_repo.go` 的 `status<>'retired'`、`import_repo.go` 的 `"status": "enabled"` | `device_meta` 表，属 `devices.DeviceMetaStatus`，不在该文件归属范围 |
| `gateway/repo` 内的 `event_type = 'heartbeat'` 等 | SQL 谓词内字面量，保留并注释指向 `protocol.EventType` |
| `gateway/repo` 内的 `status='pending'`／`IN ('sent','acked')` 等 | 同上，指向 `devices` 的命令状态词表 |
| `export/artifact.go` 的 `format == "pdf"` | **枚举外的历史兼容值**，`export_task.file_format` 只有 csv／xlsx；该分支为存量数据兜底，不得纳入 `Known()` |
| `worker/repo/schedule_repo.go` 的 `ExecutionLog.Status`／`TriggeredBy`、`FinishExecutionLog` 形参 | 调用方 `worker/schedule/scheduler.go` 把它们当 `string` 局部变量传递；本批定义类型词表并收敛 SQL 常量，字段类型待调用方收敛后一并处理 |

> **上条已收敛（P1 尾项）**：`BillingOrderProcessor.Defer` 的 status 形参与 `settlement.BillingOrders.Defer` 实现均改为 `BillingJobStatus`，连同 3 个集成测试替身（`recoveryOrders`、`financeDeliveryOrders`、`financeOrderBoundary`；`billReadOrders` 嵌入 `recoveryOrders` 自动跟随）同步；`worker/repo` 的 `ExecutionLog.Status`／`TriggeredBy`、`FinishExecutionLog` 形参与 `LastExecutionRow.Status` 均改为具名类型，`scheduler.go` 的 `execute` 形参与 `status` 局部变量同步。两处不再属于例外登记。收敛时顺带修正了 `FailStaleRunningLogs` 把 `string(TaskExecutionRunning)` 拼进 SQL 的写法（该写法与 9.2 节登记的单元素 IN 列表同源），改为绑定参数。
| `settlement/invoice.go` 的 `InvoiceType string` | 公开 HTTP 入参边界；用例内以 `invoiceTypeOf()` 收敛，JSON 解码路径零变化 |
| `persist.PageQuery.Status` | 跨业务共用的查询筛选项，被 coupon／activity_rule／export_task／feedback／user／vendor 等多家复用且各自白名单不同，**必须保持 `string`**，各用例在自己的校验处转换 |
| `platform/auth/jwt.go` 的 `Kind` | 令牌载荷的签发方标识，取值 user／admin 属平台层自有约定，非数据库状态列 |

### 8.3.3 P1 的空值与未装配边界

- **`charging/query` 的 `billing_status` 空串必须原样出现在响应体**。该字段无 `omitempty`，且计费任务不存在时 `Status` 为空串——空串不是 `charge_billing_job.status` 的合法取值，`Known()` 对它返回 false 是正确的，**但不得据此拒绝、替换为 `null` 或改为指针**。本批已实测序列化结果为 `"billing_status":""`。
- **`ops.AccountUpdateInput.Status` 与 `ops.EventOutboxRecord.Status` 保持指针语义**。前者以 `nil` 表示「不修改」，后者靠 `gorm:"default:pending"` 让数据库填充初值；改成值类型会把非法零值写进 ENUM 列。
- **`task_execution_log` 在生产中不会执行**。`cmd/worker/app.go` 的处理器登记表为空（`Handlers: map[string]schedule.Handler{}`），`CreateExecutionLog`／`FinishExecutionLog` 仅测试调用。类型化不代表该枚举已在线上运行；真库集成能验证存储与读取路径，但不能替代真实调度运行。
- **`pricing` 删除路径的空串行为未变**：`remove == true` 时跳过状态校验，空串由仓储写入 `disabled` 覆盖，不得在校验里拒绝空串。
- **真库集成已执行并通过**（95 个包零失败，见 9.2 节）。本批涉及的 GORM 投影列类型变更（指针 + 自定义类型、default 标签、ENUM 参数绑定）均有真实 MySQL 证据；真库运行同时找出了本批引入的六个回归，均已修复并逐条登记在 9.2 节。
### 8.4 P2 · 派生值、外部输入与受限标签

此类不设固定先后顺序，允许同一枚举的存储类型、读模型与映射在同一批完成。持久化与展示状态需分立时定义显式映射，保持原值、未知值、空值与时间判定行为。

> **本批已实施并通过第 9 节基础入口**，另补 `go vet -tags integration` 编译期检查（该层不参与 `node tasks.mjs check`，但能在真库执行前发现类型断裂）。行号已漂移，核对时以「文件 + 字段名」定位。

| 枚举 | 来源与存储 | 产生方式 | Go 字段（已复核） | 目标类型与归属 | 批次 | 完成 |
| --- | --- | --- | --- | --- | --- | --- |
| 订单启动来源 | SQL `CASE` 派生，**无数据库列** | 逻辑派生 `payment`／`balance`／`card` | `order/order_view_repo.go` 的 `orderStartSourceSQL` 与 `OrderView.StartSource` | **`order.OrderStartSource`**（新建 `order/derived_status.go`） | P2 | ☑ |
| 订单退款展示状态 | SQL `CASE` 派生，**无数据库列** | 逻辑派生 4 值 | `order/order_view_repo.go` 的 `orderColumns` 与 `OrderView.RefundStatus` | **`order.RefundViewStatus`**（与 `money.RefundStatus` 分立） | P2 | ☑ |
| 钱包退款展示状态 | 无数据库列 | 内存派生 6 值 | `wallet/query.go` 的 `refundView` 与 `RefundView.Status` | **`wallet.RefundViewStatus`**（新建 `wallet/refund_view_status.go`） | P2 | ☑ |
| 渠道退款状态 | 无数据库列 | **外部契约输入**，**开放集合** | `payment/channel_refund.go` 的 `RefundResult.Status`；`settlement/refund_executor_repo.go` 的 switch | **`payment.ChannelRefundStatus`**（四个大写取值；未知值不被拒绝，见下） | P2 | ☑ |
| 事件投递对象与来源 | 无数据库列 | 外部契约输入 | `internal/delivery/events.go` | **移出本批，登记入 8.5**（死代码路径，见下） | P2 | ⊘ |
| 网关停止请求来源 | contracts 输入 | **外部契约输入** `user_app`／`admin`／`auto` | `contracts/gateway/charging.go` 的 `StopRequest.Source` | **`contracts/gateway.StopSource`**（`binding` 标签逐字节保留） | P2 | ☑ |
| 刷卡推进结果 | contracts 输入 | **外部契约输入** `retry`／`done` | `contracts/gateway/card.go` | contracts 内类型，保留绑定校验；不复用投递状态集合 | P2 | ☑（**P1 已完成**） |
| 核销业务类型 | `coupon_redemption.biz_type` **VARCHAR** | 业务写入，可写子集仅 `charge` | `marketing/coupon_redeem_repo.go` | 复用 `domain/money.PaymentBizType`，**未因复用开放钱包充值核销**（见 5.3） | P2 | ☑（**P1 已完成**） |
| 适配器标识 | `device_meta.protocol_adapter`、`vendor.adapter_class`、`device_event.protocol_name` 均 VARCHAR | 业务写入，当前受支持集合仅 `dc589` | `protocol/adapter_id.go`（新建）；`Registration.Protocol`、`Event.Protocol`、`Adapter.Name()` 与能力判定参数同批覆盖 | **`protocol.AdapterID`**（明确为新增类型，不与 `tcp`／`mqtt`／`hybrid` 或 `EventType` 混用） | P2 | ☑ |

**订单启动来源的双语义已按原样保留**：展示字段是 `*OrderStartSource`（无来源为 `null`），筛选参数 `OrderQuery.StartSource` **保持 `string`**（空串表示不筛选）。筛选字段不改类型是有意为之——改后需把 `!= ""` 重写为 `!= nil`，会改变 `adminhttp.AdminOneOf` 的空串短路路径，风险高于收益。后台白名单已从字面量改为引用 `order.AllStartSources`。

**渠道退款状态是开放集合，不是封闭枚举**。判定依据三条：写入侧 `refundResult` 对 status 无 switch 白名单（只校验 SUCCESS 必须带成功时刻），其余取值原值放进结果；SDK 的 `refunddomestic.Status` 是 `type Status string`、JSON 反序列化不做 `oneof`；消费侧 `settlement/refund_executor_repo.go` 的 `default` 返回 `ErrRefundConflict` 是**资金闸门**（不改状态、不出账、不释放预占，退款保持在途），不是词表校验。因此 `Known()` 零调用，**不得改为拦截未知值**——放行会让未知取值沿 SUCCESS 之后的路径直接出账。

**适配器标识的 `Known()` 不构成准入门槛**。写入路径（厂商资料、CSV 导入）接受未登记标识并原样落库，能力判定仍由 `ProtocolCapabilities` 的 `default` 报错分支承担。测试夹具中的 `"test"`、`"future"`、`"unknown"`、`"DC589"`、`"dc590"` 均为**故意保留的非法值**，它们是「未登记标识按原值透传」与「大小写敏感」的测试证据，不得修正为合法值。`vendor.adapter_class` 是 `varchar(256)`（另两列是 `varchar(32)`），HTTP 层的长度校验因此保留，未因有类型而删除。

**新增依赖边 `domain/charging → protocol`**：`ProtocolCapabilities` 与 `CheckModeSupport` 的适配器参数改用 `protocol.AdapterID`。`protocol` 是零内部依赖的叶子包、非业务实现包，该边不违反「domain 不反向依赖业务实现包」，与既有 `domain/devices → domain/money` 同类。已复核 `internal/protocol` 仍零 `chargepilot/internal` 依赖，全仓无 import 环。

**扫码种类与端口状态不建立相互映射**：`port` 表示解析到单端口，不能推出 `idle`；`device` 返回多个各有状态的端口。已核实 `internal/gateway/repo/scan_repo.go` 将 `port.Status` 原样传入，未按 `idle` 过滤。保留现有结果组装与可用性计算，未知端口状态按原行为处理。

### 8.5 登记但不类型化

| 项 | 位置 | 理由 |
| --- | --- | --- |
| Outbox 来源标识 | `internal/worker/outbox/publisher.go` | 开放标签，注释为「非空即可」，非有限集合 |
| 模拟器行为类型 | `internal/gateway/simulator/dc589/behavior.go` | 工具私有，无数据库列与对外路径，保留在包内 |
| 退款原因等自由文本 | 多处 `Reason` 字段 | 自由文本，非枚举 |
| **监管事件投递对象与来源** | `internal/delivery/events.go` | **P2 实施前从 8.4 移入本节**。该包全仓 **0 处 import**、0 测试、监管投递未装配、`REGULATORY_*` 未被配置加载；worker 调度器处理器登记表为空。类型化死代码会强化「外部投递已就绪」的错觉，与第 1 节「不得据表、模板变量或组件存在宣称外部投递可用」相悖，待真正装配时再收敛。另注：其中 `ServiceType`、`deviceData.Protocol`、`alertData.Type` 是开放文本集合，`deviceData.Protocol` 是监管载荷的外部字段，与 `protocol.AdapterID` 是不同的东西，不可复用。 |

**保留组件的声明**：worker 调度器当前处理器登记表为空，Webhook、通用 DLQ 与监管投递未装配，`REGULATORY_*` 未被配置加载与消费。本方案对 Outbox 与投递相关类型只收敛现有类型，**不新增运行中的外部投递任务**。

## 9. 验收条件

**基础入口**：`node tasks.mjs check`（含 `fmt`、`lint`、`test`）与 `node tasks.mjs build-all` 均通过。

**确定性前后基线**：固定请求与夹具，控制可控时钟与运行状态，写入接口从同一起始数据快照分别执行改造前后。可控样本逐字节比较；不可控实时字段明确列出比较范围与限制，**状态字段不得归一化**，不将局部比较称为完整逐字节兼容。

**请求回归**覆盖有效值、字段缺失、空字符串、`null`、非法值、错误 JSON 类型；原有未知字段、尾随内容、长度与权限检查保持原行为。**响应与回执**覆盖合法状态、按原规则处理的未知状态、空值与 `null`、指针与 `omitempty`、派生状态及历史回执解码。**空值敏感项**（订单启动来源的可空指针、故障事件的空串 `from_status`、订单业务状态的生成列）须单独断言。

**副作用比较**：同时比较 HTTP 状态码、错误信封与数据库副作用，保持幂等身份、原请求重放、状态推进与退款边界。

**真实数据库**：涉及 GORM 投影、可空字段、枚举参数、状态写入、行锁、幂等或唯一键冲突的批次，登记并执行相关真实 MySQL 集成用例。`node scripts/db/test-integration.mjs` 会启动开发 MySQL／Redis、执行迁移并测试，且使用开发数据，执行前须核对夹具、并发与清理边界。

**绑定校验**：`binding:"oneof"` 本轮保留。仅当确有替换必要时单独登记解析位置、接受集合与失败行为，并逐入口核对。已知依赖方为 `internal/gateway/control/card_delivery.go:102` 的 `cardEventAdvance`，其在 `:112` 直接以 `in.Outcome == "retry"` 推进投递。

**契约核对**：触及 `internal/contracts/` 时，同时核对真实服务端、具名客户端与 `internal/http/auth/admin.openapi.json`、`internal/http/user/user.openapi.json`，不改变契约语义或文档生成方式。

**完成判据**：不以编译通过或代码扫描作为验收证据。第 7 节四类残留在编译通过的情况下依然存在，须按第 8 节清单逐项关闭。

**依赖可行性复核**：实施前对每项确认第 5.2 节的判定仍成立——**以改动前的完整 import 图重新核对传递路径**，不以本文结论代替核验。

**文档同步时机**：首批实施并验收后，再把稳定的维护规则登记进 [AGENTS.md](../AGENTS.md)、把实际类型归属与依赖边界写入 [architecture-boundaries.md](architecture-boundaries.md)；本文保留逐项进度与验收证据。在此之前继续明确「尚未实施」，不得把目标写成当前保证。

### 9.1 P0 与 P1 的执行记录

**已取得证据**：`go build ./...`、`go vet ./...`（含全部 `_test.go`）、`node tasks.mjs check`（fmt + lint + test）、`node tasks.mjs build-all` 全部通过；`go mod tidy -diff` 无差异；`migrations/`、`go.mod`、`go.sum` 零改动；全仓无新增 `MarshalJSON`／`UnmarshalJSON`，无 JSON 标签改动。

**审查**：P0、P1、P2 各经两轮独立审查（首轮 2 个🔴 + 3 个🟡、复审 0 个🔴）已全部关闭。首轮的两个🔴 是 `card_event_delivery.status` 整项漏做，以及 `OutboxStatus` 在 `domain/devices` 与 `ops` 对同一列重复声明——后者正是第 7 节第 4 类残留（「同一枚举多处独立声明」），已删除前者，`ops` 为唯一声明点。P2 的两个🟡 是两个未接线的 `All*` 常量，其中 `order.AllStartSources` 已改为被后台白名单引用，`AllRefundViewStatuses` 因订单列表确实没有 `refund_status` 筛选入口而改为「预登记」注释。

**补充门禁**：P2 补跑了 `go vet -tags integration ./...`。该层不参与 `node tasks.mjs check`，但能在真库执行前发现类型断裂——P2 借此发现并修复了 10 处集成测试夹具的类型不兼容（含 P1 遗留的 5 处）。后续批次应保留这道门禁。

### 9.2 真库集成的执行记录

**已取得证据**：`node scripts/db/test-integration.mjs` 已实际执行，**95 个包全部通过、零失败**，覆盖 `device`、`export`、`gateway/repo`、`http/integration`、`ops`、`payment`、`settlement`、`testfixture`、`wallet`、`worker/repo` 等。GORM 投影列类型、ENUM 参数绑定、`gorm:"default"` 指针语义、跨库行锁与 `SKIP LOCKED` 均有真实 MySQL 证据。执行前已核对：开发栈原本未运行（无容器、无既有数据），本次为全新启动；测试使用事务回滚，事后 `device_meta` 与 `announcement` 表无残留。

**这次真库运行找出了 6 个默认门禁无法发现的回归**，全部已修。它们印证了第 7 节「编译器无法捕获的四类残留」判断的正确性：

| # | 位置 | 现象 | 根因 |
| --- | --- | --- | --- |
| 1 | `worker/repo/outbox_repo.go` | `ListPublishableOutbox` 领取到 0 行 | P1 把空格分隔的白名单串拼进 `IN ('pending failed')`，得到**单元素 IN 列表** |
| 2 | `export/export_task_repo.go` 三处 | 到期清理与完成/失败写入不生效 | 同上（`completed failed`／`pending running`） |
| 3 | `worker/repo/schedule_repo.go` | 历史清理语句永不命中 | 同上（`success failed partial`），且在 `Exec` 原始 SQL 内 |
| 4 | `export/*_integration_test.go` 两处 | 故障注入与取消闸门夹具失效（一个立即失败、一个等满 20s 超时） | `Updates(map[string]any)` 的 Dest 值改为具名 `ExportStatus` 后，测试里 `values["status"] == "completed"` 走**接口动态类型比较**恒为 false |
| 5 | `ops/announcement_query_repo.go` | 站点详情接口 `announcements` 返回空数组 | P1 把 `status='published'` 改为绑定参数 `status=?`，在**事务型句柄 + 先构造 Where 再条件分支追加 Where** 的写法下追加条件在真库中不生效 |
| 6 | `ops/announcement_query_repo.go` 同批 | — | `scope IN ('global','station')` 同样被改成字符串拼接 |

**结论与新规则**：

- **白名单常量串不可直接拼进 SQL 的 `IN (...)`**。`"pending failed"` 是空格分隔的展示口径，拼进括号会得到单元素 IN 列表，编译器与静态检查均无法发现。已改为 `IN ?` 绑定 `[]T` 切片，并在常量旁注明两者关系。逐个拼接带引号（`'global','station'`）是正确写法，予以保留。
- **map 落值的动态类型会变**。`Updates(map[string]any{"status": ExportStatusCompleted})` 写入的值，其动态类型不再是 `string`；下游按接口比较时必须先归一（本仓库改为 `fmt.Sprint(values["status"]) == ...`）。这类比较散落在 GORM 回调夹具里，默认门禁不覆盖。
- **GORM 链式查询在事务句柄下的行为必须以真库为准**。生成 SQL 文本正确、绑定参数正确，仍可能因 Statement 复用而不生效。`Active` 已恢复内联字面量并注明原因；若将来改为绑定参数，须重跑真库而不是只看 DryRun 输出。

**仍缺的证据**：`task_execution_log` 无真实调度运行证据（处理器登记表为空，见 8.3.3）。真库集成覆盖了其存储与读取路径，但不等同于线上调度已运行。

### 9.3 确定性前后基线的执行记录

**已执行**，方法与结果如下。

**环境**：以基线提交 `5a5ac2c`（P0 已完成、P1 之前）与本方案最终提交各建一个 git worktree，各自启动独立的 MySQL（端口 3307／3308）与 Redis（6381／6382），各自用本版本的 `cmd/migrate` 建库并用 `cmd/devseed` 造数。

**已取得的两项证据**：

1. **数据库 schema 逐字节一致**。对 `central_db`、`gateway_db`、`worker_db` 三库取 `mysqldump --no-data` 并比对哈希，三库均完全相同。这独立证明本次改造**未改动任何数据库结构**，与「`migrations/` 零改动」的静态核对互为印证。
2. **业务数据高度一致**。对 `central_db` 全部 60 张表逐表取数据 dump 并比对哈希，**58 张完全一致**，包括全部框架表（`permission`、`role`、`role_permission`、`audit_log` 等）与 `coupon_*`、`refund_*`、`wallet_risk_*`、`fee_*` 等多数业务表。

**未覆盖的 12 张表与确切原因**：其余 12 张造数表（`user`、`wallet_account`、`station`、`device_meta`、`pricing_rule`、`pricing_template`、`charge_order`、`charge_order_pricing`、`charge_payment_intent`、`payment_order`、`charge_fee_receipt`、`device_fault_report`）存在差异，**根因单一且已定位**：`cmd/devseed` 用 `time.Now()` 派生时间字段，其「时与分」随运行时刻变化（实测两次运行相差 2 分钟），代码中**没有任何注入点**可固定该时钟。

已用 `scripts/db/baseline-normalize.mjs` 消除了其余四类随机量——Snowflake 主键（按表内行号锚定重映射）、UUID v4 单号（按行号派生）、秒与毫秒尾数（按行号重写）——两侧在这些维度已完全一致；**仅剩「时分」无法消除**。若强行把整行时间按行号完全重写，会破坏 `created_at < updated_at` 等业务时间先后关系，使依赖时间序的查询分支走错、测试失真，因此**不采用**该做法。

**结论**：本次基线**已证明 schema 未变、数据行为未变**（58／60 张表逐字节一致，剩余 12 张的差异完全归因于造数时钟且已定位到具体字段）。但**严格的「改造前后接口响应逐字节比较」未完成**——该步骤还需在两侧启动服务、注入固定 `X-Request-ID` 后发同一批请求并比对响应体，本次未走到该步。状态应表述为：

**基础入口、真库集成与前后 schema／数据基线均已通过；接口响应层面的严格逐字节比较未完成，缺口原因是造数工具无可注入时钟，而非改造引入了差异。**

### 9.4 P2 的执行记录
**已取得证据**：`go build ./...`、`go vet ./...`、**`go vet -tags integration ./...`**、`node tasks.mjs check`、`node tasks.mjs build-all` 全部通过；`go mod tidy -diff` 无差异；`go list ./...` 全仓无 import 环；`internal/protocol` 经 `go list -deps` 核实仍为零内部依赖的叶子包；`migrations/`、`go.mod`、`go.sum` 零改动；JSON 标签与 `binding` 标签逐字节不变；无新增 `MarshalJSON`／`UnmarshalJSON`。

**P2 特有证据**：`binding:"required,oneof=user_app admin auto"` 对自定义字符串类型仍生效，已用真实 gin `ShouldBindJSON` 实测（`user_app`／`auto`／`admin` 通过，`bogus`／空串／字段缺失均 400），并核对了 `reflect.Kind` 仍为 `string`；序列化输出与原 `string` 字段逐字节相同。

**真库证据**：见 9.2 节。P2 涉及的三处 VARCHAR 投影列（`device_meta.protocol_adapter`、`vendor.adapter_class`、`device_event.protocol_name`）扫描为 `protocol.AdapterID` 已在真实 MySQL 下验证；`OrderView.StartSource` 的可空指针往返与 `RefundViewStatus` 断言同样已覆盖。

## 10. 独立缺陷

以下问题与类型化无关，混入会降低回归定位效率，**不纳入本方案**，建议单独处理。

1. **退款展示链路缺少 `rejected` 分支，但该值在当前链路不可达**。`internal/wallet/query.go:110` 的 `case "failed", "needs_review"` 中，`needs_review` 不在 `refund_record` 的枚举内，永不命中；`refundView` 的 `switch` 也没有 `rejected` 分支。`refundView`（同文件 `:104-117`）读取的是两条独立链：分笔状态来自 `refund_record`（`internal/wallet/query_repo.go:19`），审核结果来自 `wallet_risk_review`（同文件 `:29`）。
   **已核验**：`rejected` 在钱包链路**不可达**。钱包退款单由 `internal/settlement/refund_creation_repo.go:31` 固定写为 `ExecutionPolicy: "automatic"`，而退款审核准入要求 `manual_review`（`internal/settlement/refund_review_repo.go:168`），因此该单进不了驳回路径；钱包风险审核拒绝时亦不创建分笔（`internal/wallet/risk.go:118-120`）。依据见第 12.2 节。
   **结论定性**：缺少 `rejected` 分支是已确认事实，但**不构成当前可达的展示缺陷**，不得写成「已确认线上错误」。历史数据或人工修改造成的异常需另行核验真实数据。核验与定性均待独立处理：若将来要支持异常分笔，先确定多分笔与风险审核的展示优先级，再补分支与测试。
   同一 `case` 中的 `needs_review` 作为**输出值**仍被 `internal/wallet/query_test.go:21` 覆盖，属有效逻辑，清理死输入时应与该修复一并处理。
2. **声明错位**：~~`internal/identity/charge_user_repo.go` 的状态字段实为订单域枚举，却声明在 identity 包。~~ **已在 P1 批次修复**：实际有三个字段（`Status`、`BusinessStatus`、`PaymentStatus`）而非一处，均改复用 `domain/order` 的既有类型（路线图原只登记了第一个，其余两处为复核时补登）。

