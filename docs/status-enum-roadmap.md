# 状态与枚举类型收敛路线图

状态：方案阶段，尚未实施。依据 2026-10-10 对 `backend/` 的全量盘点，覆盖三个数据库初始化文件、66 处 ENUM 列、227 处候选字符串字段与 111 个文件。

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

**各列来源与可信度**：「数据库列」与「存储」取自 `migrations/*/0001_init.sql`，已逐项核对；「Go 字段」为 2026-10-10 盘点定位，**实施前须逐条复核**；「目标类型与归属」已按第 5.2 节依赖路径校正。

### 8.1 P0 · 高重复声明，先验证模式

| 枚举 | 数据库列与存储 | 产生方式 | Go 字段（待复核） | 目标类型与归属 | 批次 | 完成 |
| --- | --- | --- | --- | --- | --- | --- |
| 订单状态 | `charge_order.status` ENUM 8 值 | 业务写入 | `order/order_schema.go:19` 等 6 处，`settlement/` 4 处 | `domain/order.OrderStatus`（无冲突） | P0 | ☐ |
| 订单业务状态 | `charge_order.business_status` ENUM | **数据库生成，只读** | `order/order_schema.go:20` | `domain/order.OrderBusinessStatus` | P0 | ☐ |
| 订单支付状态 | `charge_order.payment_status` ENUM 4 值 | 业务写入 | `order/order_schema.go:21` | `domain/order.OrderPaymentStatus` | P0 | ☐ |
| 支付关联业务类型 | 三表 `biz_type` ENUM 2 值 | 业务写入 | `payment/money_schema.go:12` 等约 8 处 | `domain/money.PaymentBizType` | P0 | ☐ |
| 支付单状态 | `payment_order.status` ENUM 6 值 | 业务写入 | `payment/money_schema.go:20`；跨域 `wallet/wallet_account_repo.go`、`order/order_view_repo.go` | `domain/money.PaymentStatus`（wallet／order／marketing 冲突） | P0 | ☐ |
| 退款单状态 | `refund_record.status` ENUM 5 值 | 业务写入 | `settlement/money_schema.go:48`；跨域 `wallet/query_repo.go:19` | `domain/money.RefundStatus`（wallet／payment 冲突） | P0 | ☐ |
| 券发放状态 | `coupon_grant.status` ENUM 3 值 | 业务写入；`expired` 另由 `marketing/query.go:84` 派生展示 | `marketing/coupon_repo.go:28`；跨域 `identity/charge_user_repo.go:241` | `domain/marketing.GrantStatus`（identity 冲突） | P0 | ☐ |
| 券发放来源 | `coupon_grant.grant_source` ENUM 5 值 | 业务写入 | `marketing/activity_grant.go:38`（`Source string`） | `domain/marketing.GrantSource`（与状态分立，语义不同） | P0 | ☐ |
| 故障工单状态 | `device_fault_report.status` ENUM 4 值 | 业务写入 | `device/fault_repo.go:19`、`fault_casework.go:22` | `device.FaultStatus`（已有无类型常量 `fault.go:10-13`，判定 `:20-34`） | P0 | ☐ |
| 刷卡操作类型与状态 | `card_operation.kind`／`status` ENUM 2 值／3 值 | 业务写入 | `card/card_repo.go`、`charging/query/user.go:79,82`、`contracts/central/card.go:28,31` | **`domain/card.CardOperationKind`**／`CardOperationStatus`（contracts 不得引用 card） | P0 | ☐ |
| 扫码结果种类 | **无数据库列** | **仓储逻辑派生** `device`／`port` | `domain/money/scan.go:16`、`gateway/repo/scan_repo.go:31`、`contracts/gateway/scan.go:28` | `domain/money.ScanKind`（与端口状态分离，二者不建立相互映射） | P0 | ☐ |
| 设备端口状态 | `gateway_db.device_port.status` ENUM 5 值 | 业务写入＋设备上报 | `gateway/repo/gorm_models.go:28`、`scan_repo.go:21`、`contracts/gateway/scan.go:21` | `domain/money.PortStatus`（**单一归属**） | P0 | ☐ |

### 8.2 P1 · 主体持久化枚举

| 枚举 | 数据库列与存储 | 产生方式 | Go 字段（待复核） | 目标类型与归属 | 批次 | 完成 |
| --- | --- | --- | --- | --- | --- | --- |
| 用户状态与性别 | `user.status`／`gender` ENUM 2 值／3 值 | 业务写入 | `identity/user_schema.go:21`、`charge_user_repo.go:23`；跨域 card／ops／payment／wallet | `domain/identity.UserStatus`／`Gender`（wallet 冲突） | P1 | ☐ |
| 设备运营状态 | `device_meta.status` ENUM 4 值 | 业务写入 | `device/device_repo.go:30`、`operation_repo.go:54`、`domain/money/scan.go:11,19` | **`domain/devices.DeviceMetaStatus`**（device→ops 冲突，且 domain 不得引用 device） | P1 | ☐ |
| 告警状态与级别 | `alert_event.status`／`severity` ENUM 4 值／3 值 | 业务写入 | `device/alert_repo.go:72`；`ops/dashboard_repo.go:89` 引用常量 | **`domain/devices.AlertStatus`**／`AlertSeverity`（device→ops 冲突） | P1 | ☐ |
| 充电命令状态 | `charge_command.status` ENUM 5 值含 `stopping` | 业务写入＋设备回执 | `gateway/repo/gorm_models.go:67`；经 contracts `charging.go:99` | **`domain/devices.ChargeCommandStatus`**（gateway／contracts 冲突） | P1 | ☐ |
| 停机命令状态 | `charge_stop_command.status` ENUM 4 值无 `stopping` | 同上 | `gateway/repo/gorm_models.go:106`；经 contracts `charging.go:86` | **`domain/devices.StopCommandStatus`**（独立类型，不与启动命令取并集） | P1 | ☐ |
| 数据范围类型 | `admin_data_scope.scope_type` ENUM 2 值 | 业务写入 | `ops/account_repo.go`、`account_management_repo.go`；`domain/operations/scope.go:5-6,16` 已有常量与字段 | **`domain/operations.ScopeType`**（就地升级已有 `ScopeStation`／`ScopeVendor`） | P1 | ☐ |
| 刷卡事件投递 | `card_event_delivery.status` ENUM 2 值 | 业务写入 | `gateway` 投递仓储 | `gateway.CardDeliveryStatus` | P1 | ☐ |
| 网关设备状态 | `gateway_db.device.status` ENUM 3 值 | 业务写入 | `gateway/repo` 会话与回填 | `gateway.DeviceStatus`（不与中央四值混用） | P1 | ☐ |
| 厂商状态 | `vendor.status` ENUM 2 值 | 业务写入 | `gateway/repo/provision_repo.go:48,149,167,285`；**经 contracts** `contracts/gateway/provision.go:29-68`；`device/vendor.go:15-24`；`integration/gateway/vendor.go:23` | **`domain/devices.VendorStatus`**（contracts 与客户端均消费，筛选空串仍表示不筛选） | P1 | ☐ |
| 厂商传输配置 | `vendor.protocol` ENUM 3 值含 `hybrid` | 业务写入 | `gateway/repo/provision_repo.go:147,283`；经 `contracts/gateway/provision.go:57` | **`domain/devices.VendorProtocol`**（不与会话传输取并集） | P1 | ☐ |
| 会话传输方式 | `device_session.protocol` ENUM 2 值 `tcp`／`mqtt` | 业务写入＋连接建立 | `gateway/repo/session.go:22`；`protocol/session_audit.go:5-14` | **复用 `protocol.Transport`**（已有 `TransportTCP`／`TransportMQTT`） | P1 | ☐ |
| 会话关闭原因 | `device_session.close_reason` VARCHAR | 业务写入 | `gateway/repo/session.go:32`；`protocol/session_audit.go:25` | **复用 `protocol.CloseReason`** | P1 | ☐ |
| 设备事件类型 | `device_event.event_type` VARCHAR | 协议上报 | `gateway/repo/gorm_models.go:129` | **复用 `protocol.EventType`**（已有 11 个常量） | P1 | ☐ |
| 网关 Outbox | `gateway_db.event_outbox.status` ENUM 3 值 | 业务写入 | `gateway/repo` outbox 仓储 | `gateway.OutboxStatus` | P1 | ☐ |
| 设备计费方式 | `device_meta.charge_mode` **VARCHAR** | 业务写入 | 设备仓储投影 | 复用 `charging.ChargeMode` | P1 | ☐ |
| 站点状态 | `station.status` ENUM 3 值 | 业务写入 | `device/station_repo.go:20`、`station.go:97` | `device.StationStatus` | P1 | ☐ |
| 故障类型与来源 | `device_fault_report.fault_type`／`report_source` ENUM 5 值／3 值 | 业务写入 | `device/fault_report.go:23` | `device` 各自类型 | P1 | ☐ |
| 设备导入任务 | `device_import.status` ENUM 3 值 | 业务写入 | `device/device_import.go:22` | `device.ImportStatus` | P1 | ☐ |
| 设备事件 Outbox | `event_outbox.status`、`admin_event_outbox.status` ENUM 3 值 | 业务写入 | `delivery/events.go` 与各 outbox 仓储 | 各自所属包 | P1 | ☐ |
| 支付方式 | `payment_order.pay_method` ENUM 4 值 | 业务写入 | `payment/money_schema.go:15`；跨域 wallet／order | `domain/money.PayMethod`（wallet 冲突） | P1 | ☐ |
| 支付意图状态 | `charge_payment_intent.status` ENUM 5 值 | 业务写入 | `payment/money_schema.go:54`；跨域 charging／identity／order | `domain/money.PaymentIntentStatus`（order／identity 冲突） | P1 | ☐ |
| 退款执行策略 | `refund_record.execution_policy` ENUM 2 值 | 业务写入 | `settlement/money_schema.go:37`；跨域 wallet | `domain/money.ExecutionPolicy`（wallet 冲突） | P1 | ☐ |
| 计费任务状态 | `charge_billing_job.status` ENUM 3 值 | 业务写入；**跨域读模型** | `settlement/billing_job_repo.go:397`、`manual_repo.go:35`；`charging/query/user_repo.go:28-32` 的 `out.BillingStatus = job.Status`，`user.go:94,136` | `settlement.BillingJobStatus`（charging→settlement 直连，归属可留；**须同步类型化 charging 侧 `BillingStatus` 字段**） | P1 | ☐ |
| 账单状态 | `charge_bill.status` ENUM 3 值 | 业务写入 | `settlement/bill_repo.go:37` | `settlement.BillStatus` | P1 | ☐ |
| 计量核实状态 | `charge_meter_review.status` ENUM 3 值 | 业务写入 | `settlement/meter_review_repo.go:22` | `settlement.MeterReviewStatus` | P1 | ☐ |
| 人工兜底单状态 | `manual_fee_review.status` ENUM 2 值 | 业务写入 | `settlement/refund_review_list_repo.go:47` | `settlement` 包内 | P1 | ☐ |
| 发票类型与审核 | `invoice_request.invoice_type`／`review_status`、`invoice_admin_review.review_status` | 业务写入 | `settlement/invoice.go:38,48`、`invoice_repo.go:44,53` | `settlement` 各自类型（两张表集合不同） | P1 | ☐ |
| 钱包账户状态 | `wallet_account.status` ENUM 2 值 | 业务写入 | `wallet/wallet_account_repo.go:18` 等 4 处；跨域 card／identity | `wallet.WalletStatus`（无冲突） | P1 | ☐ |
| 钱包流水方向与业务类型 | `wallet_txn.direction`／`biz_type` ENUM 2 值／7 值 | 业务写入 | `wallet/wallet_ledger_repo.go:62`（写入 `:73`） | `wallet` 各自类型（见 5.3） | P1 | ☐ |
| 风险冻结记录 | `risk_freeze_log.status` ENUM 2 值 | 业务写入 | `wallet/admin_wallet_risk_repo.go:32` | `wallet.FreezeStatus` | P1 | ☐ |
| 在线卡状态 | `online_card.status` ENUM 4 值 | 业务写入 | `card/card_schema.go:16`、`admin.go:27` | `card.CardStatus`（跨域消费方按 5.2 复核） | P1 | ☐ |
| 营销模板与活动状态 | `coupon.status`、`coupon_activity_rule.status` ENUM 2 值 | 业务写入 | `marketing/coupon.go:35,43`、`activity.go:21,37` | **分立**为 `marketing` 两个类型 | P1 | ☐ |
| 优惠类型 | `coupon.discount_type` ENUM 3 值 | 业务写入 | `marketing/coupon.go:27` | `marketing.DiscountType` | P1 | ☐ |
| 活动触发类型 | `coupon_activity_rule.trigger_type` ENUM 4 值 | 业务写入 | `marketing/rules_coupon_activity.go:14-19` | `marketing.TriggerType`（已有无类型常量） | P1 | ☐ |
| 反馈状态与分类 | `feedback.status`／`category` ENUM 3 值 | 业务写入 | `order/feedback_repo.go:20,77` | `order.FeedbackStatus`／`FeedbackCategory`（属 order 包，非 ops） | P1 | ☐ |
| 公告状态与范围 | `announcement.status`／`scope` ENUM 3 值 | 业务写入 | `ops/announcement_repo.go:18`、`announcement.go:16` | `ops.AnnouncementStatus`／`Scope`（无冲突） | P1 | ☐ |
| 后台账号状态 | `admin_user.status` ENUM 3 值 | 业务写入 | `ops/account_repo.go:24`、`account_management_repo.go:27` | `ops` 包内（无冲突） | P1 | ☐ |
| 定价规则与模板状态 | `pricing_rule.status`、`pricing_template.status` ENUM 2 值 | 业务写入 | `pricing/pricing_scope_repo.go:14`、`scheme.go:42` | `pricing` 各自类型 | P1 | ☐ |
| 导出任务状态与格式 | `export_task.status`／`file_format` ENUM 5 值／2 值 | 业务写入 | `export/export_task_repo.go:26`、`service.go:56` | `export` 各自类型 | P1 | ☐ |
| 任务执行状态与触发来源 | `task_execution_log.status`／`triggered_by` ENUM 4 值／2 值 | 业务写入 | `worker/repo/schedule_repo.go:33,132` | `worker` 各自类型 | P1 | ☐ |

### 8.3 P1 · 故障事件：按操作分别登记可写集合

`device_fault_report_event.event_type` 为六值 ENUM（`migrations/central_db/0001_init.sql:438`）。**四值不是全局可写集合**：用户报修入口 `internal/device/user_fault_repo.go:36` 实际写入 `reported`，且 `from_status` 写空串。

| 集合 | 取值 | 依据 |
| --- | --- | --- |
| 数据库合法集合 | `reported`、`dispatched`、`reassigned`、`fixed`、`closed`、`migration_baseline` | `migrations/central_db/0001_init.sql:438` |
| 用户报修操作写入 | `reported`（`from_status` 空串、`to_status` `open`） | `internal/device/user_fault_repo.go:36` |
| 后台处理操作写入 | `dispatched`、`reassigned`、`fixed`、`closed` | `internal/device/fault_repo.go:67` 注释与写入 |
| `migration_baseline` | **来源待核**：当前无写入路径，保留为兼容读取 | — |

- 目标类型 `device.FaultEventType`。**不提供以四值为准的统一写入校验**；若增加判定方法，按具体操作命名（如 `FaultEventType.BackgroundWritable()`）。
- 事件的 `FromStatus`／`ToStatus`（`internal/device/fault_repo.go:68-69`，VARCHAR `:439-440`）**复用 `FaultStatus`**，保留现有空串与可空表示，**不得新增拒绝或转换为 `null`**。

### 8.4 P2 · 派生值、外部输入与受限标签

此类不设固定先后顺序，允许同一枚举的存储类型、读模型与映射在同一批完成。持久化与展示状态需分立时定义显式映射，保持原值、未知值、空值与时间判定行为。

| 枚举 | 来源与存储 | 产生方式 | Go 字段（待复核） | 目标类型与归属 | 批次 | 完成 |
| --- | --- | --- | --- | --- | --- | --- |
| 订单启动来源 | SQL `CASE` 派生，**无数据库列** | 逻辑派生 `payment`／`balance`／`card` | `order/order_view_repo.go:28`（`orderStartSourceSQL`，`:66`） | `order.OrderStartSource` | P2 | ☐ |
| 订单退款展示状态 | SQL `CASE` 派生，**无数据库列** | 逻辑派生 | `order/order_view_repo.go:39`（`:72`） | `order.RefundViewStatus` | P2 | ☐ |
| 钱包退款展示状态 | 无数据库列 | 内存派生 6 值 | `wallet/query.go:69`（`refundView`，`:104-125`） | `wallet.RefundViewStatus` | P2 | ☐ |
| 渠道退款状态 | 无数据库列 | **外部契约输入** | `payment/channel_refund.go:30` | `payment.ChannelRefundStatus` | P2 | ☐ |
| 事件投递对象与来源 | 无数据库列 | **外部契约输入** | `delivery/events.go:19,35,40,52` | `delivery` 各自类型 | P2 | ☐ |
| 网关停止请求来源 | contracts 输入 | **外部契约输入** `user_app`／`admin`／`auto` | `contracts/gateway/charging.go:78` | contracts 内类型，保留 `required` 与 `oneof`；不与 worker 开放 Source 标签混同 | P2 | ☐ |
| 刷卡推进结果 | contracts 输入 | **外部契约输入** `retry`／`done` | `contracts/gateway/card.go:50-52` | contracts 内类型，保留绑定校验；不复用投递状态集合 | P2 | ☐ |
| 核销业务类型 | `coupon_redemption.biz_type` **VARCHAR** | 业务写入，可写子集仅 `charge` | `marketing/coupon_redeem_repo.go:64,78` | 复用 `domain/money.PaymentBizType`，**不因复用开放钱包充值核销**（见 5.3） | P2 | ☐ |
| 适配器标识 | `device_meta.protocol_adapter`、`vendor.adapter_class`、`device_event.protocol_name` 均 VARCHAR | 业务写入，当前受支持集合仅 `dc589` | `device/import.go:125-136`、`domain/charging/capabilities.go:8-14`、`protocol/adapter.go:12,51` | **新建 `protocol.AdapterID`**（明确为新增类型，不得列为已有样板），同批覆盖上述三处及 `Registration.Protocol`、`Event.Protocol`、`Adapter.Name()` 与能力判定参数；不与 `tcp`／`mqtt`／`hybrid` 混用 | P2 | ☐ |

**订单启动来源的空值规则**：展示字段为 `*string`，无来源时为 `null`；筛选参数为空串仍表示不筛选，两种语义不得合并。

**扫码种类与端口状态不建立相互映射**：`port` 表示解析到单端口，不能推出 `idle`；`device` 返回多个各有状态的端口。已核实 `internal/gateway/repo/scan_repo.go:54,74` 将 `port.Status` 原样传入，未按 `idle` 过滤。保留现有结果组装与可用性计算，未知端口状态按原行为处理。

### 8.5 登记但不类型化

| 项 | 位置 | 理由 |
| --- | --- | --- |
| Outbox 来源标识 | `internal/worker/outbox/publisher.go:16` | 开放标签，注释为「非空即可」，非有限集合 |
| 模拟器行为类型 | `internal/gateway/simulator/dc589/behavior.go:21` | 工具私有，无数据库列与对外路径，保留在包内 |
| 退款原因等自由文本 | 多处 `Reason` 字段 | 自由文本，非枚举 |

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

## 10. 独立缺陷

以下问题与类型化无关，混入会降低回归定位效率，**不纳入本方案**，建议单独处理。

1. **退款展示链路缺少 `rejected` 分支，但该值在当前链路不可达**。`internal/wallet/query.go:110` 的 `case "failed", "needs_review"` 中，`needs_review` 不在 `refund_record` 的枚举内，永不命中；`refundView` 的 `switch` 也没有 `rejected` 分支。`refundView`（同文件 `:104-117`）读取的是两条独立链：分笔状态来自 `refund_record`（`internal/wallet/query_repo.go:19`），审核结果来自 `wallet_risk_review`（同文件 `:29`）。
   **已核验**：`rejected` 在钱包链路**不可达**。钱包退款单由 `internal/settlement/refund_creation_repo.go:31` 固定写为 `ExecutionPolicy: "automatic"`，而退款审核准入要求 `manual_review`（`internal/settlement/refund_review_repo.go:168`），因此该单进不了驳回路径；钱包风险审核拒绝时亦不创建分笔（`internal/wallet/risk.go:118-120`）。依据见第 12.2 节。
   **结论定性**：缺少 `rejected` 分支是已确认事实，但**不构成当前可达的展示缺陷**，不得写成「已确认线上错误」。历史数据或人工修改造成的异常需另行核验真实数据。核验与定性均待独立处理：若将来要支持异常分笔，先确定多分笔与风险审核的展示优先级，再补分支与测试。
   同一 `case` 中的 `needs_review` 作为**输出值**仍被 `internal/wallet/query_test.go:21` 覆盖，属有效逻辑，清理死输入时应与该修复一并处理。
2. **声明错位**：`internal/identity/charge_user_repo.go:59` 的状态字段实为订单域枚举，却声明在 identity 包。收敛时应移交 order 域。

