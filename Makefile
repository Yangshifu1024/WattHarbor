.PHONY: fmt lint test test-integration build build-all check clean dev-up dev-down
.NOTPARALLEL: check

# 后端 Go 模块位于 backend/：检查与构建一律进入该目录执行，
# 编译产物统一落到仓库根 bin/（已在 .gitignore）。
SERVICES := user admin central gateway worker migrate devseed simulator-dc589
export TEST_MYSQL_URL SERVICE

fmt:
	cd backend && go fmt ./...

lint:
	cd backend && go vet ./...

test:
	cd backend && go test ./...

# 真库集成测试。SQL 里的判定（活动窗口、门槛、软删除）纯函数覆盖不到，
# 只能对着真库验；这些文件由 integration tag 隔离，make check 不依赖 Docker。
#
# 起库与迁移：
#   node scripts/db/test-integration.mjs
# 脚本直接使用开发环境（docker/compose.dev.yaml 的 3306 MySQL 与 6379 Redis）。
# 跑测试：
#   make test-integration
#
# 刻意没有跳过路径：没配 TEST_MYSQL_URL 就直接失败而不是 skip——
# 跳过的测试在输出里和通过的一模一样，CI 没配库就会一路绿灯。
test-integration:
	@node -e "if (!process.env.TEST_MYSQL_URL) { console.error('请先设置 TEST_MYSQL_URL，例如 mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/central_db'); process.exit(2); }"
	cd backend && go test -tags=integration -count=1 ./...

# build 编译单个服务，用法：make build SERVICE=central
# 经 node 建目录：Makefile 在 Windows 上也用，mkdir -p 在那里不可用。
MKDIR = node -e "require('fs').mkdirSync('bin',{recursive:true})"

build:
	@node -e "if (!process.env.SERVICE) { console.error('用法：make build SERVICE=user'); process.exit(2); }"
	@$(MKDIR)
	cd backend && go build -trimpath -o ../bin/$(SERVICE) ./cmd/$(SERVICE)

build-all:
	@$(MKDIR)
	cd backend && go build -trimpath -o ../bin/ ./cmd/...

# 只删 bin 下的编译产物，不触碰 .tmp 与容器数据。
# 走 node 而非 rm -rf：Makefile 在 Windows 上也用，rm 在那里不存在。
clean:
	node -e "require('fs').rmSync('bin',{recursive:true,force:true})"

check: fmt lint test

# 开发栈启动走跨平台 Node 脚本（保留既有数据）；停止用 compose stop，
# 容器与数据卷保留，dev-up 可直接复起，不会触发重建。
dev-up:
	node scripts/dev/start.mjs

dev-down:
	docker compose -f docker/compose.dev.yaml stop

# 全链路场景验收（原 make e2e 隔离栈已退役）：make dev-up 启动开发栈并执行
# node scripts/dev/prepare.mjs 后，用 scripts/dev/e2e/scenarios.mjs 驱动场景。
