# 验证入口

本文件只回答「跑什么命令验什么」，不重述任何规则。规则的含义去各自落点看，见 [规则层入口](README.md)。本表是验证入口类目的唯一落点，`AGENTS.md` 与 `README.md` 只做指针，不再复制这张表。

| 命令 | 覆盖什么 | 前置条件 |
| --- | --- | --- |
| `node tasks.mjs check-rules` | 文档层不变量，见下节 | 无，只读文档与迁移 schema |
| `node tasks.mjs check` | 默认门禁：顺序执行 `check-rules`、`fmt`、`lint`、`test` | Go 工具链与可用的模块缓存 |
| `node tasks.mjs test-contract` | 对外 JSON 形状的序列化契约用例 | Go 工具链，不连数据库 |
| `node tasks.mjs test-integration` | 真实 MySQL 语义：行锁、唯一键冲突、幂等重放、`SKIP LOCKED` | 先设置 `TEST_MYSQL_URL`；本地入口是 `node scripts/db/test-integration.mjs` |
| `node tasks.mjs fmt` / `lint` / `test` | `go fmt ./...`、`go vet ./...`、`go test ./...` | Go 工具链 |

## 规则层检查判定了什么

六条不变量登记在 `scripts/rules/check-rules.mjs`。**实现状态以该脚本的实际输出为准**，不要凭文档推断——报告会把尚未实现的断言逐条标注「尚未实现」，摘要给出尚未实现条数。

| 不变量 | 状态 |
| --- | --- |
| 冻结文档顶部带冻结声明并指向新层 | 已实现 |
| agent 执行层的行数上限（100 行以内） | 尚未实现 |
| 执行层的规范性语句指向检查入口，或原地标注「本条无对应检查」 | 尚未实现 |
| 数据库枚举的取值串不得出现在任何文档中，只能是指向迁移 schema 的指针 | 尚未实现 |
| 跨文档引用的文件与锚点均可解析 | 尚未实现 |
| 架构决策目录编号连续、每份带状态标注 | 尚未实现 |

几点边界：

- 默认门禁不连数据库，也不启容器。依赖 Docker 或真实 MySQL 的验证不进 `check`，单列一层。
- `scripts/` 下的 Node 脚本测试用 `node --test <文件>` 单独跑，**不在** `node tasks.mjs check` 里。
- `scripts/git/pre-commit.mjs` 直接跑 `go vet` 与 `go test`，不经 `tasks.mjs check`，因此规则层检查不在 git 钩子范围内。
- 前端各自的检查命令见 `frontend/admin-web/` 与 `frontend/miniprogram/` 的 README。

依赖方向、表归属与意图这类约束机器判不了，按 [规则层入口](README.md) 的约定留在人读层并标注「本条无对应检查」。
