# 04: 架构决策目录编号与状态检查

**What to build:** 架构决策目录成为可信索引——编号连续、每份带状态标注。读者靠编号就能发现「中间缺了一篇」，不会因为编号跳号而误以为决策不存在。

**Blocked by:** 01

**Status:** done

- [x] 检查能报出：编号重号、编号断号、文件名与编号不一致、文件缺少状态标注。
- [x] 现有两份 ADR 判定为绿。
- [x] 状态取值集合固定且在说明中有定义（不是自由文本）。
- [x] 检查在无数据库环境下运行。
- [x] ADR 文件结构在检查与说明中一致：状态标注位置固定，可被机器解析。

## 实施记录

### 判定口径

`scripts/rules/check-rules.mjs` 的 `assertAdrNumbering` 接入第六条不变量，读 `docs/adr/` 目录（跳过目录说明 `README.md`），报出五类问题：

| 问题 | 判据 |
| --- | --- |
| 文件名与编号不一致 | 文件名不匹配 `^(\d{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$` |
| 编号重号 | 两个文件的前四位相同，报出后一个与前一个的路径 |
| 编号断号 | 现有编号集合里缺 `0001..N` 中的某个 |
| 缺少状态标注 | 首块 YAML front matter 里没有 `status` 键 |
| 状态取值非法 | `status` 不在固定集合内，报出实际取值 |

状态集合 `ADR_STATUSES = proposed / accepted / rejected / deprecated / superseded`，机器认的是检查里的常量，[docs/adr/README.md](../../../docs/adr/README.md) 是给人看的说明。`superseded` 该写明的新编号**本条无对应检查**，已在说明里标注。

### 红绿确认

五个竖切逐条先红后绿：断号 → 文件名不合约定 → 缺状态标注 → 取值不在集合 → 编号重号。前四刀是新增判定，第五刀在实现过程中暴露两个真 bug：

- `[seen.keys()].sort()` 展不开 `Map` 的键迭代器，报错里直接打印 `[object Map Iterator]`。
- `const [number] = entry.match` 取的是整个匹配串而不是第 1 个捕获组，`seen` 的键因此是整条文件名，断号判据全线失真。

两个都是实现错、不是测试错，改完即绿。

### 与票 02 的计数联动

不变量从五条「尚未实现」变成四条，票 02 写的两条断言（`尚未实现：` 行数与摘要计数）随之更新。这不是放宽断言，是登记处真的多了一条实现。

### 验证

- `node tasks.mjs check-rules` 退出 0：两条「通过」、四条「尚未实现」、0 条失败。
- `node --test scripts/tasks.test.mjs scripts/rules/check-rules.test.mjs scripts/git/pre-commit.test.mjs scripts/ops/check-deploy.test.mjs scripts/db/backup.test.mjs scripts/db/provision-runtime.test.mjs` → 82 条用例全过。
- `node tasks.mjs check` 退出 0：fmt 无输出、vet 无输出、`go test` 95 个包零失败；pre-commit 钩子走同一路径，同样三步通过。
- 仓库文档与票据 367 条相对链接逐条解析，0 条死链（顺手修掉票 02 实施记录里一条写错的相对链接）。

### 留给后续

- `superseded` 状态的取代编号没有机器检查，靠人工核对。
- 目录下的子目录不在检查范围内；目前没有子目录，将来加了要一并处理。
- 票 05 回捞 8–13 条 ADR 时，编号连续与状态标注已就位，只需保证不跳号。
