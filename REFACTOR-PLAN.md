# charge-pilot 分层重构方案

> 状态：**已被 2026-10-03 的 use case 重构取代**。本文件记录的是上一次
> （按 domain/application/infrastructure/interface 四层）的方案，
> 当时的结构与依赖规则已不再成立。
>
> 当前结构的权威描述在 [AGENTS.md](AGENTS.md)，本次重构的诊断、实施记录、
> 判断修正与遗留缺口在 [docs/target-structure.md](docs/target-structure.md)。
> 本文件保留作为历史记录，不再更新。

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

### 2.4 目录结构

目录树只有一份，在 [AGENTS.md](AGENTS.md#工具与代码风格)，并由
`make layers` 的 `scripts/check/doc_tree.py` 机检：树里写了而仓库里没有的
文件会报违规。

本文件原先也放了一份，两处靠人同步——正是这种"靠人记住"的约定最先失效：
迁移期间两边悄悄分叉，读文档的人按图找代码必然扑空。改为单一来源。

新增或迁移任何文件都先落进 AGENTS.md 的树，再改代码。


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

### 阶段 6：devices + operations（已做）

`domain/devices/`、`domain/operations/` 与 admin 层不再出现的 gorm 模型
已就位；`central/admin` 的内联 SQL 也在本轮全部下沉完毕。

实际做法与原计划有一处偏离：**没有**为每个后台列表在 `application` 新建用例。
后台接口大多是单表读取加一次联表回填，套一层用例只会平移代码而换不来
业务不变量的表达。分界按"有没有跨聚合不变量"划：

- 有不变量的留在处理器（角色判定、状态机、重放裁定、审计编排），
  数据访问交给 `infrastructure/database` 的 store 方法；
- 没有不变量的（列表、详情、单条读取）整体进 store，处理器不碰 gorm。

判据是**表名只出现在 `infrastructure/database`**。原先 `internal/central/admin`
下有 24 个文件在处理器里拼 SQL，现已归零。

本轮顺带修掉的既有缺陷（都不是重构引入的）：

| 缺陷 | 表现 | 原因 |
|---|---|---|
| 对账差异金额全是零 | 运营看到的每条差异金额都是 0 | 领域类型缺 JSON tag，存 `ChannelCents`、读 `channel_cents`，解码不报错 |
| 站点导出多一列空值 | 每次导出恒定带一列空 `编码` | `code` 列早已删除，导出列清单没跟上 |
| 在线卡绑定对真卡号不可用 | 任何真实卡号都被判非法 | 卡号校验用了 `ParseUint(..., 32)`，只容得下约十位 |
| 目录树护栏无判别力 | 从未真正抓到过任何东西 | 路径解析失败时退回「按文件名在别处找」 |

每条都补了回归用例或在注释里固定了前提。

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
3. **分层的机器护栏是四道**：领域层零框架、`internal/central` 下不得再出现
   gorm 模型、OpenAPI 文档与注册端点双向一致、目录树与文件系统一致。
   前两道随重构逐步加上，后两道是补文档时发现漂移才补的——
   约定只有被机器守着才不会烂。

---

## 第八部分：维护约定

- **目录结构只有一份**，在 AGENTS.md，由 `doc_tree.py` 机检。改结构时先改树
  再改代码；机检会在你漏改时失败。
- 阶段完成后更新本文件第五部分对应小节：把「计划」改写为「已做」并记录
  实施中发现的计划外裁决，与第一轮的执行进度记录方式一致。
- 结构图不是装饰。新增文件前先在图里找到它的位置；图里没有，说明缺的是
  一次结构决策，不是随手新建目录。
