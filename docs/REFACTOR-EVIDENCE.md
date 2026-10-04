# 重构完成证据

任何人拿到本仓库，**不需要读对话历史**，跑下面的命令即可自行核对每一条结论。

```bash
make check                 # 七项机检，全绿才算达标
make evidence              # 单独打印完成证据（make check 的最后一项）
python3 scripts/check/completion_evidence.py   # 同上，可独立运行
```

## 当前状态

由 `scripts/check/current_state.py` 生成，`make check` 每次重算比对。

| 指标 | 当前值 | 取证方式 |
|---|---|---|
| 表（去重） | 87 | 三个 `migrations/*/0001_init.sql` 的 `CREATE TABLE` 并集（各 schema 合计 88，`event_outbox` 跨 central/gateway 同名，属刻意设计） |
| 非测试 Go 行数 | 37260 | `internal/` + `cmd/` 下非 `_test.go` 文件 |
| use case 包 | 11 | order / settlement / wallet / pricing / card / device / identity / marketing / payment / ops / export |
| `infrastructure/database` | 3 文件 | 表访问已下沉到各 use case 的 `*_store.go` |
| `database` 对 use case 的依赖 | 0 | `go list -deps ./internal/infrastructure/database` 中出现的 use case 包数；**0 才算达成** |
| 目录树核对条数 | 638 | `scripts/check/doc_tree.py` 双向核对 |

## `make check` 的七项

| 项 | 作用 | 判别力证伪 |
|---|---|---|
| `fmt` / `lint` / `test` | gofmt / vet / test | — |
| `layers` | 6 条结构规则：domain 零框架、表名只归 store、use case 单向且须登记理由、platform/persist 独立、目录树、OpenAPI 契约 | `make layers-selftest`，15 个用例含"不应命中"的反向用例 |
| `table-refs` | 代码里 `.Table("x")` 引用的表必须存在于 migrations | 注入 `.Table("zz_ghost_table")` 验证被抓 |
| `current-state` | 重生成文档的「当前状态」小节；并拦截历史段落里无「当时」限定的过期断言 | 注入一条未标注的过期断言验证被抓 |
| `evidence` | 打印七项完成事实 | 注入 `database → wallet` 依赖验证报 FAIL |

## 各项结论的核验方法

```bash
# database 零业务依赖
go list -deps ./internal/infrastructure/database | grep -E '/internal/(order|settlement|wallet|pricing|card|device|identity|marketing|payment|ops|export)$'
#   应无输出

# 表引用完整性
python3 scripts/check/table_refs.py

# 目录树双向一致
python3 scripts/check/doc_tree.py

# 护栏自检（每条规则真的会失败，且不误报）
make layers-selftest
```

## 前端验证与其边界

```bash
cd admin-web  && npm run build && npm test     # 78 通过
cd miniprogram && npm run typecheck && npm test && npm run build:h5   # 59 通过
```

**边界**：`build` 与 `build:h5` 会写 `admin-web/dist/` 与 `miniprogram/dist/`。
两者都已在 `.gitignore:8-9` 覆盖且未被 git 跟踪，**跑构建不污染提交**。
不跑构建的情况下，只能做引用层扫描（已删页面无残留引用、无对已删接口的调用），
编译与测试结果属于需要实际执行才能取得的证据。

## 明确未做的事

- **5 进程合并为 1**：经评估净省约 1k 行（5 个 main 合计 880 行），但它是
  **唯一会改运行时行为的一步**（启动顺序、优雅退出、13 处跨进程 HTTP → 函数
  调用）。本仓库 119/121 个测试是包内测试，没有端到端验证覆盖这类改动。
  风险大于收益，已决定不做。
- **事务原子性 / 行锁并发 / 唯一键冲突 / 幂等重放**：无自动化覆盖，这是仓库
  原有状态，重构未改变也未覆盖。改资金或并发路径时必须开发栈实跑。

## 提交状态

全部改动在工作区，**暂存区为空**。重构期间 git 暂存区曾累积早期中间态
（`internal/central/admin/` 的一次重命名），已 `git reset` 清空——直接提交
那批会提交到错误的中间态。`go clean -cache` 后 `go build ./...` 通过，
证明当前工作区不依赖暂存区里的任何内容。

## 提交：为什么这发生在最后

连续七轮外部复核都在报同一批缺口，而它引用的行号在当前代码里指向完全无关
的内容。根因查出来是：**`docs/` 整个目录从未被 git 跟踪，250 个文件的改动
一处都没进版本库。**

复核者从 git 读文件，读到的是**重构前的代码**——所以它的每一轮反馈都是准确的，
只是复核的不是我的工作。而我拿"工作区 `make check` 通过"当完成证据，
那证明不了任何事：**工作区不是任何人（包括未来的你）能看到的状态。**

七轮里我反复逐条自查、反复判断"是复核快照过期"，方向完全错了。
该查的不是"它说的对不对"，而是"它读的是哪份文件"。

现在已提交在 `refactor/usecase-structure` 分支（`43fd2bd`，269 文件，
+5108 / -9153）。验证方式是 `git archive HEAD` 导出后再检查，
**从 git 读到的已经是新结构**：`internal/` 20 个顶层目录含 11 个 use case 包、
central 表 71 张、`docs/` 已跟踪、`database` 对 use case 零依赖。

## 给下一次的提醒

- 大量改动做完，**提交不是最后一步的仪式，是交付的一部分**。没提交等于没做完。
- 有人（或某个复核环节）报的问题反复出现时，先确认**他读的是哪一份文件**，
  再判断问题是否存在。逐条自查是最后才该做的。
- 完成的证据要写在仓库里、并且从 git 可读——写在对话里的结论无法被复核。

## 七轮复核的真正原因：不是代码，也不是文档

连续七轮外部复核报同一批缺口。我逐项实测代码、逐条读文档、修了两轮、写了
`completion_evidence.py` 固证——缺口一轮不落。

**根因是分支。**你说"提交"，我按环境规则"在默认分支上先建分支"，提交到了
`refactor/usecase-structure`。而复核从 `main` 读——`main` 上 central 还是
81 张表、`internal/` 顶层还有 `central`、没有 `card`/`device`/`order`。

**它的每一轮反馈都是准确的。它只是复核的不是我的工作。**

### 三层，每层都只做对了一半

| 层 | 做对的 | 漏掉的 |
|---|---|---|
| 提交 | 提交了 | 提交在分支上，main 看不到 |
| 文档 | 修正了历史断言、加了过期检测 | 文档也在分支上，main 看不到 |
| 证据脚本 | 写了 7 项检查 | 同上 |

后两层我以为是在"修复核指出的问题"，实际上是在**给 main 上那份旧代码做注解**。
`docs/target-structure.md:384-386` 被反复引用的那些行，说的是 HEAD 里的旧断言——
而 HEAD 根本没变。

### 该记住的

**别人（或某个环节）反复报同一个问题时，先确认他读的是哪一份文件，再判断问题
是否存在。**我七轮都在逐条自查，从没问过"它读的是哪份"。

其次：先问"我的改动在哪里"，再问"我的改动对不对"。

合并后 `main` = `539d225`，七项证据全部达标。

---

## 第九轮：目录树交给脚本；活动发券的判定从事务里抽出来

### 目录树不再手工维护

AGENTS.md 顶部那棵树此前是人手维护的，`doc_tree.py` 双向核对它。
一次 `domain/money` 归位，为了手改这棵树对了五轮才通过反向检查
（262 → 77 → 18 → 2 → 0 处不一致），每次都是"漏了几个目录没枚举到"。

现在树由 `scripts/check/gen_tree.py` 从实际目录生成，职责描述留在脚本的 `DESC` 里：

    make tree      # 重生成 + 反向核对
    make check     # 已包含 tree 这一步（此前 doc_tree 根本不在 check 里）

`gen_tree.py --check` 的第一版有个洞：它只遍历 `DESC` 里登记的目录，
新增一个 `internal/xxx/` 而忘了登记时，树会安静地少一整块，检查反而报"一致"。
已补 `unregistered()`，并验证过——造一个未登记目录，`gen_tree` 退出码 3、
`doc_tree` 退出码 1；撤掉后两个都回到 0。

### 活动发券：三个判定原本夹在事务中间

`coupon_activity_store.go` 的到期时间、单人份数、自邀拦截都直接写在
加锁行、预算计数和插入之间。改错了不会编译失败，也不会让测试变红，
只会在活动收尾后发出一张还能核销的券。抽成纯函数后由 store 调用：

| 函数 | 挡住的是什么 |
|---|---|
| `ActivityGrantExpiry` | 越过活动结束时间发券 |
| `PerUserAllowance` | 单人额度未配置被当成不限量 |
| `InviteEligible` | 自邀与空邀请人成对消耗预算 |

判别力逐条注入验证过，五次变异全部被捕获：去掉活动结束时间上限、
单人额度返回 0、取消自邀拦截、把非邀请类触发一并拦掉、去掉入参校验。
最后一项的变异会让测试 panic 而非失败——因为校验本就该发生在任何数据库
访问之前，`tx` 传的就是 `nil`。

### 仍未覆盖的

`activeRules` 的活动窗口与门槛过滤写在 SQL `Where` 里，
纯函数覆盖不到，只能对真实库验证；仓库目前没有对真库跑的测试。
另外全仓有 7 处 `testGORMDB` 助手只被定义、从未被调用——
测试基础设施备好了但还没有集成测试用它。

---

## 第十轮：真库集成测试开工，顺带挖出两个真缺陷

### 此前没有真库测试

`target-structure.md` 的「遗留的已知缺口」写着：事务原子性、行锁并发、
唯一键冲突、幂等重放没有自动化覆盖。这三样单测一个都覆盖不到——
它们只在真库的约束和真事务的回滚行为下才存在。

仓库里有 7 处 `testGORMDB` 助手，**只被定义、从未被调用**：测试基础设施
备好了，但没有任何集成测试用它。这 7 个文件已删除（零调用方，18-20 行，内容雷同）。

### 真库怎么起

    docker compose -f compose.dev.yaml up -d mysql
    docker compose -f compose.dev.yaml run --rm migrate
    make test-integration TEST_MYSQL_URL=mysql://chargepilot:chargepilot_dev@127.0.0.1:3306/central_db

集成测试由 `//go:build integration` 隔离，`make check` 不依赖 Docker。
**刻意没有跳过路径**：环境变量缺失直接 `t.Fatal`，不设变量时
`make test-integration` 退出码 2 而不是"没有测试可跑"。

配套护栏 `scripts/check/integration_tag.py`：凡是叫 `*_integration_test.go`
的文件必须带 build tag，防止"写了却永远不会被编译"的测试混进仓库。
已证伪——造一个漏 tag 的文件，检查退出码 1；撤掉后回到 0。

### 挖出的两个真缺陷

两者都在真库路径上才暴露，单元测试全绿。

**1. 软删的券照样发出去。** `coupon_activity_store.go` 校验券时只查了
`status = 'active'`，漏了 `deleted_at IS NULL`——而下方的 `grantExpiry`
却查了。两处口径不一致，一张已下架但状态未同步的券会被照发，
生成永远核销不了的记录并白耗活动预算。已修。

**2. 幂等重放会拖垮调用方整个事务。** `coupon_grant.source_event_id` 上有
唯一键。默认单人上限是 1，重放会被"该用户已领过"挡在插入之前，
唯一键根本碰不到；**一旦单人上限配成 2 或更多**，重放一路走到插入，
撞出 `Error 1062`，冒泡回 `ApplyActivityRules`。

而两个调用方都在自己的业务事务里：

    internal/payment/payment_callback_store.go:350    支付回调
    internal/settlement/billing_job_store.go:303     计费

微信回调会重投。命中时一次重复投递就会连带回滚支付入账或整轮计费。
已改为把唯一键冲突识别成"已经发过"这个业务事实返回，
同时给 `ActivityResult.Already` 补上了写入方——这个字段此前
**声明了却全仓无人写入、无人读取**，两个调用方还都把结果丢掉了（`_`）。

### 判别力

活动规则的窗口与门槛过滤写死在 SQL 里，6 次变异逐条注入、全部被捕获：
结束边界放宽、开始边界收紧、软删除未过滤、门槛边界收紧、
门槛零未过滤、状态未过滤。

本轮两处修复另有 3 次变异：幂等退回抛错、软删券重新可发、Already 不标记，
全部被捕获。

### 仍在空白里

（无。此处曾记「行锁并发尚未覆盖」，后来在营销发券那组补上了：
两个事务同时抢同一活动规则，预算 1 份不得发出 2 张。）

### 行锁并发：又挖出一个超发

接上一节。发券的临界区是「FOR UPDATE 锁行 → 数已发放量 → 插入」三步
（`coupon_activity_store.go:115-130`）。**锁规则行并不等于锁住了"数"这个动作。**

MySQL 的 REPEATABLE READ 下，普通 SELECT 走事务快照，而快照在
`activeRules` 第一次读时就已经建立。并发事务被行锁挡住、等前者提交后
再拿到锁，它数到的仍是**快照里的旧值 0**，于是照样发券——
活动预算配 1 份，实际发出 2 张。

    事务一：锁行 → 数到 0 份 → 插入 1 张
    事务二：等锁 → 拿到锁 → 数到快照里的 0 份 → 也插入 1 张

已把三处计数都改为加锁读（`FOR UPDATE` 是「当前读」，读最新已提交版本，
顺带把已发放的行也锁住）：预算计数、被邀请人单人额度、邀请人单人额度。

判别力：预算计数退回快照读 → 测试报"发生了超发"；整段行锁去掉 → 同样被捕获。

**并发测试怎么写才不至于两边都不合格**：起 N 个 goroutine 碰运气，
要么抓不到竞态，要么在别的机器上偶发失败。这里用 gorm 回调把事务一
精确卡在「刚拿到行锁」的那一刻，再让事务二从另一条独立连接冲上去，
断言它此时必须被数据库挡住，事务一提交后它才继续——每一步的先后都是确定的。
两个事务必须是两条独立连接：复用同一条会让"并发"退化成串行，测出来的是假象。

预置数据必须真的提交（两个独立事务得看到同一行规则），所以这两组测试
不用事务回滚，改由收尾显式清理，跑完开发库 0 残留。

### 支付回调：两笔并发场景分开验，才抓得住各自的守卫

钱包充值回调的第一组测试只覆盖「同一笔支付单被重复投递」：
余额只加一次、两笔中恰好一笔标记 `Replayed`、钱包流水只有一条。
判别力验证时，支付单行锁与幂等摘要两处能被抓到，
**钱包账户行锁、CAS 的 `status='initiated'` 守卫、充值申请行锁三处删掉都测不出来**。

原因不是测试写得不好，是那个场景里最外层的 `payment_order` 行锁
已经把两笔回调完全串行化，下游守卫根本不会被单独触发——
它们是纵深防御，处在外层锁的荫蔽之下。

补第二组场景才把它们分开：**同一个用户的两笔不同充值单同时到账**。
外层锁的是两行不同记录，互不阻塞，两笔都会同时走到钱包余额更新。
把闸门改挂在 `wallet_account` 的加锁读上，删掉该行锁后
第二笔不再被挡、读到快照旧值，余额只增加一笔——测试报"丢了更新"。

判别力：支付单行锁 ✔、幂等摘要 ✔、`wallet_account` 行锁 ✔。

### 一处证明不了的守卫

余额更新带 `WHERE id = ? AND version = ?` 的乐观版本条件，
但删掉它测不出任何差异。查了全仓所有 `wallet_account` 写入路径——
`payment_callback_store`、`wallet_ledger_store`、`wallet_refund_store`、
`wallet_reservation_store`、`wallet_account_store`、`admin_wallet_risk_store`
无一例外都是先 `FOR UPDATE` 再读后写。行锁在位时 `version` 不可能变化，
这个条件恒为真。**它今天没有任何场景能让它失效**：
要么是纵深防御，要么是早前某版并发写法留下的痕迹。
记在这里，免得后来人以为删掉它会出事。

### 顺带记一笔端口归属

`CheckoutPortAvailable` 在事务里但前面没有加锁，看着是典型的检查后写入。
实际由 `charge_payment_intent.active_port_code` 兜住：它是个 STORED 生成列
（`status='initiated'` 时等于 `port_code`，否则 NULL），上面有唯一键
`uk_active_port`。同端口并发建意图会被数据库挡下，不会双占。
注意 MySQL 唯一索引不约束 NULL，所以这只保护 `initiated` 状态；
`paid` 且订单在充电的占用仍只靠那条检查语句。撞库时返回的是原始 1062
而不是 `ErrPaymentIntentConflict`，接口层会翻译成 500 而非"端口被占用"。

### 人工退款：同一笔支付单上的并发申请会合计超额

同一类缺陷的第三次出现。`ManualCreate` 在 `refund_review_store.go:66`
锁了 `payment_order` 行，第 96 行再用 `SUM(refund_cents)` 算已占用额度。
看着很稳——**但那把行锁挡不住别人已经落库的记录**：
聚合读是快照读，而快照在第 61 行第一次读充电订单时就已建立，
早于并发的另一笔申请提交。

实测：实付 10000，两笔各 6000 的申请并发提交，两笔各自读到「未占用 0」，
一起通过 `pending > paid - refunded - amount` 校验。**合计退掉 12000，超发 2000。**

已把两处聚合都改为加锁读：`ManualCreate` 与 `RefundExecutor.claim`。
判别力：聚合退回快照读 → 报"超发 2000"；支付单行锁去掉 → 被捕获；
超额校验条件去掉 → 报"超发 2000"。

### 这一类缺陷出现三次了

活动发券预算计数、支付回调、还有这次的退款累计占用。
共同形状是：**先加锁锁住某一行，再聚合读一个集合，据聚合值做准入决策**。
行锁让事务串行化了，但聚合读仍按自己的快照算，于是串行化帮不上忙。

判据不变——凡是「锁一行 → 读聚合计数 → 按计数决策」的流程，
**那个计数必须自己也是当前读**。已经改掉的三处：
`marketing/coupon_activity_store.go` 的三处计数、
`settlement/refund_review_store.go` 与 `settlement/refund_executor_store.go` 的两处 `SUM`。

全仓还剩的 `Count`/`SUM` 聚合读都是分页计数或只读展示，不参与准入决策。

### 计费主干：没找到缺陷，但护栏钉住了

`Store.Calculate` 的结构是「按 `charge_order_id` upsert 回执 → 锁回执行 →
看 `calculation_id` 是否已填」。填了走重放路径返回首次结果，没填才新建计价。
这里的 `calculation_id` 是加锁读（当前读），所以拿到锁就能看到别人的提交。

四层防护都在：`fee_receipt` 主键就是 `charge_order_id`（upsert 本身即行锁）、
`fee_delivery` 主键同样是 `charge_order_id`、`fee_calculation` 有
`uk_no(calculation_no, created_month)`、回执行的 `FOR UPDATE`。
单测只覆盖 `PriceSource`，这套判重只在真库下才存在。

补了两组真库测试：并发计费只出一份结果（计价一行、投递一行、两次拿到同一单号），
以及同单不同来源必须判冲突、不得再出计价。三个变异全部被捕获：
回执行锁去掉、回执 upsert 退化成纯插入、来源一致性比对去掉。

**这轮没找到新缺陷。** 不是因为没看——是真查过之后确认防护成立，
如实记下比编一个缺陷出来有价值。

同包内两组集成测试共用一份骨架，提取到 `gorm_integration_test.go`：
独立连接、并发闸门、按表名挂载的加锁回调。

### 退款成功入账：查过之后守卫成立，仍有一处冗余锁

`RefundExecutor.apply` 的 SUCCESS 分支拿的是「钱包分摊行 → payment_order 行锁 →
refund_record 行锁」，钱账的累加读 `p.RefundedCents` 走的是那把支付单行锁的当前读，
所以累加是安全的。三组真库测试：同一笔退款并发入账只加一次、
同一支付单上两笔退款并发累加精确等于实付、退满后超额退款必须被拒。

判别力：支付单行锁去掉 ✔、成功回执的幂等判断去掉 ✔、退额上限校验去掉 ✔。
**退款单那把行锁删掉测不出差异**——两笔退款锁的是同一张支付单，
已被外层行锁串行化，它处在荫蔽之下。

顺带记一笔：`apply` 第 312 行那个 `Count` 也是快照读，但它只用来决定
充电订单要不要从 `refunding` 翻回 `completed`。读到旧值的后果是这笔订单
晚一轮才翻状态（活性问题），不涉及金额。刻意没有一并改成加锁读——
它不参与金额决策，加锁只会白白串行化计费。

三组测试都用 `biz_type='charge'`：那样 `LockRefundWallet` 与
`SettleRefundWallet` 直接返回，要落的表只剩 payment_order、refund_record、
charge_order 三张。

### 事件外盒：SKIP LOCKED 是多副本不重复投递的全部依靠

worker 有三个 publisher（gateway 读 `gateway_db.event_outbox`、
user 与 admin 读 `central_db`），生产上多副本同时扫表。
`ListPublishableOutbox` 的 `FOR UPDATE SKIP LOCKED` 让各副本各取各的——
既不排队，也不会把同一条事件投递两次。

这条性质**只在真库的行锁语义下存在**：SKIP LOCKED 是 MySQL 特有的，
换成别的数据库整个假设就变了；单测和内存替身都验不了。

三组测试：两个副本并发领取的两批互不重叠（并集等于全部，无一条同时出现）、
发布成功后不再被领到、失败退避期内不被立刻重领。

判别力四个变异全部捕获，且报出的正是对应失效模式：
去掉 SKIP LOCKED → "SKIP LOCKED 失效，多副本会排队等同一批"；
去掉整个行锁 → 被捕获；状态与到期过滤去掉 → "已发布的事件被再次领到"；
退避间隔传 0 → "退避期内的事件被立刻重领"。

`PublishBatch` 用 `READ COMMITTED` 隔离，不走快照，所以这一段没有前几轮
那个"聚合读读到旧值"的问题。行锁一直持有到 Redis 确认收到为止，
MySQL 提交失败时 Redis 侧可能已有一份重复消息——这是设计上接受的，
消费端按 event_id 去重。

### 跨库一致性：充电结束回执靠显式 READ COMMITTED，不是靠行锁

这是整条跨库链路离钱最近的一环：gateway 采到结束帧 → worker 同步到 central
→ central 落结束回执、结束计量、释放端口占用、派发计费。
worker 在「central 已确认、gateway 端口释放失败」时会整条重放
（`end_sync.go:136-142` 是硬错误，设备事件不会标记已处理），
所以 central 这头必须幂等。

三组真库测试：并发重复投递只结算一次（结束回执/事件日志/计费派发/发件箱
各一条，端口占用归零）、顺序重放识别为重放、同单不同计量逐项判冲突
（电量/时长/停止原因/结束时刻/停止指令号五项各一个用例）。

**关键发现：那段 `&sql.TxOptions{Isolation: sql.LevelReadCommitted}` 是承重的。**
把它删掉退回 REPEATABLE READ，重复投递的那个事务会在 `charge_end_receipt`
上读到快照旧值（看不到前一事务刚落的回执），于是跳过重放分支、
落到订单状态检查，判成冲突而不是重放——worker 等不到成功响应，
端口一直占着，新订单全被挡住。

这里我**推理错过一次**：先以为安全性来自"加锁读不建立快照，
所以快照在行锁之后才建立"。为验证它把锁前的 `card_charge` 加锁读改成
普通读，测试**没有**变红——推理是错的。是隔离级别在起作用。
差一点的写法会把这个没验证的说法当成结论留在注释里。

顺带记一个测试自身的坑：并发闸门最初用 `strings.Contains(sql, "charge_order")`
匹配，结果挂到了 `card_charge` 那条查询上——它的 WHERE 列名是
`charge_order_id`，含这个子串。而 card_charge 没有行时 `FOR UPDATE` 只取间隙锁，
挡不住第二个事务，测试误判成「结算没有串行化」。**按 `FROM` 子句匹配表名**，
不要按列名或裸子串。

---

## 第十一轮：把真库测试接进 CI，顺带挖出一个从未显形的坏迁移

### 写 CI job 本身就是一次核验

新增 `.github/workflows/ci.yml` 的 `integration` job：起 MySQL 8.4 service、
建库、跑迁移、`make test-integration`。写完不验证等于没写，
所以在一台**全新的 MySQL 容器**上把三个步骤原样跑了一遍。

第一步就红了。

### migrations/worker_db/0001_init.sql 在干净库上跑不通

文件开头有一段孤儿分区子句：

    /*!50100 PARTITION BY RANGE (to_days(`created_month`))
    (PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB, ...) */;

它不属于任何 `CREATE TABLE`（`task_execution_log` 有自己的一份），
单独执行是无效 SQL，任何 MySQL 都报 1064。在两个实例上分别执行，错误完全一致。

**它为什么藏了这么久**：开发库那个数据卷是重构之前就迁移过的，
里面还留着已裁剪的 `dlq_log`、`dlq_replay_cursor` 两张表；
goose 看到版本已是 1，重跑就是空操作，那段坏 SQL 根本没机会执行。
`make check` 不跑迁移，于是它一路绿灯。

已删除该孤儿片段。现在全新库上三个 schema 都能迁移成功。
加了 `scripts/check/migration_sql.py` 做静态兜底（分区子句前一句
若已以分号结尾即判为悬空），并接入 `make check`；真库迁移才是最终那道关。

顺带把 `docker/mysql/init-databases.sh` 换成 `.sql`：
CI 的 runner 上未必装了 mysql 客户端，而 SQL 本身可以由 mysql 镜像自带的客户端执行。
一份 SQL 两个调用方共用，少 GRANT 会在迁移第一步报 Access denied，
而那条报错完全指不到授权这一层。

### 并发测试的做法写进了 AGENTS.md

新增「并发与幂等」一节：集成测试怎么组织（含 build tag 机检、不提供跳过路径）、
并发测试怎么写才算数（闸门控制先后、不许靠 goroutine 碰运气、按 FROM 子句匹配表名），
以及那两条判据。写进契约是因为这些判据不是读一次代码就能内化的——
本轮我自己就在其中两条上踩了坑。

### 三处被证伪的判断

1. 「结束回执的幂等靠加锁读不建立快照，所以快照在行锁之后才建立」——
   把锁前的加锁读改成普通读，测试**没变红**。真正承重的是那行 `LevelReadCommitted`。
2. 并发闸门按裸子串 `charge_order` 匹配，挂到了 `card_charge` 的查询上，
   误判成「结算没有串行化」。
3. `migration_sql.py` 第一版判据写反：合法形态里 `COMMENT='…'` 后**没有**分号
   （分号在 `*/;` 上），导致 12 处合法分区全被报成孤儿。

三条都是靠"跑一遍看报什么"发现的，没有一条是自己想明白的。

---

## 第十二轮：wallet 包——扫了，结论是这里没有同类缺陷

按「找准入决策点」而不是「哪里没测试补哪里」的做法，扫 `wallet` 的 5 个 store。

### 两处形似的地方，查完都是安全的

**退款频次风控**（`user_wallet.go:245` → `CountRecentRefundRequests`）
是一个没有加锁子句的普通读，事务用的还是默认 REPEATABLE READ——
按前几轮的规律这就是漏洞的样子。真库并发 5 笔：2 笔受理、第 3 笔转人工复核
并冻结钱包、后 2 笔被 409 挡下。**上限没被绕过。**

**`ReserveRefund` 里 `refund_record` 的 `SUM`** 同样没有加锁，
外层也只有一把 `payment_order` 行锁——和我在 `settlement` 修掉的那个形状一模一样。
但它没被绕过，因为真正的守门员是 `wallet_account.frozen_cents`：
它随每笔预占累加，且在钱包行锁内以当前读读出。
两笔各 8000 的退款撞上 10000 的充值额时，第二笔在冻结额上就被挡住了。
真库验：只有一笔受理，退款记录合计与冻结额严格相等。

**换句话说 `wallet` 用的是另一种正确设计**——不是让聚合读变成当前读，
而是让「已占用额度」本身就是一个在锁内累加的当前读列。
两者等价，但后者更难被改坏。

### 判别力

四个变异捕获三个：冻结额不再累加 ✔、频次上限改成 99 ✔、申请不落库 ✔。
**`ReserveRefund` 内部的行锁删掉测不出差异**——handler 在
`user_wallet.go:212` 已经锁了同一行钱包账户，它处在荫蔽之下，
和 `apply` 里的退款单锁、`wallet_account` 里的 version 条件同属纵深防御。

### 这一轮我被自己的测试骗了两次

第一次：断言写错。以为 5 笔都该返回 200，实际冻结之后到达的申请被 409 挡下，
那是正确行为。差点据此"修"一个不存在的缺陷。

第二次（更值得记）：清理函数的表名写成了 `risk_freeze_link`，
真实表名是 `wallet_risk_freeze_link`。于是每一轮都撞主键、返回 503，
看上去像是并发下的真故障。**其实清理函数自己 `t.Errorf` 报出来了，
被我 grep 时过滤掉了。** 定位靠的是给 `resourceWriteFailure` 临时加一行
把被吞掉的错误打出来——那是个 503 兜底分支，不记日志，错误全靠猜。

两次的共同点：都是"把测试的假象当成了代码的缺陷"。
**写并发测试时，先确认失败是生产代码造成的，不是自己搭的脚手架造成的。**
