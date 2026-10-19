# 02: 新层骨架与冻结声明

**What to build:** 规则层的新落点就位，旧规则文档被显式冻结。任何人（包括 agent）打开任何一份旧文档，第一眼看到「本文已冻结、只读，规则请到新层看」的声明与新层入口链接，不会再往冻结文档里加新规则。冻结不等于清空——旧文档内容此刻仍然可读可查。

**Blocked by:** 01

**Status:** done

- [x] 新层落点存在并各自写明用途：架构边界与依赖、业务不变量、验证入口、进度与计划（含归档子目录）、架构决策目录、术语词表。
- [x] 每一份旧规则文档顶部都有显式冻结声明，含「已冻结」「只读」字样，并带一个可解析链接指向新层入口。
- [x] 规则检查能报出「缺少冻结声明」的文档并指出文件；补上声明后判定为绿。
- [x] 冻结声明不破坏文档原有结构，既有链接仍可解析。
- [x] 仓库级 agent 约定中描述文档布局的那份文件已同步为新层布局。
- [x] 归档目录与活跃文档物理分离：归档只放历史快照，活跃规则不写入归档。

## 实施记录

### 新层布局

`docs/rules/` 是人读层根目录，`docs/rules/README.md` 是单一入口，列出七个类目的唯一落点：

| 类目 | 落点 | 本票状态 |
| --- | --- | --- |
| 架构边界与依赖 | `docs/rules/architecture.md` | 骨架，内容待票 08 迁入 |
| 业务不变量 | `docs/rules/business-invariants.md` | 骨架，内容待票 07 迁入 |
| 架构决策 | `docs/adr/`（新增 `README.md` 写明用途） | 已存在 |
| 术语边界 | `GLOSSARY.md`（补类目标注） | 已存在 |
| 验证入口 | `docs/rules/verification.md` | 本票新建，列出检查命令与实现状态 |
| 运行期操作 | `docs/operations.md` | 已存在 |
| 进度与计划 | `docs/rules/plans/`，归档在 `docs/rules/plans/archive/` | 本票新建，均为空目录加说明 |

### 冻结范围

冻结四份「规则即将迁走」的旧文档：`docs/business-contracts.md`、`docs/architecture-boundaries.md`、`docs/refactor-roadmap.md`、`docs/status-enum-roadmap.md`。判据是「已过期」——票 07／08／09／10 会删除它们并把内容搬进新层。`README.md` 与 `docs/operations.md` 不冻结：内容仍在原地且仍是运行入口，给它们贴过期声明反而误导。`docs/miniprogram-roadmap.md` 未被任何一张票迁移，本轮不纳入。

### 不变量 5 的断言

`scripts/rules/check-rules.mjs` 新增 `FROZEN_DOCUMENTS` 与 `NEW_LAYER_ENTRY` 两个登记处，实现 `assertFrozenDeclarations`，报出五种违反：登记的文件已不存在、顶部缺少声明、缺「已冻结」或「只读」字样、声明没带链接、链接落空或指向的不是新层入口。声明的定位锚在「紧跟标题之后的第一段非空内容」，重排版不误报，往导语后面塞声明会被判不合格。

### 红绿确认

六个竖切，每个都先写测试再实现，逐条跑过红：缺声明报红 → 补齐判定为绿 → 缺字样报红 → 无链接报红 → 链接落空报红 → 登记文件被删报红。审查后又补两条：指向存在但非入口的文件报红、声明被导语挤到标题下报红。测试用一次性临时目录搭受控文档集合，清单直接取 `FROZEN_DOCUMENTS`，不再抄一份。

### 双轴审查的修复

Standards 轴与 Spec 轴各抓到同一个最重的问题：我在新层文档里写「`check-rules` 判定冻结声明、跨文档链接、枚举取值串、ADR 编号与执行层行数」，而实际只有冻结声明实现了——正是本项目要消灭的假绿。已改为在 [验证入口](docs/rules/verification.md) 逐条列出实现状态并声明「以脚本实际输出为准」，规则层入口不再复述。

其余修复：AGENTS.md 的两条新增散文压回指针（执行层不写解释）；目录树里 `rules/`、`adr/` 曾挂在 `docs/ .agents/ .githooks/` 多条目行下、渲染成了 `.agents/` 的子目录，已拆开；`architecture.md`／`business-invariants.md` 预写的迁入章节清单删掉（与实施票重复，且会自己漂）；归档豁免条款删除（该口径归票 09／10 定，且检查里没有对应逻辑）；测试文件里抄的第二份冻结清单改为 import。

### 验证

- `node tasks.mjs check-rules` 退出 0，报告「通过：冻结文档顶部带冻结声明并指向新层」「0 条失败，5 条尚未实现」。
- `node --test scripts/tasks.test.mjs scripts/rules/check-rules.test.mjs scripts/ops/check-deploy.test.mjs scripts/db/backup.test.mjs scripts/db/provision-runtime.test.mjs` → 73 条用例全过。
- 仓库文档 363 条相对链接逐条解析，0 条死链（跨文档链接不变量尚未实现，这条是一次性脚本验证，不进仓库）。
- `node tasks.mjs check`：check-rules 排在最前面并通过，随后在 Go 步骤失败——`goproxy.io` 不可达、模块缓存缺 14 个包，与本票无关，本票未新增 Go 代码。

### 留给后续票的账

- `docs/miniprogram-roadmap.md` 承载计划却不在进度与计划类目下，本轮无票迁移它，是当前唯一的例外。
- `AGENTS.md` 的 golang-lint 行仍写「`node tasks.mjs check` 不增加架构脚本」，与已存在的 check-rules 不符；瘦身归票 12。
- `scripts/git/pre-commit.mjs` 不经 `tasks.mjs check`，规则层检查不在 git 钩子范围内；改钩子不在本票范围。
