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
