# 仓库维护规则

## 语言约束
1. 使用中文进行思考和回复
2. 使用中文进行注释和文档编写
3. Git 提交使用中文：类型前缀按 Conventional Commits 写英文（`docs`、`feat(db)`），描述与正文写中文

## 依据与边界

- 以当前源码、路由注册、数据库初始化 SQL、Compose 配置和测试为实现依据。**规则层的单一入口是 [规则层入口](docs/rules/README.md)**，七个类目各有唯一落点，新增规则怎么落位见该文件。[README](README.md) 提供项目概览、启动与检查入口，[运行与数据维护](docs/operations.md) 记录配置、部署、备份恢复、监控及模拟器操作。修改行为或运行方式时同步所属文档，避免在多处复制完整规则。难以逆转且无上下文时令人困惑的取舍记入 [docs/adr/](docs/adr/README.md)，术语边界记入根目录 [GLOSSARY.md](GLOSSARY.md)；两者按需惰性创建，不为凑数预建。
- `docs/business-contracts.md`、`docs/architecture-boundaries.md`、`docs/refactor-roadmap.md` 与 `docs/status-enum-roadmap.md` **已冻结、只读**，不再接受新增或修改规则，现行落点见 [规则层入口](docs/rules/README.md)。冻结声明由 `node tasks.mjs check-rules` 判定；已实施批次的流水进 [docs/rules/plans/archive/](docs/rules/plans/archive/README.md)，活跃规则不写入归档。
- **文档重述枚举时分治，不一律照抄取值列表**：取值真相源是 `migrations/*/0001_init.sql` 的 ENUM 声明与 `internal/domain/` 的 Go 常量，文档负责语义。**顺序有语义**的不重述列表——MySQL ENUM 按声明序存序位，任何 `ORDER BY` 或序位比较都依赖声明顺序，照抄必然漂移（`payment_status` 的文档顺序已与 SQL 相反），改为指向真相源；**纯集合、顺序无意义**的保留在文档，可读性价值大于漂移风险。文档中出现只存在于文档、代码与迁移里零命中的取值名，按文档错误处理并核对真相源，不得当作「兼容输入」放过。
- 重构的完成证据与核验方法见 [docs/refactor-roadmap.md](docs/refactor-roadmap.md)，目标结构与实施记录见 [docs/architecture-boundaries.md](docs/architecture-boundaries.md)。
- 用户端 `frontend/miniprogram/` 的后续开发任务与交互优化计划见 [docs/miniprogram-roadmap.md](docs/miniprogram-roadmap.md)；完成任务后原位更新该文件的状态与证据。新增页面只登记 `frontend/miniprogram/src/app.config.ts` 一处，不另建页面清单副本；改动该文件后 Taro dev server 不会热重建路由表，须重启 `user-h5` 容器。
- 后端状态与枚举字段的类型化计划见 [docs/status-enum-roadmap.md](docs/status-enum-roadmap.md)，记录数据库 ENUM 列、VARCHAR 有限集合与 Go 侧裸字符串的分类维度、放置规则与依赖可行性判定、逐枚举清单、分批顺序与验收标准；数据库列定义仍以 `migrations/*/0001_init.sql` 为准。**P0 批次 12 项枚举已实施**，取值词汇落在 `internal/domain/<域>/`（`order`／`money`／`marketing`／`card`／`devices`／`wallet`／`identity`）、就地升级的 `device.FaultStatus` 与 `operations.ScopeType`，以及各属主包的就地定义（`card`、`marketing`、`order`、`settlement`、`ops`、`pricing`、`export`、`worker/repo`、`gateway/repo`）；类型归属与依赖边界见 [docs/architecture-boundaries.md](docs/architecture-boundaries.md) 的「状态词汇的归属与依赖边界」，保留裸字面量的例外逐条登记在路线图 8.1.2（P0）、8.3.2（P1）与 8.4（P2）节。**P0（12 项）、P1（38 项）与 P2（6 项）批次均已实施并通过 `node tasks.mjs check`、`build-all`、`go vet -tags integration` 与**真实 MySQL 集成测试**（95 个包零失败）；真库运行找出并修复的六个回归登记在路线图 9.2 节，其中**白名单常量串不可直接拼进 SQL 的 `IN (...)`**（会得到单元素列表）一条必须遵守。前后基线也已部分执行：三库 schema 逐字节一致、`central_db` 60 张表中 58 张数据逐字节一致，**接口响应层面的逐字节锁定改由 `node tasks.mjs test-contract` 的 452 条序列化契约用例承担**（覆盖全部 56 项枚举的每个取值，含 `cmd/devseed` 触及不到的 `marketing`／`card`／`ops`／`pricing`／`export`／`worker` 等域）；「改造前后各起服务比对响应体」本身未执行，详见路线图 9.3 与 9.4 节，表述时不得把契约测试说成前后基线。P1 实施前的依赖复核修正见路线图 8.3.1（该节列出的四处原归属结论已被实测推翻），P2 的三项决策见 8.4 节（`delivery` 包因是死代码路径移入 8.5、`domain/charging → protocol` 新增依赖边已授权、测试夹具只做类型适配不改断言值）。
- `cmd/` 只负责配置、依赖装配与进程生命周期，每个子目录是一个部署单元。**业务实现按 use case 位于 `internal/` 下**：`charging`/`order`/`settlement`/`wallet`/`pricing`/`card`/`device`/`identity`/`marketing`/`payment`/`ops`/`export` 十二个业务模块（charging 的四个生命周期子包与 query 只读组合包），纯规则与跨用例契约在 `internal/domain`，表访问在各 use case 的 `*_repo.go`，进程特有编排在 `internal/worker`、`internal/gateway`、`internal/delivery`，设备契约在 `internal/protocol`，通用能力在 `internal/platform`。
- 部署单元分两类。**对外**：`user`（C 端，`:8080`）、`admin`（运营后台，`:8081`）。**对内**：`central`（用例编排与内部端点，`:8082`）、`gateway`（设备接入，`:8083` 与 DC589/TCP `:9100`）、`worker`（持久任务扫描、结果同步与 Outbox 发布，`:8085`）。**`central` 的 8082 在两套编排里都不映射端口**，内部调用只在容器网络内可达。开发栈把 `gateway`:8083、`worker`:8085 绑到 `127.0.0.1` 便于本地直连排查；生产编排对这两个进程只写 `expose`，公网 HTTP 入口为 Caddy，设备另走 `9100/TCP`。

五个进程共享同一套 use case 包，差别只在各自注册哪些路由与启动哪些后台循环——进程拆开不等于代码复制。
- 后台进程不取微信与支付凭据：它不替用户登录，也不发起支付。`config.Admin` 与 `config.Central` 分开定义，让"这个进程需要什么凭据"在类型上就是清楚的——多给一份 `WECHAT_SECRET` 就等于多一处泄露面。
- 订单快照、历史曲线和过程采样经 `GATEWAY_INTERNAL_URL` 调用 gateway 的 `/api/v1/internal/charge-orders/*`。计量证据和设备摘要也由 gateway 提供。端点位于 `internal/gateway/control`，内部请求携服务令牌。
- 业务状态与资金写入由所属领域执行。worker 复用已定义的内部接口、任务与幂等记录，不复制计费算法或资金入账逻辑。
- HTTP 适配位于 `internal/http/auth`、`internal/http/admin`、`internal/http/central` 与 `internal/http/user/<业务族>`。跨接口族的集成测试集中在 `internal/http/integration`，该目录只有测试文件。公共 HTTP 处理使用 `internal/platform/httpapi`；服务间 HTTP 调用使用 `internal/platform/serviceclient`。设备适配器实现 `protocol.Adapter`，协议解析与数据库访问分离。
- 服务间 HTTP 的共享端点与 DTO 位于 `internal/contracts/<服务>`，专用客户端位于 `internal/integration/<服务>`。用例定义所需的窄接口，`cmd` 注入客户端；业务代码不持有 URL、服务令牌或通用 HTTP 客户端。contracts 不引用 gin、gorm 或服务实现；integration 不引用业务用例或数据库。具名客户端的请求 DTO 必须以 `Request` 结尾，响应 DTO 必须以 `Reply` 结尾；共享集合中的记录、采样点等子对象保留业务名称，不能用 `Data`、`Result`、`Acceptance` 代替端点响应名。
- 五个服务进程的入口按职责拆分：`main.go` 处理信号与退出错误，`app.go` 加载配置、装配依赖与等待退出，`resources.go` 打开和释放资源，`routes.go` 注册路由与健康检查。worker/gateway 的定时循环在各自 `runtime` 子包，导出清理在 `internal/export`。资源初始化失败须释放已打开的资源；所有受管理请求和后台任务退出后才能关闭数据库、Redis 与客户端连接池。HTTP 停机使用 `httpapi.Serve`，超时取消请求后仍等待处理器退出；处理器必须响应请求 context，不启动未受管理的后台任务。
- 环境变量变更须核对配置加载、进程使用、Compose 注入和模板；仅在 `.env` 中定义不能视为已生效。
- 当前 worker 调度器的处理器登记表为空；Webhook、通用 DLQ 和监管投递未装配，`REGULATORY_*` 未被配置加载及 worker 消费。保留的事件与发送适配器不代表任务已运行，不能据表、模板变量或组件存在宣称外部投递可用。

## 子代理定义与使用

本项目在 `.agents/agents/` 中维护三个角色定义。它们是子代理的任务说明，与 `.agents/skills/` 中的技能分工不同：子代理承担委派的工作，技能提供该工作所需的方法与规范。实际使用前读取对应定义，并将本文件的项目约束、用户要求及任务上下文一起交给子代理；目录中存在定义文件不等于当前客户端已经自动注册同名代理或命令。

| 子代理 | 定义文件 | 使用时机与职责 | 输出与边界 |
| --- | --- | --- | --- |
| `manager` | [manager.md](.agents/agents/manager.md) | 需求分析、功能裁剪、PRD、用户故事、用户旅程、优先级与 MVP 范围设计。用于把业务诉求拆成可实施、可验收的需求；该名称指产品经理角色。 | 输出需求概要、功能拆解、验收标准、衡量指标与风险依赖。区分当前实现、目标行为与必要假设，结合现有源码、接口及业务契约确认可行性；分析结果不自动授权编码或变更业务范围。 |
| `reviewer` | [reviewer.md](.agents/agents/reviewer.md) | 审查指定代码、工作区改动或提交。按正确性、安全性、性能、可维护性、可读性、测试覆盖与最佳实践核对；用户指定方面时优先聚焦该方面。 | 按定义输出优点、严重问题、一般问题、优化建议与总体评价；问题给出文件、函数或行号、触发条件、影响与可执行建议。只提供审查意见，不修改或重写代码；区分已核实问题与待核实风险，修复由主代理在授权范围内执行。 |
| `tester` | [tester.md](.agents/agents/tester.md) | 测试点梳理、用例设计、测试策略、缺陷分析与自动化建议。用户明确要求执行测试时，在指定范围内运行已有单元、集成或 E2E 入口并分析结果。 | 设计任务按定义输出测试对象、用例表、风险遗漏与自动化建议；执行任务另报告命令、环境、通过／失败／跳过／受阻情况、日志与复现步骤。用例设计、mock 与静态检查不能作为实际执行或真实外部链路的通过证据。 |

### 调用方式与协作规则

- 用户可直接说明角色、范围和产出，例如「使用 manager 分析充电套餐的功能裁剪并给出 MVP 验收标准」「使用 reviewer 审查当前全部未提交改动，只输出审查意见」「使用 tester 执行本次改动相关的单元、集成和 E2E 测试，报告未覆盖与受阻项」。这些是自然语言任务示例，不是终端命令；不要求用 `$manager`、`$reviewer` 或 `$tester` 作为技能名称。
- 用户明确要求使用某角色时，主代理先读取对应 `.md`，再通过当前环境可用的子代理工具委派。委派说明应包含需求／改动范围、相关路径或提交、验收标准、允许修改的文件、运行环境与禁止事项；涉及 Go 工作时仍按下节加载技能。没有可用子代理工具时，明确说明限制，并按该定义完成角色工作，不声称已启动子代理。
- 定义文件目前只声明 `name` 与 `description`，没有模型或权限配置；不能据此宣称已配置特定模型、工具或执行权限。子代理遵守主任务的用户授权，不因角色说明扩大提交、推送、部署、对外通信或数据清理的范围。
- 子代理共享工作区时，应按业务范围或文件归属划分任务；避免多个代理同时修改同一文件，避免针对同一数据库或端口并发执行互相干扰的测试。主代理负责核对返回的证据、汇总遗漏与冲突，完成必要修复和验证后再交付。
- 常见协作顺序为：需求不清时先由 `manager` 明确范围与验收，再实施；实现完成后由 `reviewer` 审查、主代理修复，由 `tester` 验证相关行为。用户已给出明确范围时无需额外需求分析；纯文档更新只核对事实、链接与格式，不默认运行业务测试或启动三个代理。
- `tester` 的执行入口以当前仓库为准：后端默认检查为 `node tasks.mjs check`，真库集成为 `node scripts/db/test-integration.mjs`；前端分别在 `frontend/admin-web/` 与 `frontend/miniprogram/` 执行 README 所列命令，E2E 按 [运行与数据维护](docs/operations.md) 使用现有开发栈与模拟器。真库测试会使用开发数据，执行前核对夹具、并发和清理边界；没有实机、真实支付或第三方平台证据时，必须保留对应验收边界。

## Agent skills

本节记录工程技能读取的本仓库配置；与上节的 Go 技能目录（`.agents/skills/`）分工不同——本节指向 `docs/agents/` 下的仓库级约定文件。

### Issue tracker

Issue 与规格以 markdown 文件形式存放在 `.scratch/`。见 `docs/agents/issue-tracker.md`。

### Triage labels

采用五个规范 triage 角色的默认标签字符串：`needs-triage`、`needs-info`、`ready-for-agent`、`ready-for-human`、`wontfix`。见 `docs/agents/triage-labels.md`。

### Domain docs

单上下文布局：仓库根目录一个 `GLOSSARY.md`，ADR 位于 `docs/adr/`。见 `docs/agents/domain.md`。

## Go 技能与使用时机

本项目已安装 [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) 的 46 个技能，目录为 `.agents/skills/<技能名>/`。以下按本地 `SKILL.md` 的用途与触发条件整理；技能更新后同步核对本节。表中的技能名链接到对应说明，具体操作和参考资料以该目录的文件为准。

### 加载与适用规则

- 开始 Go 编码、评审、调试、重构或环境配置任务前，先加载 [golang-how-to](.agents/skills/golang-how-to/SKILL.md)，再根据任务加载主技能与相关辅助技能。仅阅读本节不等于已加载技能；实际使用前须读取对应 `SKILL.md`，按需要阅读其引用的资料。
- 可以在提示中明确指定技能，例如 `$golang-database 审查钱包退款的行锁与幂等逻辑`，也可以用自然语言要求「使用 golang-context 检查停止和退出流程」。显式指定的技能应纳入当次任务；未指定时按下面的使用时机选择。
- `golang-how-to` 是任务路由入口；其余技能按实际需要加载。安装整个集合不代表每次读取全部技能，也不代表为满足技能示例引入全部工具、框架或依赖。
- 用户明确要求与本文件中的项目约束优先于技能的通用建议。目录、数据库、资金、权限、进程生命周期和验证方式沿用本文件规定；技能示例中的分层目录、ORM、迁移方案、CI 或框架不能直接替换现有实现。
- 库专项技能用于已使用的库、明确要求采用的库，或实际进行中的选型评估。评估可以只读比较；安装技能本身不授权新增项目依赖、迁移框架或重写服务间协议。是否使用某库，以当前源码、`go.mod` 和当次任务为依据。
- 技能提供流程与规则，不保证示例命令、MCP、LSP 或 CLI 在本机可用。需要 `gopls`、`godig`、`golangci-lint`、`govulncheck` 等工具时先检查可用性；工具不可用时说明限制并采用可核验的替代方式，不把「已安装技能」写成「已配置工具」。
- 技能中的批量修复、自动提交、建分支、推送、发布、部署、对外发消息及数据清理建议仍受用户授权和本文件约束。纯文档任务按「验证与交付」检查链接、事实与格式，不因技能中的完整项目清单扩大修改范围。

### 任务路由入口（1 个）

| 技能 | 主要用途 | 使用时机 | 边界与配合方式 |
| --- | --- | --- | --- |
| [golang-how-to](.agents/skills/golang-how-to/SKILL.md) | 识别 Go 任务意图，选择主技能和辅助技能，区分用途相近的技能；也支持配置项目的技能加载说明。 | 所有 Go 编码、评审、调试、重构和配置任务开始时；不确定应选哪个技能时。 | 负责路由，不替代专项说明。例如退款事务配合 database、error-handling、testing；故障定位先 troubleshooting；技能目录说明不等于强制所有专项技能常驻加载。 |

### 代码质量与安全（8 个）

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-code-style](.agents/skills/golang-code-style/SKILL.md) | 行长与换行、变量声明、控制流、函数组织、值与指针参数、注释是否有助于理解。 | 编写或评审 Go 代码；长调用、结构体字面量、嵌套分支影响阅读；讨论代码风格时。 | 多字段字面量的可读性属于此技能；格式化仍执行 `node tasks.mjs fmt`。命名用 naming，检查器配置用 lint，文档注释用 documentation；不为排版改动业务行为。 |
| [golang-naming](.agents/skills/golang-naming/SKILL.md) | 包、类型、函数、接口、常量、错误、接收者与测试名称；缩写大小写、重复包名前缀和构造函数命名。 | 新建标识符、比较命名方案、评审名称、批量重命名或拆包时。 | 服务 DTO 严守本文件的 `Request` / `Reply` 后缀约定；批量改名配合 refactoring 与 gopls 核对引用，不能顺便改变 JSON 字段或公共契约。 |
| [golang-error-handling](.agents/skills/golang-error-handling/SKILL.md) | 错误创建、`%w` 包装、`errors.Is/As/Join`、哨兵与自定义错误、panic/recover、错误处理和日志责任。 | 增加或修改失败路径、跨层传递错误、错误到 HTTP 响应的映射、排查重复记录或丢失原因时。 | 用例、专用客户端和 HTTP 适配层保持既有错误契约；支付未知结果不能按普通失败重提。专项结构化错误用 samber-oops，日志管道用 samber-slog。 |
| [golang-safety](.agents/skills/golang-safety/SKILL.md) | 防止 nil 与 typed-nil、slice 底层数组别名、整数截断、除零、循环中的资源积累以及零值不安全。 | 评审防御性代码、协议数值转换、集合返回值、资源释放或类型零值设计时。 | 计量和金额转换须核对范围与单位；共享可变数据同时用 concurrency。已发生崩溃先用 troubleshooting；注入、密钥和鉴权问题用 security。 |
| [golang-security](.agents/skills/golang-security/SKILL.md) | 注入、路径穿越、SSRF、密码学、HTTP 安全、认证、凭据与敏感日志；威胁建模和安全发现核验。 | 处理用户输入、鉴权、文件或网络 IO、签名验签、秘密信息，或进行安全审查时。 | 重点核对实时权限、资源归属、服务令牌和支付通知；报告需有可达路径与证据。依赖漏洞扫描用 dependency-management；把扫描接入 CI 用 continuous-integration。 |
| [golang-structs-interfaces](.agents/skills/golang-structs-interfaces/SKILL.md) | 窄接口、消费方定义接口、组合与嵌入、类型断言、零值、序列化标签、值与指针接收者。 | 定义用例依赖、DTO、仓储接口、适配器，或判断接口拆分与类型组合方式时。 | 用例定义所需窄接口，`cmd` 注入实现；contracts 不依赖 gin/gorm。JSON 标签、数值类型和单位须与双方契约一致；不为每个结构体机械增加接口。 |
| [golang-lint](.agents/skills/golang-lint/SKILL.md) | 理解 `go vet`、staticcheck 等诊断；选择检查项、配置 golangci-lint、解释或合理抑制告警。 | 处理静态检查失败，讨论检查器或 `nolint`，维护已有检查配置时。 | 本项目 `node tasks.mjs lint` 使用 `go vet ./...`；`node tasks.mjs check` 的第一项 `check-rules` 只判定文档层不变量（见 [规则层入口](docs/rules/README.md)），依赖方向与表归属仍靠代码评审，不做架构脚本检查。通用 golangci-lint 示例不替代既有检查入口；CI job 编排用 continuous-integration。 |
| [golang-documentation](.agents/skills/golang-documentation/SKILL.md) | godoc、包与接口说明、README、变更记录、示例、配置和 API 文档；保持条件与业务含义准确。 | 编写或审查 Go 项目文档、维护本文件、解释接口约束或增加可运行示例时。 | README 保留运行入口，业务规则与运维细节按 [规则层入口](docs/rules/README.md) 的类目分别维护于 `docs/rules/business-invariants.md`、`docs/operations.md`，本文件管维护规则；架构记录放 `docs/rules/architecture.md` 与既有 `docs/`。API 文档核对真实接口；不因通用模板额外创建文档站、`llms.txt` 或重复文档树。 |

### 设计、并发与数据访问（8 个）

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-design-patterns](.agents/skills/golang-design-patterns/SKILL.md) | 构造函数、functional options、枚举、资源生命周期、优雅停机、超时重试、流与迭代器及架构模式取舍。 | 设计 API、资源管理、恢复策略或选择适合当前问题的 Go 模式时。 | 沿用 use case 归属与 `httpapi.Serve`；重试沿用原请求标识。具体 DI 装配用 dependency-injection；错误包装用 error-handling，不能借模式改写业务契约。 |
| [golang-dependency-injection](.agents/skills/golang-dependency-injection/SKILL.md) | 手工构造函数注入、依赖反转、可测试性、单例与工厂管理；比较 Wire、Dig、Fx、Do 的适用成本。 | 设计依赖关系、解耦具体实现、调整 `cmd` 装配或评估是否需要 DI 容器时。 | 当前依赖由 `cmd` 装配，优先沿用现有注入方式；明确采用某容器后再加载对应库技能。注入方式不能让用例持有 URL、服务令牌或通用 HTTP 客户端。 |
| [golang-concurrency](.agents/skills/golang-concurrency/SKILL.md) | goroutine 生命周期、channel 所有权、`select`、互斥锁与原子操作、WaitGroup/errgroup、singleflight、worker pool。 | 新增后台循环、并发任务、共享状态或限流；评审退出条件、channel 关闭和竞态风险时。 | 每个任务有取消与等待路径，任务退出后再关 DB/Redis。取消同时加载 context；数据库串行化与幂等还需 database 和真库测试；已有死锁先 troubleshooting。 |
| [golang-context](.agents/skills/golang-context/SKILL.md) | context 跨层传播、取消、超时与截止时间、请求级值，以及 Background/TODO/WithoutCancel 的取舍。 | 设计请求到用例、数据库、服务客户端的取消链；修改超时、退出流程或 context 生命周期时。 | 处理器响应请求取消，后台任务接受受管理的生命周期 context。`WithoutCancel` 的通用示例不能绕过任务管理；仅有 `ctx` 参数而未改变传播语义时无需额外审计。 |
| [golang-data-structures](.agents/skills/golang-data-structures/SKILL.md) | slice/map/array 的容量与复制语义，标准容器、泛型集合、Builder/Buffer、unsafe 与弱指针的适用场景。 | 选择数据结构、合并或分组采样、批量读模型组合、解释 slice/map 行为或实现集合时。 | 关注采样批量处理的内存归属与复制成本；意外别名配合 safety。已测得的热点优化用 performance，测量方法用 benchmark，不能只凭数据结构知识宣称性能提升。 |
| [golang-database](.agents/skills/golang-database/SKILL.md) | 参数化查询、扫描与 NULL、事务与隔离级别、行锁、连接池、批处理、数据库 context 及迁移工具使用。 | 编写或审查仓储查询、事务、锁顺序、幂等、连接池，或排查数据库行为时。 | 保留 MySQL/GORM 与所属领域表访问，schema 仍修改对应 `0001_init.sql`；技能不生成 schema 或迁移 SQL。资金、锁、唯一键和重放同时加载 testing，执行独立真库集成验证。 |
| [golang-modernize](.agents/skills/golang-modernize/SKILL.md) | 使用当前 Go 语言与标准库能力、替换弃用 API、更新惯用写法并评估版本升级风险。 | 遇到弃用告警、旧式循环或集合操作，用户要求现代化或升级 Go/tooling 时。 | 以 `go.mod` 的 Go 1.27 为准，核对行为和工具链兼容性；不为旧编译器降级写法。移包、抽函数或改变结构用 refactoring；静态检查配置用 lint。 |
| [golang-refactoring](.agents/skills/golang-refactoring/SKILL.md) | 分阶段、保持行为的重构；按风险建立验证依据，安全改名/抽取/内联/移包，拆解循环依赖。 | 函数或包职责过大、重复结构阻碍需求、批量重命名、迁移所属模块或规划多步重构时。 | 同时加载目标规则技能与 gopls；保持事务、锁顺序和幂等。参考既有重构路线与边界文档；通用分支、提交和叠加 PR 流程受本文件 Git 授权规则约束。 |

### 测试、诊断与性能（5 个）

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-testing](.agents/skills/golang-testing/SKILL.md) | 表驱动测试、子测试、并行与 fuzz、夹具、mock、泄漏检查、覆盖率、示例和集成测试设计。 | 新增或评审测试、选择验证层级、修复不稳定测试、验证业务故障边界时。 | 默认测试不访问真库；integration tag、缺库失败、独立连接和确定性并发闸门按本文件执行。涉及 testify API 再加载 stretchr-testify；不为纯文档改动增加业务测试。 |
| [golang-troubleshooting](.agents/skills/golang-troubleshooting/SKILL.md) | 从复现、错误与假设出发定位根因；结合日志、Delve、race、GODEBUG、pprof 捕获缩小问题范围。 | 程序崩溃、挂起、死锁、竞态、错误结果或偶发失败，尚未确定根因时。 | 从现有运行与验证入口收集证据，不凭差异猜原因；定位后加载相关修复技能。profile 的测量和解释用 benchmark，已明确热点的优化方案用 performance。 |
| [golang-benchmark](.agents/skills/golang-benchmark/SKILL.md) | 编写和运行基准、用 benchstat 比较结果，采集并解释 CPU/内存/阻塞 profile 与 trace。 | 建立性能基线、验证优化收益、区分分配和留存，或深入分析某项性能指标时。 | 记录工作负载、环境、命令与前后数据；外部数据库/网络瓶颈须单独识别。测量后用 performance 选择方案；结果不自动授权提交或建立额外常驻服务。 |
| [golang-performance](.agents/skills/golang-performance/SKILL.md) | 按瓶颈选择减分配、预分配、布局、池化、缓存、GC 与热点 CPU 优化策略。 | profile/benchmark 已识别瓶颈，或性能评审需要提出可测量的候选改进时。 | 先证据后优化，与 benchmark 配合验证；不得为缓存或吞吐改变金额、冻结快照、锁与幂等语义。未知原因的变慢先用 troubleshooting。 |
| [golang-observability](.agents/skills/golang-observability/SKILL.md) | 日常结构化日志、Prometheus 指标、OpenTelemetry 链路、持续 profiling、告警与信号关联。 | 给服务或新功能补监控、关联请求和设备命令、改日志或设置运行告警时。 | 保留请求、订单、命令等关联信息，控制指标基数并遵守敏感日志规则；日志 Handler 用 samber-slog。临时性能深挖用 benchmark，不能为监控泄露凭据或原始敏感报文。 |

### 工程、工具与依赖（8 个）

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-cli](.agents/skills/golang-cli/SKILL.md) | 命令结构、参数校验、配置、版本信息、退出码、stdin/stdout/stderr、信号、帮助与测试。 | 编写或修改迁移、种子数据、模拟器等 Go 命令行工具时。 | 保留 `cmd` 作为部署单元和生命周期入口，业务能力放所属包；遵守模拟器前台交互规则。只有使用 Cobra/Viper 时才加载其专项技能。 |
| [golang-continuous-integration](.agents/skills/golang-continuous-integration/SKILL.md) | GitHub Actions 的测试、lint、安全扫描、覆盖率、依赖更新机器人、构建与发布 job 编排。 | 修改 `.github/workflows/`、排查 CI 配置或增加质量门禁时。 | CI 沿用 tasks.mjs，默认检查与真实 MySQL integration job 分层；不把模拟库当真库验收。漏洞分析用 security/依赖审计技能；发布、push 或远端设置变更需已有授权。 |
| [golang-dependency-management](.agents/skills/golang-dependency-management/SKILL.md) | `go.mod/go.sum`、MVS、版本升级、replace/exclude/retract、vendor、tool/go.work、依赖树与 govulncheck。 | 添加、移除或升级依赖，解决版本冲突、核验依赖漏洞或分析间接依赖成本时。 | 涉及依赖运行 `go mod tidy -diff`；先核对当前锁定版本与 replace。库选型用 popular-libraries，发布信息用 pkg-go-dev，CI 自动更新 job 用 continuous-integration。 |
| [golang-gopls](.agents/skills/golang-gopls/SKILL.md) | 基于本地解析构建跳转定义、查引用与调用关系、读取包 API、诊断、安全重命名和代码抽取。 | 理解本地代码或当前依赖，改名/移包前确认影响，修改后核对诊断时。 | 先确认 CLI/LSP/MCP 的实际可用能力；关注锁定依赖和 replace 后源码。未安装依赖的公开 API、历史版本或许可证用 pkg-go-dev；整个重构过程用 refactoring。 |
| [golang-pkg-go-dev](.agents/skills/golang-pkg-go-dev/SKILL.md) | 通过 godig 查询公开 Go 包的文档、签名、示例、版本、许可证、importers 和已知漏洞。 | 查询某个包或模块的公开资料，包括尚未加入项目的库与指定发布版本时。 | 核对查询版本与本项目版本；godig 可用性另查。公开版本漏洞信息不等于本地调用可达性结论；本地代码用 gopls，升级用 dependency-management，库取舍用 popular-libraries。 |
| [golang-popular-libraries](.agents/skills/golang-popular-libraries/SKILL.md) | 按场景比较标准库与第三方库，衡量维护、许可证、使用情况和依赖成本。 | 用户要求推荐或比较库，或任务确实需要评估新依赖时。 | 先考虑标准库及现有依赖，不因推荐清单换掉 Gin/GORM/配置实现。选定后读该库专项技能；最新维护状态须核实，版本变更用 dependency-management。 |
| [golang-project-layout](.agents/skills/golang-project-layout/SKILL.md) | Go 包与模块布局、`cmd/internal/pkg`、多部署入口、go.work、拆包和基础工程配置。 | 新建 Go 项目、调整目录、拆包/拆模块，或讨论代码归属时。 | 本项目目标结构已定，以本文件与架构边界为准；新增/迁移文件同步目录图。既有代码迁移同时用 refactoring；不重新选择通用 clean/hexagonal 模板替代 use case 布局。 |
| [golang-stay-updated](.agents/skills/golang-stay-updated/SKILL.md) | Go 官方发布、学习资料、社区、新闻、博客、会议及生态关注渠道。 | 寻找学习路径、官方升级信息、社区渠道或了解生态变化时。 | 用于发现和学习，不作为当前 API/版本结论的唯一依据；具体模块资料用 pkg-go-dev，升级实施用 modernize 或 dependency-management，常规实现任务无需加载资讯目录。 |

### API 专项（3 个）

这些技能提供特定协议或文档工具的实施方法。现有服务间 HTTP 仍按 contracts、integration 与 serviceclient 组织；安装它们不代表要迁移 API 技术栈。

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-grpc](.agents/skills/golang-grpc/SKILL.md) | protobuf、服务端与客户端、拦截器、status 错误、deadline、流式 RPC、TLS/mTLS、bufconn 测试。 | 实际实现、评审或调试 gRPC/proto，或任务明确要求评估 gRPC 时。 | 配合 testing、error-handling、context；普通 Gin/HTTP 内部端点不因多进程架构而触发协议迁移。 |
| [golang-graphql](.agents/skills/golang-graphql/SKILL.md) | gqlgen/graphql-go、schema、resolver、DataLoader 防 N+1、鉴权、错误、订阅及复杂度限制。 | 实际编写或维护 GraphQL API/resolver，或明确进行 GraphQL 选型时。 | 配合 testing、error-handling、security；保持资源归属与数据范围，不能把 GraphQL schema 与现有 OpenAPI 混用。 |
| [golang-swagger](.agents/skills/golang-swagger/SKILL.md) | swaggo/swag 注释与生成流程、路由和安全声明、schema 标签及 HTTP 框架集成。 | 增加或维护 Swagger/OpenAPI，特别是使用 swaggo 注释或生成产物时。 | 本项目核对 `admin.openapi.json` 与 `user.openapi.json`、真实路由和响应。读取技能不要求切换为 swaggo 自动生成；保留现有文档方式，不能只按注释推断接口。 |

### DI 库专项（4 个）

依赖注入的整体取舍先用 dependency-injection；以下技能只处理具体库的 API 与装配方式。保留进程装配边界和资源释放顺序。

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-google-wire](.agents/skills/golang-google-wire/SKILL.md) | 编译期注入、provider set、接口绑定、cleanup、wireinject build tag 与生成 `wire_gen.go`。 | 项目实际使用 Wire，维护或明确评估编译期生成装配时。 | 核对生成代码、构建 tag 和失败清理；手工装配本身不需要 Wire，不因有构造函数就触发代码生成。 |
| [golang-uber-dig](.agents/skills/golang-uber-dig/SKILL.md) | 运行期反射容器，Provide/Invoke、In/Out 参数对象、命名值、值组、作用域与装饰。 | 实际使用或明确评估 `go.uber.org/dig`，排查容器依赖图时。 | 负责具体容器解析；需要 Fx 的生命周期和模块时用 uber-fx。不能以容器替代所属用例的窄接口或退出等待。 |
| [golang-uber-fx](.agents/skills/golang-uber-fx/SKILL.md) | Fx 应用装配、Module、Lifecycle、注解、值组、替换依赖、启动/停止 hook 与测试。 | 实际使用或明确评估 `go.uber.org/fx`，维护 `fx.New` 和资源生命周期时。 | 要证明启动失败清理、取消和任务等待符合现有语义；原始 Dig 容器 API 用 uber-dig，不自动把五个入口迁为 Fx。 |
| [golang-samber-do](.agents/skills/golang-samber-do/SKILL.md) | samber/do 类型化容器、provider、别名、作用域、服务生命周期、健康检查与关闭。 | 实际使用 `github.com/samber/do` 或 `/v2`，或明确评估类型化 DI 容器时。 | 与 dependency-injection 配合，核对版本和关闭顺序；普通手工注入不自动采用容器，也不把健康检查等同于完成业务任务退出。 |

### CLI 框架专项（2 个）

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-spf13-cobra](.agents/skills/golang-spf13-cobra/SKILL.md) | Cobra 命令树、RunE、hook、位置参数校验、局部/持久 flag、补全、帮助与命令测试。 | 实际使用或明确评估 `github.com/spf13/cobra`，修改子命令和参数行为时。 | 整体 CLI 设计用 cli；配置分层若用 Viper 再配合 spf13-viper。简单命令无需仅为技能而引入命令框架。 |
| [golang-spf13-viper](.agents/skills/golang-spf13-viper/SKILL.md) | Viper 的 flag/env/file/default 优先级、环境变量绑定、反序列化、子配置、热更新与测试隔离。 | 实际使用或明确评估 `github.com/spf13/viper`，处理配置覆盖或热更新时。 | 现有进程类型化配置与 Compose/模板同步规则优先；不把 Viper 热更新当成本项目已具备能力，不改成跨进程共享全量凭据。 |

### samber 库专项（6 个，Do 已列于 DI）

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-samber-hot](.agents/skills/golang-samber-hot/SKILL.md) | 内存缓存算法、TTL、loader、容量、分片、过期刷新、缺失缓存与指标。 | 实际使用或明确评估 `github.com/samber/hot`，重复读取造成可测量压力且需要进程内缓存时。 | 配合 performance/benchmark 证明收益；实时权限、余额、幂等事实和资金准入不能被缓存旧值替代，跨进程一致性单独设计。 |
| [golang-samber-lo](.agents/skills/golang-samber-lo/SKILL.md) | 有限 slice/map 的 Map/Filter/Reduce、分组去重；并发、原地修改与惰性迭代变体。 | 实际使用或明确评估 `github.com/samber/lo`，处理有限集合转换时。 | 配合 data-structures 和必要的性能验证，比较普通循环与标准库的可读性及分配；持续异步流用 samber-ro，不能因 helper 自动并发执行带事务副作用的操作。 |
| [golang-samber-mo](.agents/skills/golang-samber-mo/SKILL.md) | Option/Result/Either、Future/IO/Task/State 等类型化可空值、错误值与函数式组合。 | 实际使用或明确评估 `github.com/samber/mo`，设计可空类型或函数式组合 API 时。 | 普通 nil 安全用 safety，原生错误包装用 error-handling；不能仅为使用 Option/Result 改写现有 wire DTO、NULL 或业务错误契约。 |
| [golang-samber-oops](.agents/skills/golang-samber-oops/SKILL.md) | oops 错误 builder、上下文属性、堆栈、错误码、包装、panic 恢复及日志集成。 | 实际使用或明确评估 `github.com/samber/oops`，需要维护结构化错误时。 | 配合 error-handling；保留 `errors.Is/As` 与客户端响应映射，开发者诊断和用户消息分开，错误属性不携敏感信息。 |
| [golang-samber-ro](.agents/skills/golang-samber-ro/SKILL.md) | ReactiveX 流、冷热 observable、subject、Pipe 算子、背压、错误传播及 context 集成。 | 实际使用或明确评估 `github.com/samber/ro`，实现持续异步事件或实时数据流时。 | 同时核对 concurrency/context；有限集合转换用 samber-lo。响应式流不能替代持久化事件、重试与幂等，关闭时仍须等待受管理任务。 |
| [golang-samber-slog](.agents/skills/golang-samber-slog/SKILL.md) | slog Handler 组合、分流、采样、属性变换、HTTP 日志中间件与后端投递。 | 使用 slog 或 `github.com/samber/slog-*`，确实需要设计/审查日志处理管道时。 | 基本日志规则与信号设计配合 error-handling/observability；标准库够用时保留标准库。核对字段脱敏、采样和开销，审计记录不得被普通日志采样替代。 |

### 测试库专项（1 个）

| 技能 | 主要用途 | 使用时机 | 本项目应用与边界 |
| --- | --- | --- | --- |
| [golang-stretchr-testify](.agents/skills/golang-stretchr-testify/SKILL.md) | assert/require、mock 期望和参数匹配、调用核验、suite 生命周期、Eventually/JSONEq 等断言。 | 测试实际使用 `github.com/stretchr/testify`，或明确评估该库及选择 assert/require/mock/suite 时。 | 先用 testing 确定测试层级；普通 `testing` 测试无需引入 testify。mock 与 Eventually 不能代替真实 MySQL 的行锁、唯一键和确定性并发验证。 |

### 本项目常见任务的技能组合

下表给出主技能与常用辅助技能；仍按具体改动增减。纯审查时可先报告发现，不因加载测试技能而扩大实现范围。

| 任务 | 优先加载 | 需要核对的项目约束 |
| --- | --- | --- |
| 展开长调用或结构体字段，提高可读性 | code-style；涉及名称时加 naming | 保持行为与字段值，执行 `node tasks.mjs fmt`。 |
| 调整 `cmd/*` 资源装配与停机 | dependency-injection、design-patterns、context、concurrency | 初始化失败释放资源，先等处理器和任务退出再关闭连接池。 |
| 修改充值、退款、结算与幂等事务 | database、error-handling、testing；涉及金额转换加 safety | 资金写入归属、锁顺序、当前读、唯一键、稳定请求标识与独立真库集成测试。 |
| 调整 worker/gateway 后台循环与重试 | concurrency、context、error-handling、testing | 明确取消和等待路径，复用已有内部接口，重试不换请求身份。 |
| 修改 DC589 解析与计量证据 | safety、testing、error-handling；涉及共享会话加 concurrency | 帧长、校验、BCD、字节序、单位、会话匹配和持久化先后顺序。 |
| 修改登录、MFA、鉴权或敏感日志 | security、error-handling、testing；涉及事务加 database | 实时权限、用户归属、数据范围、凭据隔离和脱敏，核验实际可达路径。 |
| 移包、拆用例或批量改名 | refactoring、gopls，配合 project-layout、naming 或 structs-interfaces | 更新目录图与架构记录，保持契约、事务和幂等，不自动执行 Git 写操作。 |
| 修改服务间 HTTP 契约 | structs-interfaces、naming、context、error-handling、testing | contracts/integration 依赖方向、Request/Reply、JSON 字段与单位、处理器和客户端双向契约。 |
| 排查慢查询或设备采样热点 | troubleshooting 定位，benchmark 测量，performance 改进；SQL 加 database | 区分数据库/网络与 Go 热点，记录基线及前后结果，保持业务正确性。 |
| 修改依赖版本或 Go 能力使用 | dependency-management 或 modernize；公开资料用 pkg-go-dev | 以本地 `go.mod/go.sum` 为准，依赖改动检查 `go mod tidy -diff`，核对构建与测试。 |
| 修改 Go CI 或真库测试 job | continuous-integration，配合 lint、testing、dependency-management | `node tasks.mjs check` 与 integration 分层，缺库失败，构建产物位于 `bin/`。 |
| 修改 README、AGENTS 或 API 说明 | documentation；涉及 OpenAPI 工具时加 swagger | 文档职责分工、真实路由与接口核对、链接有效；纯文档不运行无关业务测试。 |

以上组合中的简称均指 `golang-` 前缀技能，例如 `database` 指 `golang-database`。技能增加、移除或更新时检查本节的条目、链接、用途与组合是否仍与本地安装一致。

## 工具与代码风格

- Go 版本以 `go.mod` 为准，当前为 Go 1.27；使用其标准库与语言能力，不为旧编译器改写代码。
- 后端检查直接使用 tasks.mjs：`node tasks.mjs fmt` 调用 `go fmt ./...`，`node tasks.mjs lint` 调用 `go vet ./...`，`node tasks.mjs test` 调用 `go test ./...`；`node tasks.mjs check` 顺序执行 `check-rules`、fmt、lint、test 四项，其中规则层检查 `node tasks.mjs check-rules` 只读文档与迁移 schema（运行方式见 [运行与数据维护](docs/operations.md)）。**涉及 GORM 投影、枚举参数、状态写入、行锁或幂等的改动，除默认门禁外还必须跑 `go vet -tags integration ./...` 与 `node scripts/db/test-integration.mjs`**：前者能在真库执行前发现类型断裂，后者是唯一能发现单元素 `IN` 列表、map 动态类型比较与 GORM Statement 复用问题的门禁（路线图 9.2 节登记了六个此类回归）。目录、依赖方向、表归属与接口契约按本规范和代码评审核对，不使用架构检查脚本。
- **真库集成测试单列一层**：`node tasks.mjs test-integration`（预先设置 `TEST_MYSQL_URL`） 带 `//go:build integration` 跑真实 MySQL。`node tasks.mjs check` 不含它（不依赖 Docker）。2026-10-06 起 CI workflow 与独立测试实例编排（compose.test.yaml）已移除，集成测试经 `scripts/db/test-integration.mjs` 直接对开发环境执行；行锁、唯一键冲突、幂等重放、`SKIP LOCKED` 需要真实 MySQL 语义验证，详见「并发与幂等」。
- 目录结构定稿如下。后端 Go 模块整体位于 `backend/`（含 `cmd/`、`internal/`、`migrations/`、`go.mod` 与服务镜像 `Dockerfile`），下图为 `backend/` 内部结构；本文件后文与 `docs/` 提到的 `cmd/`、`internal/`、`migrations/` 路径均相对 `backend/`。新增或迁移文件时同步维护本图，括号内注明迁移前的位置：

```text
wattharbor/backend/          Go 后端模块（仓库根布局见本图末尾）
│
├── cmd                                                   进程入口
│   ├── wattharbor/main.go                               单体入口
│   ├── {admin,central,gateway,user,worker}/main.go         五进程部署入口
│   ├── devseed/{main,scheme}.go
│   ├── migrate/{main,unified}.go
│   └── simulator-dc589/main.go
├── internal/application                                 进程装配，不承载业务规则
│   ├── {admin,central,gateway,user,worker}/                装配、资源与路由（迁移前：cmd/<服务>/）
│   └── monolith/                                         单体生命周期、公开路由与进程内传输
├── internal/charging                                     充电生命周期与只读组合：按业务步骤组织子包
│   ├── internal/charging/start                           启动授权、待启动查询与启动回执
│   │   ├── internal/charging/start/authorization.go（迁移前：internal/charging/authorization.go[启动部分]）
│   │   ├── internal/charging/start/authorization_repo.go（迁移前：internal/charging/authorization_store.go[启动查询]）
│   │   ├── internal/charging/start/pending_repo.go（迁移前：internal/charging/paid_start_store.go）
│   │   ├── internal/charging/start/receipt_schema.go（启动回执与启动阶段端口占用的写模型）
│   │   └── internal/charging/start/result_repo.go（迁移前：internal/charging/start_result_store.go）
│   ├── internal/charging/stop                            停止授权、自动停机与计费截止冻结
│   │   ├── internal/charging/stop/authorization.go（迁移前：internal/charging/authorization.go[停止部分]）
│   │   ├── internal/charging/stop/authorization_repo.go（迁移前：internal/charging/authorization_store.go[停止查询]）
│   │   ├── internal/charging/stop/auto_stop.go（迁移前：internal/charging/auto_stop.go[编排]、card_deadline.go[调用]）
│   │   ├── internal/charging/stop/auto_stop_repo.go（迁移前：internal/charging/auto_stop_store.go[订单查询]）
│   │   ├── internal/charging/stop/cutoff_repo.go（迁移前：internal/charging/authorization_store.go、auto_stop_store.go[截止记录]）
│   │   ├── internal/charging/stop/deadline_repo.go（迁移前：internal/charging/card_deadline_store.go）
│   │   ├── internal/charging/stop/gateway.go（迁移前：internal/charging/auto_stop.go[网关调用]）
│   │   └── internal/charging/stop/meter.go（迁移前：internal/charging/auto_stop.go[计量判断]）
│   ├── internal/charging/finish                           结束回执、端口释放与结算任务提交
│   │   ├── internal/charging/finish/receipt_schema.go（结束回执写模型、启动回执只读投影与端口释放模型）
│   │   └── internal/charging/finish/result_repo.go（迁移前：internal/charging/finish/result_repo.go）
│   ├── internal/charging/measurement                     无 IO 的协议事件与计量证据转换
│   │   └── internal/charging/measurement/samples.go（迁移前：internal/charging/telemetry.go）
│   └── internal/charging/query                           只读组合与实时估算（新增充电子包）
│       ├── internal/charging/query/user.go（迁移前：internal/interface/http/charge_user_query.go[详情与快照组合]）
│       ├── internal/charging/query/user_curve.go（迁移前：internal/interface/http/charge_user_query.go[窗口与曲线]）
│       ├── internal/charging/query/user_display.go（迁移前：internal/interface/http/charge_scheme_view.go[冻结资费文案]）
│       ├── internal/charging/query/user_repo.go（迁移前：internal/interface/http/charge_scheme_view.go[所属读取组合]）
│       ├── internal/charging/query/process.go（迁移前：internal/interface/http/admin/order/order_process.go[组合读取]）
│       ├── internal/charging/query/live.go（迁移前：internal/interface/http/charge_live_meter.go[估算]、admin/order/order_live.go[组合]）
│       └── internal/charging/query/live_evidence.go（迁移前：internal/interface/http/charge_live_meter.go[观测计量]）
├── internal/order                                        订单表模型、编号分配与读模型
│   ├── internal/order/feedback.go（迁移前：internal/interface/http/charge_feedback.go、admin/order/casework.go[反馈业务]）
│   ├── internal/order/feedback_repo.go（迁移前：internal/device/user_fault_store.go[评价表访问]、internal/infrastructure/database/admin_case_store.go[反馈]）
│   ├── internal/order/funding_repo.go（新增：资金及开票事务的所属订单能力）
│   ├── internal/order/lifecycle_repo.go（新增：启动与结束阶段的订单锁读及状态推进能力）
│   ├── internal/order/card_session_repo.go（迁移前：internal/card/card_store.go[续购订单锁]）
│   ├── internal/order/order_history_repo.go（迁移前：internal/payment/order_authorization_store.go[历史视图部分]）
│   ├── internal/order/admin_query_repo.go（迁移前：internal/settlement/order_view_repo.go[订单投影]、internal/ops/admin_projection_store.go[订单事件]）
│   ├── internal/order/query.go（迁移前：internal/interface/http/charge_user_query.go[归属与历史]、admin/order/orders.go[基础查询]）
│   ├── internal/order/query_snapshot_repo.go（新增：实时估算所需冻结快照批量读取）
│   ├── internal/order/query_repo.go（迁移前：internal/interface/http/charge_user_query.go[订单读取]）
│   ├── internal/order/order_number_repo.go（迁移前：internal/order/order_number.go[编号分配]）
│   ├── internal/order/order_number.go
│   ├── internal/order/order_package_repo.go（迁移前：internal/order/order_package_store.go）
│   ├── internal/order/order_schema.go
│   ├── internal/order/order_state_repo.go（迁移前：internal/order/order_state_store.go）
│   ├── internal/order/order_view_repo.go（迁移前：internal/order/order_view_store.go）
│   └── internal/order/pricing_snapshot_repo.go（迁移前：internal/order/pricing_snapshot_store.go）
├── internal/settlement                                   结算：计价、账单、发票与退款
│   ├── internal/settlement/billing.go（迁移前：internal/interface/http/billing_service.go[调度]）
│   ├── internal/settlement/invoice.go（迁移前：internal/interface/http/user_invoice.go[申请]）
│   ├── internal/settlement/invoice_review.go（迁移前：internal/interface/http/admin/finance/invoices.go[审核]）
│   ├── internal/settlement/meter_review.go（迁移前：internal/interface/http/admin/device/meter_reviews.go[核实]）
│   ├── internal/settlement/refund.go（迁移前：internal/interface/http/admin/finance/refunds.go[人工退款]）
│   ├── internal/settlement/refund_creation_repo.go（迁移前：payment/callback、wallet/reservation[退款记录]）
│   ├── internal/settlement/money_schema.go（迁移前：internal/payment/money_schema.go[费用与退款模型]）
│   ├── internal/settlement/balance_refund_repo.go（迁移前：internal/card/card_refund_store.go、internal/interface/http/order_start_result.go[共享退款]）
│   ├── internal/settlement/bill_repo.go
│   ├── internal/settlement/user_bill.go（迁移前：internal/interface/http/settlement_bill.go[归属查询与已读编排]）
│   ├── internal/settlement/billing_job_repo.go
│   ├── internal/settlement/calculation_number.go
│   ├── internal/settlement/fee_calculation_repo.go
│   ├── internal/settlement/invoice_repo.go
│   ├── internal/settlement/manual_repo.go
│   ├── internal/settlement/meter_review_repo.go
│   ├── internal/settlement/query_fee_repo.go（迁移前：internal/interface/http/charge_user_query.go[费用批量读取]）
│   ├── internal/settlement/order_view_repo.go
│   ├── internal/settlement/refund_executor_repo.go
│   ├── internal/settlement/refund_record_repo.go（迁移前：internal/payment/order_start_repo.go[退款记录部分]）
│   ├── internal/settlement/refund_review_list_repo.go
│   ├── internal/settlement/refund_review_repo.go
│   └── internal/settlement/user_invoice_repo.go
├── internal/wallet                                       钱包：余额、充值、流水、退款预占、频次风控
│   ├── internal/wallet/recharge_history.go（迁移前：internal/interface/http/admin/user/charge_users.go[充值读取能力]）
│   ├── internal/wallet/recharge.go（迁移前：internal/interface/http/user_wallet.go[充值]）
│   ├── internal/wallet/refund.go（迁移前：internal/interface/http/user_wallet.go[退款]）
│   ├── internal/wallet/risk.go（迁移前：internal/interface/http/admin/finance/wallet_risks.go[风险事务]）
│   ├── internal/wallet/query.go（迁移前：internal/interface/http/user_wallet.go[查询组合]）
│   ├── internal/wallet/query_repo.go（新增：本页退款详情批量查询）
│   ├── internal/wallet/recharge_credit_repo.go（迁移前：internal/payment/payment_callback_store.go[钱包到账]）
│   ├── internal/wallet/admin_wallet_risk_repo.go
│   ├── internal/wallet/wallet_account_repo.go
│   ├── internal/wallet/wallet_ledger_repo.go
│   ├── internal/wallet/wallet_model.go
│   ├── internal/wallet/wallet_refund_repo.go
│   └── internal/wallet/wallet_reservation_repo.go
├── internal/pricing                                      计费方案、规则、发布与计量匹配
│   ├── internal/pricing/query.go（迁移前：internal/interface/http/admin/pricing/charging_schemes.go[读取组合]）
│   ├── internal/pricing/scheme.go（迁移前：internal/interface/http/admin/pricing/charging_schemes.go[模板变更事务]）
│   ├── internal/pricing/publication.go（迁移前：internal/interface/http/admin/pricing/charging_schemes.go[发布与继承事务]）
│   ├── internal/pricing/metering_repo.go（迁移前：internal/pricing/metering_store.go）
│   ├── internal/pricing/pricing_admin_repo.go（迁移前：internal/pricing/pricing_admin_store.go）
│   ├── internal/pricing/pricing_scope_repo.go（迁移前：internal/pricing/pricing_scope_store.go）
│   ├── internal/pricing/pricing_repo.go（迁移前：internal/pricing/pricing_store.go）
│   └── internal/pricing/scheme_target_repo.go（迁移前：internal/pricing/scheme_target_store.go）
├── internal/card                                         刷卡：在线卡、扣款与累计时长
│   ├── internal/card/admin.go（迁移前：internal/interface/http/admin/user/online_cards.go[事务]）
│   ├── internal/card/status.go（迁移前：internal/interface/http/card_api.go[用户卡]、card_balance.go）
│   ├── internal/card/swipe.go（迁移前：internal/interface/http/card_api.go[刷卡准入]）
│   ├── internal/card/card_admin_repo.go
│   ├── internal/card/extension.go（迁移前：internal/card/card_store.go[续购纯规则]）
│   ├── internal/card/card_schema.go
│   ├── internal/card/card_repo.go
│   ├── internal/card/query_operations_repo.go（迁移前：internal/interface/http/charge_scheme_view.go[原操作读取]）
│   ├── internal/card/query_purchase_repo.go（迁移前：internal/interface/http/charge_live_meter.go[卡购买读取]）
│   ├── internal/card/charge_repo.go
│   ├── internal/card/interface_query_repo.go
│   ├── internal/card/start_result_repo.go（迁移前：internal/payment/order_start_repo.go[刷卡写入部分]）
│   └── internal/card/user_card_repo.go
├── internal/device                                       设备接入、状态、会话、故障
│   ├── internal/device/fault_report.go（迁移前：internal/interface/http/user_fault.go[提交与所属查询]）
│   ├── internal/device/fault_casework.go（迁移前：internal/interface/http/admin/order/casework.go[流转事务]）
│   ├── internal/device/fault_repo.go（迁移前：internal/infrastructure/database/admin_case_store.go[报修表访问]）
│   ├── internal/device/discovery.go（迁移前：internal/interface/http/payment_scan.go[事实组合]）
│   ├── internal/device/operation_repo.go（迁移前：internal/payment/payment_device_store.go）
│   ├── internal/device/alert.go（迁移前：internal/interface/http/admin/ops/operations.go[告警查询与认领]）
│   ├── internal/device/alert_sync.go（迁移前：internal/interface/http/admin/alertsync/sync.go[完整同步与恢复]）
│   ├── internal/device/alert_repo.go（迁移前：internal/device/alert_sync_store.go、internal/ops/admin_ops_store.go[告警表访问]）
│   ├── internal/device/admin_repo.go（迁移前：internal/device/device_admin_store.go）
│   ├── internal/device/device_import.go
│   ├── internal/device/import.go（迁移前：internal/interface/http/admin/device/device_imports.go[导入受理与执行]）
│   ├── internal/device/import_repo.go（迁移前：internal/device/device_import_job_store.go）
│   ├── internal/device/device_repo.go（迁移前：internal/device/device_store.go）
│   ├── internal/device/management.go（迁移前：internal/interface/http/admin/device/devices.go[读取组合、资料与启停]）
│   ├── internal/device/management_input.go（迁移前：internal/device/device_store.go[完整资料校验]）
│   ├── internal/device/vendor.go（迁移前：internal/interface/http/admin/vendor/[范围、字典编排与审计]）
│   ├── internal/device/fault.go
│   ├── internal/device/identity.go
│   ├── internal/device/user_station.go（迁移前：internal/interface/http/user_station.go[公开查询与运行态组合]）
│   ├── internal/device/user_station_repo.go（迁移前：internal/device/interface_query_store.go[站点／设备读取]）
│   ├── internal/device/scope.go
│   ├── internal/device/station.go（迁移前：internal/interface/http/admin/station/stations.go[新建与更新]）
│   ├── internal/device/station_repo.go（迁移前：internal/device/station_store.go）
│   └── internal/device/user_fault_repo.go（迁移前：internal/device/user_fault_store.go）
├── internal/identity                                     用户、会话、登录与手机号
│   ├── internal/identity/auth.go
│   ├── internal/identity/card_user_repo.go（迁移前：internal/card/card_admin_store.go[用户事实]）
│   ├── internal/identity/charge_user_repo.go（迁移前：internal/identity/charge_user_store.go）
│   ├── internal/identity/development.go
│   ├── internal/identity/user.go
│   ├── internal/identity/user_repo.go（迁移前：internal/identity/user_repository_store.go）
│   ├── internal/identity/user_schema.go（迁移前：internal/identity/user_repository_store.go[列模型]）
│   ├── internal/identity/session.go（迁移前：internal/interface/http/user_api.go[身份流程]）
│   ├── internal/identity/session_repo.go（迁移前：internal/infrastructure/redis/session_store.go）
│   ├── internal/identity/phone.go（迁移前：internal/interface/http/phone_api.go[号码来源及准入]）
│   ├── internal/identity/query.go（迁移前：internal/interface/http/admin/user/charge_users.go[查询编排]）
│   └── internal/identity/wechat.go
├── internal/marketing                                    优惠券、活动规则与核销
│   ├── internal/marketing/activity.go（迁移前：internal/interface/http/admin/marketing/coupon_activity.go[业务准入与审计事务]）
│   ├── internal/marketing/activity_grant.go（迁移前：internal/marketing/coupon_activity_store.go[活动求值与幂等编排]）
│   ├── internal/marketing/coupon.go（迁移前：internal/interface/http/admin/marketing/coupons.go、coupon_store.go、coupon_redeem_store.go[准入与核销编排]）
│   ├── internal/marketing/query.go（迁移前：internal/interface/http/user_coupon.go、coupon_api.go[持券视图]）
│   ├── internal/marketing/rules_coupon_discount.go（迁移前：internal/marketing/coupon_redeem_store.go[优惠计算]）
│   ├── internal/marketing/admin_activity_repo.go（迁移前：internal/marketing/admin_activity_store.go）
│   ├── internal/marketing/admin_coupon_repo.go（迁移前：internal/marketing/admin_coupon_store.go）
│   ├── internal/marketing/coupon_activity_repo.go（迁移前：internal/marketing/coupon_activity_store.go）
│   ├── internal/marketing/coupon_redeem_repo.go（迁移前：internal/marketing/coupon_redeem_store.go）
│   ├── internal/marketing/coupon_repo.go（迁移前：internal/marketing/coupon_store.go）
│   ├── internal/marketing/interface_query_repo.go（迁移前：internal/marketing/interface_query_store.go）
│   ├── internal/marketing/rules_coupon.go
│   └── internal/marketing/rules_coupon_activity.go
├── internal/payment                                      支付意图、渠道、回调与支付记录
│   ├── internal/payment/checkout.go（迁移前：internal/interface/http/payment_payment_http.go[支付准备]）
│   ├── internal/payment/callback.go（新增：迟到退款事务内契约）
│   ├── internal/payment/recharge_payment_repo.go（迁移前：internal/interface/http/user_wallet.go[支付与参数]）
│   ├── internal/payment/recharge_source_repo.go（迁移前：internal/wallet/wallet_reservation_store.go[支付来源]）
│   ├── internal/payment/refund_payment_repo.go（迁移前：internal/settlement/refund_executor_store.go[支付锁及更新]）
│   ├── internal/payment/simulation_repo.go（迁移前：internal/interface/http/payment_development_payment.go[模拟确认]）
│   ├── internal/payment/card_funding_repo.go（迁移前：internal/card/card_store.go[卡支付写入]）
│   ├── internal/payment/channel_factory.go
│   ├── internal/payment/channel_refund.go
│   ├── internal/payment/channel_simulation.go
│   ├── internal/payment/channel_wechat.go
│   ├── internal/payment/errors.go
│   ├── internal/payment/interface_query_repo.go
│   ├── internal/payment/money_schema.go
│   ├── internal/payment/order_start_repo.go
│   ├── internal/payment/payment_callback_repo.go
│   ├── internal/payment/payment_checkout_repo.go
│   ├── internal/payment/payment_intent_repo.go
│   ├── internal/payment/payment_view_repo.go
│   ├── internal/payment/order_query.go（迁移前：后台支付订单HTTP的查询入口）
│   └── internal/payment/recharge_history_repo.go（迁移前：internal/payment/user_recharge_repo.go[支付历史投影]）
├── internal/ops                                          后台账号、角色、审计与事件发件箱
│   ├── internal/ops/audit.go（迁移前：internal/interface/http/admin_resource.go[事务内审计]）
│   ├── internal/ops/announcement_query.go（迁移前：internal/interface/http/user_announcement.go、user_station.go[公告规则]）
│   ├── internal/ops/announcement_query_repo.go（迁移前：internal/device/interface_query_store.go[公告读取]）
│   ├── internal/ops/session_repo.go（迁移前：internal/infrastructure/redis/admin_session.go）
│   ├── internal/ops/session.go（迁移前：internal/interface/http/admin_auth.go[认证、实时权限及签发]）
│   ├── internal/ops/auth_rate_repo.go（迁移前：internal/interface/http/admin_auth.go[登录频次IO]）
│   ├── internal/ops/mfa.go（迁移前：internal/interface/http/admin_mfa_settings.go[安全设置规则及事务]）
│   ├── internal/ops/mfa_repo.go（迁移前：internal/interface/http/admin_mfa_settings.go[短期凭据、租锁及频次IO]）
│   ├── internal/ops/account_repo.go（迁移前：internal/ops/admin_account_store.go）
│   ├── internal/ops/account_management.go（迁移前：internal/interface/http/admin_accounts.go、admin/ops/operations.go[账号管理事务]）
│   ├── internal/ops/management.go（迁移前：后台HTTP[当前准入与角色集合约束]）
│   ├── internal/ops/role.go（迁移前：internal/interface/http/admin_roles.go[角色事务]）
│   ├── internal/ops/audit_query.go（迁移前：internal/interface/http/admin/ops/audit_log.go[查询组合]）
│   ├── internal/ops/audit_query_repo.go（迁移前：internal/ops/admin_audit_store.go）
│   ├── internal/ops/announcement.go（迁移前：internal/interface/http/admin/ops/operations.go[公告发布事务]）
│   ├── internal/ops/announcement_repo.go（迁移前：internal/ops/admin_ops_store.go[公告表访问]）
│   ├── internal/ops/dashboard.go（迁移前：internal/infrastructure/database/admin_dashboard_store.go[日界与趋势组合]）
│   ├── internal/ops/dashboard_repo.go（迁移前：internal/infrastructure/database/admin_dashboard_store.go[只读投影]）
│   ├── internal/ops/role_repo.go（迁移前：internal/ops/admin_role_store.go）
│   ├── internal/ops/scope_repo.go（迁移前：internal/ops/admin_scope_store.go）
│   ├── internal/ops/scope_query.go（迁移前：后台HTTP的数据范围读取入口）
│   ├── internal/ops/account_management_repo.go（迁移前：internal/ops/admin_user_store.go）
│   ├── internal/ops/event_outbox_repo.go（迁移前：internal/ops/event_outbox_store.go）
│   └── internal/ops/audit_repo.go（迁移前：internal/ops/interface_query_store.go）
├── internal/export                                       后台批量导出
│   ├── internal/export/cleanup.go（迁移前：cmd/admin/main.go[过期文件清理循环]）
│   ├── internal/export/format.go（迁移前：internal/interface/http/admin/export/exports_format.go）
│   ├── internal/export/service.go（迁移前：internal/interface/http/admin/export/exports.go[资源、申请与审计]）
│   ├── internal/export/generation.go（迁移前：internal/interface/http/admin/export/exports.go[生成与失败恢复]）
│   ├── internal/export/transaction.go（受管理生成事务，回调退出后释放文件操作锁）
│   ├── internal/export/artifact.go（迁移前：internal/interface/http/admin/export/exports.go[文件发布与路径]）
│   ├── internal/export/download.go（迁移前：internal/interface/http/admin/export/exports.go[下载与清理]）
│   └── internal/export/export_task_repo.go（迁移前：internal/export/export_task_store.go）
├── internal/contracts                                    服务间 HTTP 的端点、共享 DTO 与错误契约
│   ├── internal/contracts/central/alerts.go（迁移前：internal/worker/alerts/sync.go[同步DTO]）
│   ├── internal/contracts/central/authorization.go（迁移前：internal/gateway/control/authorize.go[HTTP 授权响应]）
│   ├── internal/contracts/central/card.go（迁移前：internal/interface/http/card_api.go、worker/charge/cards.go[服务 DTO]）
│   ├── internal/contracts/central/charging.go（迁移前：internal/worker/charge/paid_start.go[待启动响应]）
│   ├── internal/contracts/central/errors.go
│   ├── internal/contracts/central/jobs.go（迁移前：central计费／退款／停机原HTTP回执；无业务参数Request及原进度Reply）
│   ├── internal/contracts/central/results.go（迁移前：internal/interface/http/order_*_result.go、internal/worker/charge/*sync.go[回执 DTO]）
│   ├── internal/contracts/gateway/provision.go（迁移前：后台导入／厂商 HTTP DTO）
│   ├── internal/contracts/gateway/summary.go（迁移前：后台设备 HTTP 摘要 DTO）
│   ├── internal/contracts/gateway/scan.go（新增：扫码端点与缺失错误契约）
│   ├── internal/contracts/gateway/card.go（迁移前：internal/worker/charge/cards.go、gateway/control/card_delivery.go[服务 DTO]）
│   ├── internal/contracts/gateway/query.go（迁移前：gateway/control/telemetry.go[DTO]、HTTP/user与admin/order[重复查询DTO]）
│   ├── internal/contracts/gateway/alerts.go（迁移前：internal/interface/http/admin/alertsync/sync.go、internal/gateway/control/device_faults.go[证据DTO]）
│   ├── internal/contracts/gateway/charging.go（START、STOP 与计量证据契约）
│   └── internal/contracts/gateway/sync.go（迁移前：internal/worker/charge/*sync.go、meter_segments.go[同步 DTO]）
├── internal/integration                                  服务间 HTTP 的专用客户端
│   ├── internal/integration/central/alerts.go（迁移前：internal/worker/alerts/sync.go[触发HTTP]）
│   ├── internal/integration/central/authorization.go（迁移前：internal/gateway/control/authorize.go[HTTP]）
│   ├── internal/integration/central/card.go（迁移前：internal/worker/charge/cards.go[central HTTP]）
│   ├── internal/integration/central/client.go（迁移前：internal/worker/charge/paid_start.go[待启动 HTTP 查询]）
│   ├── internal/integration/central/jobs.go（迁移前：worker资金任务通用HTTP；由cmd注入三个消费者窄接口）
│   ├── internal/integration/central/results.go（迁移前：internal/worker/charge/*sync.go[回执 HTTP]）
│   ├── internal/integration/gateway/provision.go（迁移前：后台设备导入出站 HTTP）
│   ├── internal/integration/gateway/vendor.go（迁移前：后台厂商通用出站 HTTP）
│   ├── internal/integration/gateway/summary.go（迁移前：后台设备摘要通用出站 HTTP）
│   ├── internal/integration/gateway/scan.go（迁移前：internal/interface/http/payment_scan.go[出站 HTTP]）
│   ├── internal/integration/gateway/card.go（迁移前：internal/worker/charge/cards.go[gateway HTTP]）
│   ├── internal/integration/gateway/query.go（迁移前：internal/interface/http/admin/order/order_process.go[HTTP代理]、charge_user_query.go[用户曲线HTTP]）
│   ├── internal/integration/gateway/alerts.go（迁移前：internal/interface/http/admin/alertsync/sync.go[证据HTTP]）
│   ├── internal/integration/gateway/client.go
│   └── internal/integration/gateway/sync.go（迁移前：internal/worker/charge/sync_client.go[启动/结束同步]）
├── internal/delivery                                     对外事件契约与签名投递器
│   ├── internal/delivery/events.go
│   └── internal/delivery/senders.go
├── internal/gateway                                      设备接入运行时
│   ├── internal/gateway/control/authorize.go
│   ├── internal/gateway/control/card_delivery.go
│   ├── internal/gateway/control/cards.go
│   ├── internal/gateway/control/charge_process.go
│   ├── internal/gateway/control/charging_samples.go
│   ├── internal/gateway/control/compensate.go
│   ├── internal/gateway/control/device_events.go（迁移前：internal/gateway/control/telemetry.go）
│   ├── internal/gateway/control/device_faults.go
│   ├── internal/gateway/control/device_summary.go
│   ├── internal/gateway/control/http.go
│   ├── internal/gateway/control/order_curve.go
│   ├── internal/gateway/control/scan.go
│   ├── internal/gateway/control/session.go
│   ├── internal/gateway/control/start.go
│   ├── internal/gateway/control/user_stop.go
│   ├── internal/gateway/control/worker_sync.go
│   ├── internal/gateway/control/worker_sync_models.go
│   ├── internal/gateway/listener/server.go
│   ├── internal/gateway/provision/http.go
│   ├── internal/gateway/provision/vendors.go
│   ├── internal/gateway/runtime/runtime.go（迁移前：cmd/gateway/main.go[设备接入与停止重试]）
│   ├── internal/gateway/session/registry.go
│   ├── internal/gateway/simulator/dc589/behavior.go
│   ├── internal/gateway/simulator/dc589/simulator.go
│   ├── internal/gateway/simulator/dc589/tui.go
│   ├── internal/gateway/repo/card_delivery_repo.go（迁移前：internal/gateway/store/card_delivery_store.go、control/card_delivery.go[决策读取事务与冻结操作]）
│   ├── internal/gateway/repo/charge_process_repo.go（迁移前：internal/gateway/store/charge_process_store.go）
│   ├── internal/gateway/repo/charge_start_repo.go（迁移前：internal/gateway/store/charge_start_store.go）
│   ├── internal/gateway/repo/compensation_repo.go（迁移前：internal/gateway/store/compensation_store.go）
│   ├── internal/gateway/repo/device_event_repo.go（迁移前：internal/gateway/store/device_event_store.go、internal/gateway/control/device_faults.go[标记事务与完整心跳读取]）
│   ├── internal/gateway/repo/end_delivery_repo.go（迁移前：internal/gateway/control/worker_sync.go[结束回执冻结]、internal/gateway/repo/telemetry_read_repo.go[冻结记录读写]）
│   ├── internal/gateway/repo/gorm_models.go（迁移前：internal/gateway/store/gorm_models.go）
│   ├── internal/gateway/repo/mysql.go（迁移前：internal/gateway/store/mysql.go）
│   ├── internal/gateway/repo/order_curve_repo.go
│   ├── internal/gateway/repo/process_snapshots.go
│   ├── internal/gateway/repo/port_repo.go（迁移前：internal/gateway/store/port_store.go）
│   ├── internal/gateway/repo/provision_repo.go（迁移前：internal/gateway/store/provision_store.go、provision/http.go[整批开通事务]、provision/vendors.go[完整厂商操作]）
│   ├── internal/gateway/repo/scan_repo.go（迁移前：internal/gateway/store/scan_store.go）
│   ├── internal/gateway/repo/session.go（迁移前：internal/gateway/store/session.go）
│   ├── internal/gateway/repo/session_repo.go（迁移前：internal/gateway/store/session_store.go、internal/gateway/control/session.go[完整静默查询与清理事务]）
│   ├── internal/gateway/repo/sync_models.go（迁移前：internal/gateway/store/sync_models.go）
│   ├── internal/gateway/repo/worker_sync_repo.go（迁移前：internal/gateway/control/worker_sync.go[同步查询、批量标记与端口读写]）
│   └── internal/gateway/repo/user_stop_repo.go（迁移前：internal/gateway/store/user_stop_store.go）
├── internal/worker                                       事件消费、定时与投递编排
│   ├── internal/worker/alerts/sync.go
│   ├── internal/worker/billing/dispatch.go
│   ├── internal/worker/charge/auto_stop.go
│   ├── internal/worker/charge/cards.go
│   ├── internal/worker/charge/end_sync.go
│   ├── internal/worker/charge/meter_segments.go
│   ├── internal/worker/charge/paid_start.go
│   ├── internal/worker/charge/sync.go
│   ├── internal/worker/internaljob/ops.go
│   ├── internal/worker/outbox/publisher.go
│   ├── internal/worker/refund/dispatch.go
│   ├── internal/worker/runtime/runtime.go（迁移前：cmd/worker/main.go[三个定时循环]）
│   ├── internal/worker/schedule/http.go
│   ├── internal/worker/schedule/scheduler.go
│   ├── internal/worker/repo/outbox_repo.go（迁移前：internal/worker/store/outbox_store.go）
│   └── internal/worker/repo/schedule_repo.go（迁移前：internal/worker/store/schedule_store.go）
├── internal/http                                         已迁移的鉴权、后台及central HTTP适配
│   ├── internal/http/admin/account/admin_accounts.go（迁移前：internal/interface/http/admin_accounts.go）
│   ├── internal/http/admin/account/admin_accounts_api.go（迁移前：internal/interface/http/admin_accounts_api.go）
│   ├── internal/http/admin/account/admin_roles.go（迁移前：internal/interface/http/admin_roles.go）
│   ├── internal/http/admin/admin_actor.go（迁移前：internal/interface/http/admin/admin_actor.go）
│   ├── internal/http/admin/dashboard/dashboard.go（迁移前：internal/interface/http/admin/dashboard/dashboard.go）
│   ├── internal/http/admin/device/api.go（迁移前：internal/interface/http/admin/device/api.go）
│   ├── internal/http/admin/device/device_imports.go（迁移前：internal/interface/http/admin/device/device_imports.go）
│   ├── internal/http/admin/device/devices.go（迁移前：internal/interface/http/admin/device/devices.go）
│   ├── internal/http/admin/device/meter_reviews.go（迁移前：internal/interface/http/admin/device/meter_reviews.go）
│   ├── internal/http/admin/errors.go（迁移前：internal/interface/http/admin/errors.go）
│   ├── internal/http/admin/export/exports.go（迁移前：internal/interface/http/admin/export/exports.go）
│   ├── internal/http/admin/finance/api.go（迁移前：internal/interface/http/admin/finance/api.go）
│   ├── internal/http/admin/finance/invoices.go（迁移前：internal/interface/http/admin/finance/invoices.go）
│   ├── internal/http/admin/finance/refunds.go（迁移前：internal/interface/http/admin/finance/refunds.go）
│   ├── internal/http/admin/finance/wallet_risks.go（迁移前：internal/interface/http/admin/finance/wallet_risks.go）
│   ├── internal/http/admin/marketing/api.go（迁移前：internal/interface/http/admin/marketing/api.go）
│   ├── internal/http/admin/marketing/coupon_activity.go（迁移前：internal/interface/http/admin/marketing/coupon_activity.go）
│   ├── internal/http/admin/marketing/coupons.go（迁移前：internal/interface/http/admin/marketing/coupons.go）
│   ├── internal/http/admin/normalize.go（迁移前：internal/interface/http/admin/normalize.go）
│   ├── internal/http/admin/ops/api.go（迁移前：internal/interface/http/admin/ops/api.go）
│   ├── internal/http/admin/ops/audit_log.go（迁移前：internal/interface/http/admin/ops/audit_log.go）
│   ├── internal/http/admin/ops/operations.go（迁移前：internal/interface/http/admin/ops/operations.go）
│   ├── internal/http/admin/order/api.go（迁移前：internal/interface/http/admin/order/api.go）
│   ├── internal/http/admin/order/casework.go（迁移前：internal/interface/http/admin/order/casework.go）
│   ├── internal/http/admin/order/order_process.go（迁移前：internal/interface/http/admin/order/order_process.go）
│   ├── internal/http/admin/order/orders.go（迁移前：internal/interface/http/admin/order/orders.go）
│   ├── internal/http/admin/order/payment_orders.go（迁移前：internal/interface/http/admin/order/payment_orders.go）
│   ├── internal/http/admin/pricing/api.go（迁移前：internal/interface/http/admin/pricing/api.go）
│   ├── internal/http/admin/pricing/charging_schemes.go（迁移前：internal/interface/http/admin/pricing/charging_schemes.go）
│   ├── internal/http/admin/request.go（迁移前：internal/interface/http/admin_resource.go）
│   ├── internal/http/admin/scope.go（迁移前：internal/interface/http/admin/scope.go）
│   ├── internal/http/admin/scope_reader.go（后台HTTP消费方的数据范围窄接口）
│   ├── internal/http/admin/station/api.go（迁移前：internal/interface/http/admin/station/api.go）
│   ├── internal/http/admin/station/stations.go（迁移前：internal/interface/http/admin/station/stations.go）
│   ├── internal/http/admin/user/api.go（迁移前：internal/interface/http/admin/user/api.go）
│   ├── internal/http/admin/user/charge_users.go（迁移前：internal/interface/http/admin/user/charge_users.go）
│   ├── internal/http/admin/user/online_cards.go（迁移前：internal/interface/http/admin/user/online_cards.go）
│   ├── internal/http/admin/vendor/api.go（迁移前：internal/interface/http/admin/vendor/api.go）
│   ├── internal/http/admin/vendor/vendors.go（迁移前：internal/interface/http/admin/vendor/vendors.go）
│   ├── internal/http/auth/admin.openapi.json（迁移前：internal/interface/http/admin.openapi.json）
│   ├── internal/http/auth/admin_auth.go（迁移前：internal/interface/http/admin_auth.go）
│   ├── internal/http/auth/admin_mfa_settings.go（迁移前：internal/interface/http/admin_mfa_settings.go）
│   ├── internal/http/auth/admin_openapi.go（迁移前：internal/interface/http/admin_openapi.go）
│   ├── internal/http/auth/auth.go（迁移前：internal/interface/http/auth.go）
│   ├── internal/http/auth/middleware.go（迁移前：internal/interface/http/middleware.go）
│   ├── internal/http/auth/service_token.go（迁移前：internal/http/admin/service_token.go）
│   ├── internal/http/central/billing_service.go（迁移前：internal/interface/http/billing_service.go）
│   ├── internal/http/central/card_api.go（迁移前：internal/interface/http/card_api.go[内部刷卡适配]）
│   ├── internal/http/central/card_balance.go（迁移前：internal/interface/http/card_balance.go）
│   ├── internal/http/central/charge_auto_stop.go（迁移前：internal/interface/http/charge_auto_stop.go）
│   ├── internal/http/central/charge_paid_starts.go（迁移前：internal/interface/http/charge_paid_starts.go）
│   ├── internal/http/central/charge_stop_authorization.go（迁移前：internal/interface/http/charge_stop_authorization.go）
│   ├── internal/http/central/device_alert_sync.go（迁移前：internal/http/admin/alertsync/http.go）
│   ├── internal/http/central/order_authorization.go（迁移前：internal/interface/http/order_authorization.go）
│   ├── internal/http/central/order_end_result.go（迁移前：internal/interface/http/order_end_result.go）
│   ├── internal/http/central/order_start_result.go（迁移前：internal/interface/http/order_start_result.go）
│   ├── internal/http/central/payment_simulation.go（迁移前：internal/interface/http/payment_payment_http.go[内部模拟通知]）
│   ├── internal/http/central/refund_internal_api.go（迁移前：internal/interface/http/refund_internal_api.go）
│   ├── internal/http/integration（跨接口族注册路由测试，无生产包）
│   ├── internal/http/user/card/api.go（迁移前：internal/interface/http/card_api.go）
│   ├── internal/http/user/charging/stop.go（迁移前：internal/interface/http/charge_user_stop.go）
│   ├── internal/http/user/device/api.go（所属业务族的服务注入与路由注册）
│   ├── internal/http/user/device/coordinates.go（迁移前：internal/interface/http/user_account_helpers.go）
│   ├── internal/http/user/device/fault.go（迁移前：internal/interface/http/user_fault.go）
│   ├── internal/http/user/device/station.go（迁移前：internal/interface/http/user_station.go）
│   ├── internal/http/user/errors.go（迁移前：internal/interface/http/user_account_helpers.go）
│   ├── internal/http/user/identity/phone.go（迁移前：internal/interface/http/phone_api.go）
│   ├── internal/http/user/identity/session.go（迁移前：internal/interface/http/user_api.go）
│   ├── internal/http/user/marketing/api.go（所属业务族的服务注入与路由注册）
│   ├── internal/http/user/marketing/available.go（迁移前：internal/interface/http/coupon_api.go）
│   ├── internal/http/user/marketing/owned.go（迁移前：internal/interface/http/user_coupon.go）
│   ├── internal/http/user/ops/announcement.go（迁移前：internal/interface/http/user_announcement.go）
│   ├── internal/http/user/ops/api.go（所属业务族的服务注入与路由注册）
│   ├── internal/http/user/order/api.go（所属业务族的服务注入与路由注册）
│   ├── internal/http/user/order/feedback.go（迁移前：internal/interface/http/charge_feedback.go）
│   ├── internal/http/user/order/query.go（迁移前：internal/interface/http/charge_user_query.go）
│   ├── internal/http/user/payment/development.go（迁移前：internal/interface/http/payment_development_payment.go）
│   ├── internal/http/user/payment/payment.go（迁移前：internal/interface/http/payment_payment_http.go）
│   ├── internal/http/user/payment/scan.go（迁移前：internal/interface/http/payment_scan.go）
│   ├── internal/http/user/settlement/api.go（所属业务族的服务注入与路由注册）
│   ├── internal/http/user/settlement/bill.go（迁移前：internal/interface/http/settlement_bill.go）
│   ├── internal/http/user/settlement/invoice.go（迁移前：internal/interface/http/user_invoice.go）
│   ├── internal/http/user/status.go（迁移前：internal/interface/http/charge_status.go）
│   ├── internal/http/user/user.openapi.json（迁移前：internal/interface/http/user.openapi.json）
│   ├── internal/http/user/wallet/api.go（所属业务族的服务注入与路由注册）
│   ├── internal/http/user/wallet/fields.go（迁移前：internal/interface/http/user_account_helpers.go）
│   └── internal/http/user/wallet/wallet.go（迁移前：internal/interface/http/user_wallet.go）
├── internal/protocol                                     设备契约层（与 domain 平行）
│   ├── internal/protocol/adapter.go
│   ├── internal/protocol/command.go
│   ├── internal/protocol/dc589/adapter.go
│   ├── internal/protocol/dc589/cards.go
│   ├── internal/protocol/dc589/commands.go
│   ├── internal/protocol/dc589/config.go
│   ├── internal/protocol/dc589/device.go
│   ├── internal/protocol/dc589/fault.go
│   ├── internal/protocol/dc589/frame.go
│   ├── internal/protocol/dc589/log_data.go
│   ├── internal/protocol/dc589/measurements.go
│   ├── internal/protocol/dc589/power_control.go
│   ├── internal/protocol/dc589/session_audit.go
│   ├── internal/protocol/endpoint.go
│   ├── internal/protocol/router.go
│   └── internal/protocol/session_audit.go
├── internal/testfixture                                  仅 integration 编译的真库测试夹具，不参与服务构建
│   ├── internal/testfixture/id.go                         随机大整数身份基址（替代测试中的时间主键）
│   ├── internal/testfixture/outbox.go                     按主键清理自身事件，保留原归属条件
│   └── internal/testfixture/outbox_integration_test.go     无关夹具锁隔离回归
├── internal/persist                                      与业务无关的持久化原语
│   ├── internal/persist/insert_id.go
│   └── internal/persist/page.go
├── internal/domain                                       纯业务规则（零框架依赖）
│   ├── internal/domain/charging/actual.go
│   ├── internal/domain/charging/breakdown.go
│   ├── internal/domain/charging/budget_stop.go
│   ├── internal/domain/charging/capabilities.go
│   ├── internal/domain/charging/cost.go
│   ├── internal/domain/charging/device.go
│   ├── internal/domain/charging/display.go
│   ├── internal/domain/charging/end_meter.go
│   ├── internal/domain/charging/estimate.go
│   ├── internal/domain/charging/live_meter_view.go
│   ├── internal/domain/charging/measured_segments.go
│   ├── internal/domain/charging/metering.go
│   ├── internal/domain/charging/mode.go
│   ├── internal/domain/charging/offer.go
│   ├── internal/domain/charging/offer_actual.go
│   ├── internal/domain/charging/order_package.go
│   ├── internal/domain/charging/rule.go
│   ├── internal/domain/charging/scheme.go
│   ├── internal/domain/charging/scheme_cost.go
│   ├── internal/domain/charging/segments.go
│   ├── internal/domain/charging/spec.go
│   ├── internal/domain/devices/reading.go
│   ├── internal/domain/money/refund.go（新增：无 ORM 的事务内退款输入）
│   ├── internal/domain/money/card_number.go
│   ├── internal/domain/money/card_port.go
│   ├── internal/domain/money/review.go
│   ├── internal/domain/money/scan.go
│   ├── internal/domain/operations/errors.go（迁移前：internal/interface/http/admin_resource.go[业务冲突]）
│   ├── internal/domain/operations/account.go
│   ├── internal/domain/operations/export.go
│   ├── internal/domain/operations/scope.go
│   └── internal/domain/operations/validate.go
├── internal/platform                                     无业务语义的通用能力
│   ├── internal/platform/auth/jwt.go
│   ├── internal/platform/auth/totp.go
│   ├── internal/platform/config/config.go
│   ├── internal/platform/dbconn/mysql.go
│   ├── internal/platform/dbutil/dbutil.go
│   ├── internal/platform/httpapi/decode.go（迁移前：internal/interface/http/admin_decode.go[严格JSON输入]）
│   ├── internal/platform/httpapi/codes.go
│   ├── internal/platform/httpapi/decimal_id.go
│   ├── internal/platform/httpapi/gin.go
│   ├── internal/platform/httpapi/metrics.go
│   ├── internal/platform/httpapi/server.go（迁移前：cmd/*/main.go[HTTP 运行与停机]）
│   ├── internal/platform/netguard/guard.go
│   ├── internal/platform/phone/phone.go
│   ├── internal/platform/serviceclient/client.go
│   ├── internal/platform/serviceclient/token.go
│   ├── internal/platform/sessiontoken/token.go（迁移前：internal/infrastructure/redis/session_token.go[通用编码与注销]，共用原子轮换）
│   ├── internal/platform/sessiontoken/uint64.go（JSON 无符号整数精确编码）
│   └── internal/platform/snowflake/snowflake.go
│
├── migrations/central_db/0001_init.sql
├── migrations/gateway_db/0001_init.sql
├── migrations/worker_db/0001_init.sql
├── Dockerfile                                            服务镜像：上下文为 backend/，容器内编译 ./cmd/<SERVICE>
└── go.mod  go.sum  .dockerignore

仓库根（backend/ 之外，路径相对仓库根）：
├── frontend/                                            前端项目
│   ├── admin-web/                                       运营后台
│   └── miniprogram/                                     用户端 Taro（H5 与微信小程序）
├── docker/                                               Compose 编排（compose.dev／prod.yaml）、Caddyfile 与 MySQL 初始化 SQL
├── scripts/db/  scripts/dev/  scripts/git/  scripts/ops/  scripts/rules/ 跨平台 Node 脚本
├── docs/                                                 DC589 协议文档在 docs/protocols/
│   ├── rules/                                            规则层人读层，单一入口 docs/rules/README.md
│   │   ├── architecture.md  business-invariants.md      架构边界与依赖、业务不变量
│   │   ├── verification.md                               验证入口，仅指针不重述规则
│   │   └── plans/  plans/archive/                       进度与计划；归档只放历史快照
│   └── adr/                                              架构决策记录，编号连续、每份带状态
├── .agents/  .githooks/
└── AGENTS.md  README.md  GLOSSARY.md  LICENSE  tasks.mjs  .env.example
```

- **代码按 use case 组织**，主轴是业务用例不是技术层。每个 use case 目录自己
  回答"这个用例怎么读写自己的数据"：

  ```
  internal/
    charging/    order/       settlement/   wallet/      pricing/    card/
    device/      identity/     marketing/   payment/    ops/
    export/                        —— 12 个 use case
    domain/                       纯规则与跨用例契约（零框架依赖）
    platform/  persist/  protocol/  contracts/  integration/  delivery/  http/
    gateway/  worker/               —— 进程特有编排
  ```

  依赖方向：

  ```
  domain       →  无（不碰 gorm/gin/redis，不依赖任何实现层）
  protocol     →  无（契约自足）
  persist      →  无（分页、自增主键等原语）
  platform     →  无（无业务语义的通用能力）
  use case     →  domain + contracts + platform + persist + 其它 use case（在结构文档说明业务理由并经评审）
  use case     →  所属repo（表名与列映射仅在 *_repo.go / *_schema.go / *_model*.go）
  ```

  以下三条由实现者和评审者逐项核对：

  1. **domain 零框架**：不出现 gorm / gin / redis，不出现列映射与表名映射，
     不 import 任何实现层。
  2. **表访问集中**：SQL、列映射和数据库写入位于所属用例的 `*_repo.go` /
     `*_schema.go` / `*_model*.go`。函数签名收 `*gorm.DB` 传递事务是正常用法。
     同一回执的状态、幂等凭据、资金和 Outbox 保持完整事务边界。
  3. **use case 之间单向**：新增跨用例引用须在 `docs/architecture-boundaries.md`
     说明业务理由、数据归属与事务边界；评审核对是否反向依赖或形成循环。

  `worker` 与 `gateway` 是进程特有编排，可 import use case；但 `worker`
  不得直接访问 `central` 拥有的业务表，取数据要经 central 的内部端点。
  `domain` 与 `protocol` 是平行层，互不 import。

- `internal/domain/` 装**纯规则与跨用例契约**，零 gorm、零 gin、零 Redis。
  它的判据是"有没有 IO"，不是"属不属于某个用例"——`DataScope` 被 45 处
  引用、`ChargeMode` 29 处，它们是共享契约，搬进任一 use case 都会让
  其他用例反过来引它。当前子包见上方目录树：charging / money / devices /
  operations。

- `internal/charging/` 包含 `start`、`stop`、`finish`、`measurement` 四个生命周期子包及 `query` 查询组合包，根目录不放业务代码或汇总转发服务。`start` 管启动授权、待启动清单与启动回执；`stop` 管停止授权、自动停机和计费截止；`finish` 管结束回执及提交结算任务；`measurement` 管无 IO 的协议事件转换；`query` 组合订单状态、计量证据及订单采样。
- `start`、`stop`、`finish` 不相互依赖；需要共享的计量证据转换放 `measurement`，纯业务规则放 `domain`。`measurement` 只引用领域契约与 `protocol` 根契约，不引用数据库、HTTP、设备具体协议或其他用例。子包的数据库代码、业务编排和测试放在一起，不统一再拆 `service/repository/model` 技术目录。
- 跨聚合事务在对应生命周期子包内编排。付款渠道属于 payment，余额入账属于 wallet，退款事务、计费任务及其状态查询属于 settlement，刷卡操作状态属于 card。新增抽象以实际复用或隔离需求为依据；迁移不切碎既有事务、锁顺序和幂等判定。
- 停止用例的 `Gateway` 接口只包含取证与停止受理。分页策略在用例中，单次 HTTP 调用、状态码、业务 code 与响应对应关系由专用客户端核对；不完整证据不得用于自动停机判定。wire DTO 显式标记 JSON 字段并保留既有大小写、整数类型与单位，处理器与客户端共用；字段变更需同步核对双方与契约测试。

- 后台业务适配位于 `internal/http/admin`，central内部端点位于 `internal/http/central`，共享鉴权适配位于 `internal/http/auth`；用户端点按所属业务族位于 `internal/http/user`，由cmd逐族装配，不再使用全量UserAccountAPI容器。user父包只提供无数据库依赖的错误映射和状态筛选；跨族注册回归在只有测试文件的 `internal/http/integration`。内部刷卡与用户卡是分别装配的类型，模拟内部支付通知只按central原模式注册，对外支付回调仍由user提供。gateway／worker 的进程特有端点保持运行职责。业务HTTP结构体只持有所需服务、鉴权器与协议配置，不持有数据库、仓储、服务地址或通用出站客户端。后台范围通过消费方 `ScopeReader` 读取，由cmd注入 `ops.ScopeQueryService`；不改变无范围记录表示不受限的既有规则。
- HTTP处理器只做认证、输入绑定、用例调用和响应映射，不实现业务SQL或事务。查询和账单已读操作也由所属服务提供；`settlement.UserBillService` 保持原归属查询后幂等保存顺序，查询阶段404与保存阶段503仍由HTTP映射。`charging` 不依赖 gin、HTTP 层或具体设备协议；worker 的充电编排只经 central 内部接口取业务数据。评审核对写入归属：card_charge/card_operation 属于 card，charge_billing_cutoff 属于 charging/stop，charge_billing_job 属于 settlement。

- `internal/protocol/` 是 gateway 与 central 共享的设备契约层，与 `domain` 平级，既不属于 `domain` 也不属于 `infrastructure`：
  - 根包只放跨进程契约：`Adapter`（`Name` + `ServeConn`）、`Sink`、`Event`、`Command`、`Registry`、`Listener`。
  - 每个协议一个子目录实现 `Adapter`，当前为 `dc589/`。帧格式、校验、指令编解码、故障码语义（`0xBB` 烟雾）、单位换算（分 → 协议余额单位）全部封闭在该目录内。
  - **协议码值、帧结构、校验算法一律不得出现在 `domain/` 与 `charging/`。** 业务代码出现 `0xBB`、`dc589.` 前缀或帧相关字样即违反本约定。
  - **新增协议必须新开子目录实现 `Adapter`，不得修改 `protocol` 根包。** 若做不到，说明抽象有缺陷，应改抽象而不是把新协议逻辑塞进根包。
  - 设备领域模型对所有协议保持一致；加协议不产生新的设备类型，只产生新的语义翻译。
  - `ServeConn(context.Context, net.Conn, Sink)` 假设"一条长连接 = 一个设备会话"。若将来某协议走 HTTP 轮询，要改的是 `Adapter` 接口本身，不是各协议实现——那是接口演进，不要为想象中的协议提前过度抽象。
- 前端使用 Node.js 24、npm 和已提交的 `package-lock.json`。以 `npm ci` 安装，保留 `frontend/miniprogram/.npmrc` 的依赖配置。
- 重构范围内新增、修改和迁移的代码必须补齐详细中文注释，语言严肃、简练、准确，不使用口语、比喻、主观强调或实施过程描述。规则见 [中文注释规范](docs/architecture-boundaries.md#中文注释规范)。类型及接口说明职责；每个结构体字段说明含义，适用时注明来源、单位、范围、零值／空值与快照语义；未导出字段及局部持久化投影同样适用。
- 每个函数、方法及接口方法说明行为与前置条件，逐项解释全部入参、出参、错误、特殊返回值及副作用；涉及 context、事务或资源时说明取消影响、提交／回滚责任及生命周期。核心逻辑在对应代码段详细说明业务依据、事务边界、锁顺序、幂等、资金／状态约束、外部调用顺序、未知结果与恢复；不逐行复述语法，不把待实现目标写成现有保证。修改代码时同步修正注释，字段含义与 SQL COMMENT、DTO 和 OpenAPI 一致；保留 `go:build`、`go:embed`、`go:generate` 等指令及 `Deprecated:` 标记。
- 不提交构建产物、依赖目录、真实配置、凭证、数据库备份或临时脚本；临时文件置于 `.tmp/`。

## 数据与计算

- 金额以整数分传输和保存；费率及中间精度沿用 `internal/domain/charging/scheme.go` 的十进制计算与舍入规则。不得使用浮点金额或另建前端计费算法。
- 电量、功率和时长必须注明单位。设备 `0.1 W`、`mWh`、`Wh`、分钟和秒按协议字段转换；`1 Wh = 1000 mWh`，`1 kWh = 1000 Wh`。
- 用户 Snowflake ID 在数据库中保留 `BIGINT UNSIGNED`，公开 API、Redis 会话和前端采用十进制字符串。使用 `httpapi.DecimalID`；前端不得将大 ID 转为 JavaScript `Number`。
- 编号分配与业务记录共用事务；重试复用已持久化编号。保留历史业务编号，不重新生成或批量改写。
- 数据库连接按 UTC 解析时间；业务日界与 DC589 本地时间使用北京时间。转换必须显式指定时区，不依赖宿主机时区。
- 订单计费使用创建支付意图时冻结的完整方案、套餐及实际计量；刷卡在首次扣款事务中冻结。不得读取当前模板重算历史订单。跨费率缺少边界实测读数时进入计量复核，不按时间均摊电量或插值补造证据。
- 缺失采样、金额、关联信息以 `null` 或明确状态表达，不用零值伪装为已测量、已结算、已到账或端口空闲。
- 手机号存入 `central_db.user.phone` 明文列，未绑定为 `NULL`，由唯一索引约束号码归属。复用 `internal/platform/phone` 的规范化、校验和展示脱敏；正式绑定须使用微信验证结果。

## 状态、幂等与审计

- 对余额、支付、退款、订单、端口归属和配额的并发更新使用既有事务与行锁顺序。同库的状态变更、幂等凭据、审计及 Outbox 应与业务写入同事务提交。
- 同一请求标识绑定同一业务内容；一致重放返回原结果，内容或身份冲突返回冲突。网络重试沿用原 `request_id`、命令 ID、支付单号和退款单号。
- 支付客户端成功、启动请求受理、STOP 回执、结束计量、费用核算和退款到账是不同事实；按持久化回执推进各自状态，不提前确认后续环节。
- 微信通知先验签、解密并核对商户、应用、订单身份、金额及渠道交易号，再入账。未知渠道结果保留查询与预留资金，不换号重提。
- 人工退款、计量分段修订和发票通过保留不同人员复核约束，第二签核对前一审核人的实时权限及原快照。人工最终金额结算沿用现有授权、实付上限、原因及审计约束；分段修订保持原总量与冻结来源，不能补造截止计量。
- 审计记录操作人、目标、请求标识及必要的前后快照；不记录密码、JWT、服务密钥、TOTP 密钥或验证码。设备日志保留关联编号和解析信息，不泄露卡号、余额、SIM 标识、面板密码或原始敏感报文。

## 权限与接口

- 后台授权实时读取有效账号、角色和权限；JWT 及前端菜单只表示会话或展示，不代替服务端授权。
- 站点、设备、厂商及配置操作沿用服务端数据范围检查。列表与详情、导出、写入和批量操作使用一致的授权条件；客户端筛选不能扩大范围。
- 用户资源以当前会话校验归属；无权访问与不存在统一按既有接口返回。服务间接口使用 `X-Service-Token`，前端不得持有服务令牌。
- 后台导出仅支持 CSV、XLSX，格式留空时默认 CSV；账单导出仍须校验日期范围。
- 修改 API 时同步检查调用方、DTO、输入约束、错误状态和对应的 OpenAPI 文档：后台是 `internal/http/auth/admin.openapi.json`，通过 `go:embed` 提供 `GET /api/docs/admin.openapi.json`；C 端是 `internal/http/user/user.openapi.json`，当前为静态文件，不新增文档路由。两份文档直接维护，当前没有自动生成流程；修改时逐项核对声明端点的实际注册、schema 引用及真实请求／响应，不把路由清单检查当作字段、鉴权或行为验证。生产 Caddy 不代理后台文档端点。
- 维持乐观版本检查和幂等标识。前端刷新令牌不能改变业务请求身份；账号切换后的旧请求不得覆盖新会话状态。

## 设备协议

- 当前新建设备使用 DC589/TCP。新增协议在独立适配器中实现，并补齐协议帧、能力声明和模拟器验证，不在现有解析器中猜测兼容。
- 帧长度、校验、字节序、字段范围和可选负载均须严格验证。十进制 BCD 字段校验每个半字节；模块/SIM 标识保留既有非十进制半字节兼容，不按十进制统一拒绝。启动和停止回执匹配设备、端口及六字节命令会话，连接会话与命令会话不得混用。
- 设备事件先持久化，再确认上行；指令及端口占用先提交，再下发。结束结果经 central 确认后释放 gateway 端口，迟到回执不能释放新订单的占用。
- 停机条件需要可靠、连续的端口证据；离线或缺失功率不等于零功率。保持断线重连、持久重试及补偿停止的既有语义。

## 数据库变更

- 当前项目尚未发布，三个 schema 仅保留各自的 `0001_init.sql`；结构调整直接修改初始化定义，不累加发布前迁移，也不使用 GORM 自动建表。
- 每张表、字段均填写有效 MySQL `COMMENT`，注明金额和计量单位、枚举值、快照及幂等用途。维护月分区、联合键和唯一索引语义。
- 现有数据默认保留。结构不兼容时先备份并设计数据迁移；仅对明确可丢弃的开发数据使用 `scripts/db/reset-dev.mjs --reset-development-data`。不得仅删除 Goose 版本表或无条件清空库、卷和 Redis。
- MySQL DDL 不具备事务回滚；初始化中断应针对空 schema 处理，不将失败的结构更新标为成功。

## 开发环境

- 开发环境由 `scripts/dev/start.mjs` 启动的 `docker/compose.dev.yaml` 容器统一提供，包含后端、后台、用户端、MySQL 和 Redis。启动、数据准备、模拟器与联调顺序见 [本地运行](README.md#本地运行) 与 [模拟器](README.md#模拟器)，本节只记录使用规则。
- 可自主拉起、重启与停止开发容器，包括未运行时的 `start.mjs` 与 `docker compose -f docker/compose.dev.yaml`。不为启动而启动：只做当次任务需要的动作，用完不留下多余容器，`start.mjs` 保留已有数据。**破坏性动作仍需确认**——重建数据卷、`down -v`、`reset-dev.mjs`、清空开发库前一律先问。
- 验证与联调优先用既有路径：`scripts/dev/start.mjs`（开发栈）、`/health/ready` 判定就绪。**不要为"多验证一点"另造旁路脚本**——自建的临时容器、临时二进制、临时灌库脚本既要自己收拾，又复现不了仓库既有的正确做法。需要新验证手段时先用仓库里已有的，或提出方案经确认后再写。
- 不得自行在宿主机启动常驻本地服务器或 dev server，也不得为 `dist` 另建静态服务；这两类一律用容器内的地址。确需一次性临时服务时避开容器占用端口，用完立即停止并说明用途。
- 验证前端渲染访问容器内的后台与用户端 H5 地址，并同时确认页面控制台无报错。
- 本地地址固定：后台 `5173/admin/`、用户 H5 `5174/`、MySQL `3306`、Redis Cache `6379` 与 Stream `6380`、DC589 `9100`；user、admin、gateway、worker HTTP 分别可从宿主机 `127.0.0.1:8080/8081/8083/8085` 访问，central `8082` 只在容器网络可达。五个进程均提供 `/health/live`、`/health/ready` 和 `/metrics`；检查 central 就绪须从开发栈网络内请求。
- Go 服务没有热重载。修改后按 README 重建受影响的服务，优先只重建该服务而非整个栈；确需重启整栈时先说明理由。
- 设备联调的两个模拟器须在独立终端分别执行 `scripts/dev/simulator.mjs --number 1` 与 `--number 2`，属前台交互进程，不得作为后台服务重复拉起。模拟器与模拟支付结果不得当作实机或真实资金验收。

## 并发与幂等

这一层的判断有一半是读代码看不出来的，必须对真库验证。

### 集成测试怎么组织

- 真库集成测试文件一律带 `//go:build integration`，评审核对 build tag 与 CI 执行命令。
  默认单测不访问真实数据库；集成测试必须在提供测试库后明确执行。
- **不提供跳过路径**：`TEST_MYSQL_URL` 缺失直接 `t.Fatal`，`node tasks.mjs test-integration`
  不给变量时退出码 2。跳过的测试在 `go test` 输出里和通过的一模一样，
  CI 没配库就会一路绿灯——护栏最常见的失效正是靠放行通过。
- 测试写入用事务回滚；确实需要提交的场景（如两个独立事务要看到同一行）
  在独立测试库执行，由收尾按完整主键显式清理并核对 0 残留。2026-10-06 起独立测试实例编排移除，集成测试直连开发库；显式主键会抬高开发库自增序号，必要时以 reset-dev 重建开发数据。
- 共享骨架（独立连接、并发闸门、按表名挂载的加锁回调）提取到各包的
  `gorm_integration_test.go`，不要每个文件复制一份。

### 并发测试怎么写才算数

**不许靠起 N 个 goroutine 碰运气。** 那种测试要么抓不到竞态，
要么换台机器就偶发失败，两头都不合格。做法是：用 gorm `After("gorm:query")`
回调在 SQL 命中特定表时把事务一挂起，主控用 channel 控制放行，
再从**另一条独立连接**起事务二，断言它在放行前必须返回不了。每一步的先后都是确定的。

两个事务必须用两条独立连接——复用同一条会让「并发」退化成串行，测出来的是假象。

**按 `FROM` 子句匹配表名，不要按裸子串。** `card_charge` 那条查询的 WHERE 列名是
`charge_order_id`，含子串 `charge_order`；而且该表无行时 `FOR UPDATE` 只取间隙锁，
挡不住第二个事务，测试会误判成「没有串行化」。

### 两条判据

1. **「锁一行 → 聚合读一个集合 → 按聚合值做准入决策」里的计数必须自己是当前读。**
   MySQL REPEATABLE READ 下普通 `SELECT` 走事务快照，而快照在事务第一次读时已建立。
   行锁让事务串行化了，聚合读仍按自己的快照算，串行化帮不上忙——
   被挡住等锁的那个事务照样读到旧值。配额、额度、余额、限流都适用。
   （本仓库出现过三次：活动发券预算计数、人工退款累计占用、支付到账。）
   反过来，只用于决定「要不要翻个状态」而不涉及金额的计数，**不要**加锁——
   那只会白白串行化。

   **还有第二种正确解法：让「已占用额度」本身就是一个在锁内累加的列。**
   `wallet_account.frozen_cents` 就是这样——每笔预占都 `frozen+金额`，
   校验时 `可用 = balance - frozen` 是在钱包行锁内读到的当前值。
   该模式不依赖聚合读，但所有预占、释放及扣减路径仍须在钱包行锁内读取和更新冻结额度。
   `wallet` 用的就是这种，`settlement` 至今仍是前一种。

2. **靠 `sql.LevelReadCommitted` 撑起来的幂等，要写测试钉住隔离级别。**
   重复投递的判定如果靠普通读读「这单处理过没有」，在 RR 下会读到快照旧值、
   于是判成冲突而不是重放，调用方会永远等不到成功响应。
   已知承重处：`internal/charging/finish/result_repo.go`。

**改完注入变异证伪**（护栏靠放行通过是最常见的失效），至少覆盖：
去掉行锁、去掉唯一键、去掉加锁读、去掉隔离级别声明、去掉幂等凭据比对。

## 验证与交付

- Git 操作一律需要用户明确授权。未获授权不得执行 `commit`、`reset`、`revert`、`push`、`rebase`、`merge`、`cherry-pick`、`tag`，不得新建或切换分支，不得改写提交历史。改动只落在工作区，由用户审阅后自行提交。`git status`、`git diff`、`git log`、`git show` 等只读命令可以直接使用。
- 交付时报告工作区的未提交改动，不代替用户提交。
- Go 业务变更运行 `node tasks.mjs check`；涉及模块依赖时运行 `go mod tidy -diff`，涉及进程装配或构建时用 `node tasks.mjs build <name>` 或 `node tasks.mjs build-all` 构建对应 `cmd`，可执行文件一律落在已忽略的 `bin/`，不在包目录下留产物。
- `node tasks.mjs check` 先跑规则层检查 `node tasks.mjs check-rules`（只读文档与迁移 schema，秒级），再跑格式化、go vet 与默认测试；架构规范由代码评审核对，规则层检查只判定文档层不变量，不判依赖方向与表归属。资金、并发、唯一性与幂等路径另跑 `node tasks.mjs test-integration`（预先设置 `TEST_MYSQL_URL`）；本地入口是 `node scripts/db/test-integration.mjs`，2026-10-06 起直接使用开发环境（3306/6379），不再有 compose.test.yaml 独立实例。显式测试主键即使回滚仍会抬高开发库自增序号，必要时以 reset-dev 重建；有构建 tag 的真库测试不能用默认测试通过替代。
- 真实会话测试还须配置 `TEST_REDIS_URL`（开发 Redis 6379）。确定性角色锁测试需要 `performance_schema.data_lock_waits`、`data_locks`、`threads` 的 SELECT 权限；测试入口加载 `docker/mysql/test-lock-observer.sql` 补幂等授权，生产初始化不加载这份测试授权。缺少连接或观测权限须明确失败，不跳过或改用模拟锁。
- 后台逻辑修改运行 `npm test` 与 `npm run build`；用户端逻辑修改运行 `npm run typecheck`、`npm test`，涉及平台适配或构建配置时构建受影响的 H5／微信目标。命令在对应前端目录执行。
- 纯文档、注释和文案修改检查链接、引用、事实与格式；不为低影响可逆修改增加重复测试。已有验证通过后，仅在新增修改、失败或未决问题需要时扩大检查。
- 测试覆盖实际契约和故障边界，重点验证并发重放、非法状态、授权范围、金额与计量不一致、外部未知结果和回执丢失；避免照抄实现的测试。
- 功能、命令、目录或部署契约改变时同步维护 README、本文件及相应的业务／运行文档；设备协议完整规范维护于 `docs/protocols/dc589.md`，精确业务字段留在源码、SQL 注释和 OpenAPI，不新增重复文档树。保留维护文档引用的 README 锚点，移动内容时更新入口及相对路径。
- 完成交付前检查差异与失效引用，报告实际修改、验证结果及仍存在的实现边界；不得把模拟器或模拟支付验证称为实机或真实资金验收。
