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

行锁并发（两个事务同时抢同一活动规则）尚未覆盖：
真库能验，但需要两个独立连接与显式并发编排，本轮没做。

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
