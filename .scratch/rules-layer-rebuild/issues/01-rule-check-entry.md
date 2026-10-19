# 01: 规则检查入口与门禁接线

**What to build:** 一条统一、可重复执行的规则层检查入口。任何维护者执行 `node tasks.mjs check-rules` 都能得到「规则层是否完好」的判定，失败时能立刻知道是哪一条不变量被违反、涉及哪个文件。这个判定同时是默认门禁 `node tasks.mjs check` 的第一步——规则漂移在提交前就被拦住，而不是等到某次审计才发现。检查只读文档与迁移 schema，不连数据库、不连网络。

**Blocked by:** 无（可立即开工）

**Status:** done

- [x] `node tasks.mjs check-rules` 存在；通过返回 0，不通过返回非 0，并在输出中指明违反的不变量与涉及的文件。
- [x] `node tasks.mjs check` 的执行序列以规则检查开头（秒级快速失败），tasks.mjs 的帮助文本与该顺序一致。
- [x] 检查实现与既有检查脚本同组织方式（与 `scripts/db/`、`scripts/ops/` 同级），不引入新的运行时依赖。
- [x] 骨架为六条不变量各留一个断言位置：尚未实现的断言显式占位为「尚未实现」，不得产生假绿。
- [x] 在没有数据库与网络的环境下可运行并判定为绿。
- [x] 有配套单元测试，至少覆盖「全部通过」与「某条不变量被违反时报红并指出文件」两条外部行为。
- [x] 检查只判定文档层不变量；依赖方向、表归属与接口契约不由本检查判定，仍按现有规则由人工核对。

## 实施记录

- 入口 `scripts/rules/check-rules.mjs`，测试 `scripts/rules/check-rules.test.mjs`，与 `scripts/db/`、`scripts/ops/` 同级。
- 六条不变量登记在 `INVARIANTS`，`assert` 为 `null` 即「尚未实现」，报告里单独计数，不计入已验证；摘要行同时给出失败数与未实现数，退出码 0 只表示当前无违反。
- 「尚未实现」与「通过」的判定语义先红后绿确认；违反分支另用三条变异（返回 0、不输出文件、改用 id 代替标题）验证断言确实会红。
- `node tasks.mjs check` 顺序改为 check-rules → fmt → lint → test，端到端确认 check-rules 输出排在所有 Go 输出之前。
- 门禁描述同步到 AGENTS.md 与 docs/operations.md。
- 验证：65 条 .mjs 测试全绿（`node --test scripts/**/*.test.mjs`）；Go 门禁在本机受阻于 goproxy.io 不可达、模块缓存缺 14 个包，与本票改动无关，本票未新增 Go 代码。
- 经两轴审查（Standards + Spec）修复：README 的门禁描述与任务清单、AGENTS.md 目录图同步 `scripts/rules/`；operations.md 去掉「提交前拦下」的过度宣称，改为明说当前六条都还没有断言；不变量 3 恢复 spec 原文的后半句「只能是指向迁移 schema 的指针」；不变量 id 在失败输出中显式出现，不再是死字段。

