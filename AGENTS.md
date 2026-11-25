# WattHarbor 仓库开发约定

## Git 提交规范

- 提交消息遵循 Conventional Commits。标题格式为 `type(scope): 中文摘要`，省略作用域时用 `type: 中文摘要`。类型和作用域用英文，摘要和可选正文用中文。
- 示例为 `docs: 精简仓库开发约定` 和 `fix(db): 修复订单状态更新`。
- 只读 Git 命令可直接执行。提交、建分支、切换分支、合并、变基、重置、回滚、拣选、标记和推送均需用户明确授权。未授权时交付工作区改动。
- 提交前按改动范围执行下文的验证。克隆后按需启用 [Git 钩子](README.md#开发与验证)。禁止提交构建产物、依赖目录、真实配置、凭证、数据库备份或临时脚本。仓库内临时文件放在 `.tmp/`。

## 规则与资料入口

- 回复、注释和文档使用中文。
- 实现以源码、路由注册、初始化 SQL、Compose 和测试为准。调整模块边界先读 [架构边界](docs/architecture-boundaries.md)，调整业务行为先读 [业务契约](docs/business-contracts.md)。启动见 [README](README.md#本地运行)，部署、配置和数据维护见 [运行手册](docs/operations.md)。
- [冻结清单](scripts/rules/check-rules.mjs) 中的旧文档只读。新增规则按本文件的主题归类；可验证的约束写成断言，本文件只留执行指引。依赖方向、数据归属等无法自动判定的约束由评审核对。
- 行为或运行契约变化时更新对应文档和受影响的链接。用户端路线图任务完成后更新 [路线图](docs/miniprogram-roadmap.md) 的状态与证据。

## 架构与编码

- 单体是唯一的业务部署单元，开发与生产共用入口。`cmd` 负责装配和生命周期，业务按用例归属组织。初始化失败时释放已打开的资源，退出时先取消并等待请求、会话和任务，再关闭连接池。
- `domain` 保持纯规则与契约，禁止 I/O、框架依赖和表映射。处理器负责鉴权、绑定、调用用例及响应映射。表访问和业务写入由所属用例负责，SQL 和列映射集中在所属仓储或持久化模型。新增跨用例依赖需记录理由、数据归属和事务边界，由评审核对依赖方向及循环；架构和归属由评审核对。
- 充电的启动、停止和结束用例保持独立，共用计量转换和纯领域规则。用例定义所需的窄接口，由入口注入实现。服务间调用使用专用客户端，保留内部令牌校验。业务用例和前端不得持有服务令牌，后台任务经所属用例及既有内部接口执行业务操作。
- 跨模块请求 DTO 用 `Request` 后缀，响应 DTO 用 `Reply` 后缀。变更接口时同步核对注册、调用方、鉴权、错误映射、JSON 字段、类型、单位和空值语义，并更新对应 OpenAPI 与契约测试。新增环境变量须核对加载、消费、Compose 注入及模板。
- 新增和修改的代码遵循 [中文注释规范](docs/architecture-boundaries.md#中文注释规范)。说明字段来源、单位、范围与空值语义，以及函数契约、错误和副作用。涉及事务、锁、幂等、取消与资源生命周期时，在代码附近说明约束和依据。保持注释与 SQL `COMMENT`、DTO 和 OpenAPI 一致。
- Go 工具链以 `backend/go.mod` 为准。前端用 `npm ci` 与已提交的锁文件，保留小程序的 `.npmrc`。新增 Taro 页面只登记 `frontend/miniprogram/src/app.config.ts`，修改后重启 `user-h5` 以重建路由表。

## 数据与业务安全

- 发布前直接修改对应 schema 的 `0001_init.sql`，禁止追加发布前迁移和使用 GORM 自动建表。所有表和字段保留有效的 MySQL `COMMENT`，并核对分区、联合键和唯一索引。现有数据默认保留，涉及不兼容变更先备份并设计迁移。MySQL DDL 失败后核对实际结构，再继续初始化或恢复。
- 枚举取值以初始化 SQL 和 Go 常量为准，保留 MySQL `ENUM` 声明顺序的语义。GORM 的 `IN` 列表用绑定的切片展开，避免把逗号分隔的白名单作为单个值传入。见 [真库回归记录](docs/status-enum-roadmap.md)。
- 金额用整数分，费率沿用十进制规则。计量注明单位，缺失证据显式表示。数据库时间按 UTC 解析，业务日界和 DC589 本地时间用北京时间。公开接口、Redis 会话和前端的大整数用户 ID 使用十进制字符串。
- 计费使用冻结资费与实际计量，缺少跨费率边界实测值时进入复核。资金、状态、幂等凭据、审计与 Outbox 保持既有事务边界和锁顺序。重试复用业务标识与已分配编号，未知外部结果保留查询和补偿路径。微信支付通知须先验签、解密并核对商户、应用、订单、渠道交易号及金额，再入账。
- 服务端实时核验权限、资源归属和数据范围，列表、详情、导出与写入采用一致条件。保留人工审核的人员分离和审计要求。日志禁止记录凭据、验证码和设备敏感数据。
- 新设备协议通过独立 `protocol.Adapter` 实现，帧格式、码值和单位换算留在协议适配器。修改 DC589 先读 [协议规范](docs/protocols/dc589.md)，核对 BCD 兼容、设备及端口与命令会话匹配、持久化与确认顺序，以及迟到回执对端口占用的影响。离线或缺失功率不得视为零功率。

## 开发环境与验证

- 联调使用 [现有 Compose 开发栈](README.md#本地运行)，以 `/health/ready` 判定就绪。按任务启停容器，保留数据和依赖卷。前端渲染使用容器入口并查看浏览器控制台。一次性临时服务须避开已有端口，用后停止；禁止另起常驻宿主机服务或重复拉起交互式模拟器。
- 重建数据卷、`down -v`、重置开发库或清空 Redis 等破坏性动作需用户明确授权。模拟设备和模拟支付仅覆盖模拟链路。
- 后端业务改动跑 `node tasks.mjs check`，无需连接数据库或启动容器。依赖变化在 `backend/` 执行 `go mod tidy -diff`，装配或构建变化构建受影响入口，公开 JSON 变化加跑 `node tasks.mjs test-contract`。前端按各自 README 执行检查和目标构建；构建与 Git 钩子见 [开发与验证](README.md#开发与验证)。
- GORM 投影、枚举参数、状态写入、资金、行锁、唯一键及幂等改动，额外在 `backend/` 运行 `go vet -tags integration ./...`，并从仓库根目录运行 `node scripts/db/test-integration.mjs`。
- 真库测试使用 `integration` 构建标签。缺少 `TEST_MYSQL_URL`、会话测试所需的 `TEST_REDIS_URL` 或锁观测权限时明确失败。现有脚本使用开发库，写入优先回滚；确需提交的夹具按完整主键清理并检查残留。
- 并发测试使用独立连接和确定性闸门，按 `FROM` 精确匹配目标表，用 `performance_schema` 确认等待。对承重的行锁、唯一键、隔离级别及幂等判断，移除相应保护后核实相关测试会失败。
- MySQL `REPEATABLE READ` 下，持有行锁后进行的普通聚合仍可能使用旧快照。额度判断须使用当前读，或在锁内维护已占用额度。依赖 `READ COMMITTED` 的重放路径须有测试固定隔离级别。
- 纯文档、注释和文案改动核对事实、引用、链接和差异。修改规则文档时运行 `node tasks.mjs check-rules`；报告中标为「尚未实现」的断言不算通过。修改检查器时加跑 `node --test scripts/rules/check-rules.test.mjs`，Node 脚本测试不在默认门禁内。交付时报告实际执行的验证、剩余限制和未提交改动。

## 技能与协作

- Go 编码、评审、调试、重构和配置任务先读 [golang-how-to](.agents/skills/golang-how-to/SKILL.md)，再从 [已安装的技能](.agents/skills/) 按任务读取对应 `SKILL.md`。优先加载用户指定的技能，调用示例工具前先核对可用性。技能示例遵循仓库约束及用户授权。
- 常用入口包括 [数据库](.agents/skills/golang-database/SKILL.md)、[测试](.agents/skills/golang-testing/SKILL.md)、[并发](.agents/skills/golang-concurrency/SKILL.md)、[取消与超时](.agents/skills/golang-context/SKILL.md)、[安全](.agents/skills/golang-security/SKILL.md)、[重构](.agents/skills/golang-refactoring/SKILL.md)、[故障排查](.agents/skills/golang-troubleshooting/SKILL.md) 和 [文档](.agents/skills/golang-documentation/SKILL.md)。
- 用户要求委派时，先读对应的角色定义。[需求分析](.agents/agents/manager.md)、[代码评审](.agents/agents/reviewer.md) 或 [测试](.agents/agents/tester.md) 均有独立定义。明确任务、验收、文件范围和环境，避免修改同一文件或互相干扰的测试；主代理核对结果与证据。

### Issue tracker

议题和规格使用 `docs/feats/` 内的 Markdown 文件，计划写明目标、依赖与验收标准。验收后在对应议题记录结果，将历史证据与待办、现行规则区分。见 [议题跟踪约定](docs/agents/issue-tracker.md)。

### Triage labels

使用 `needs-triage`、`needs-info`、`ready-for-agent`、`ready-for-human`、`wontfix`。见 [分诊标签约定](docs/agents/triage-labels.md)。

### Domain docs

项目采用单上下文。术语写入 [GLOSSARY.md](GLOSSARY.md)，架构取舍记入 [ADR](docs/adr/README.md)。见 [领域文档约定](docs/agents/domain.md)。
