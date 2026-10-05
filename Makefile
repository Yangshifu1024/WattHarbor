.PHONY: fmt lint test test-integration build build-all check clean
.NOTPARALLEL: check

# cmd 下的服务名，编译产物统一落到 bin/
SERVICES := user admin central gateway worker migrate devseed simulator-dc589
export TEST_MYSQL_URL SERVICE

fmt:
	go fmt ./...

lint:
	go vet ./...

test:
	go test ./...

# 真库集成测试。SQL 里的判定（活动窗口、门槛、软删除）纯函数覆盖不到，
# 只能对着真库验；这些文件由 integration tag 隔离，make check 不依赖 Docker。
#
# 起库与迁移：
#   docker compose -f compose.dev.yaml up -d mysql
#   docker compose -f compose.dev.yaml run --rm migrate
# 跑测试：
#   make test-integration
#
# 刻意没有跳过路径：没配 TEST_MYSQL_URL 就直接失败而不是 skip——
# 跳过的测试在输出里和通过的一模一样，CI 没配库就会一路绿灯。
test-integration:
	@node -e "if (!process.env.TEST_MYSQL_URL) { console.error('请先设置 TEST_MYSQL_URL，例如 mysql://chargepilot:chargepilot_dev@127.0.0.1:3306/central_db'); process.exit(2); }"
	go test -tags=integration -count=1 ./...

# build 编译单个服务，用法：make build SERVICE=central
# 经 node 建目录：Makefile 在 Windows 上也用，mkdir -p 在那里不可用。
MKDIR = node -e "require('fs').mkdirSync('bin',{recursive:true})"

build:
	@node -e "if (!process.env.SERVICE) { console.error('用法：make build SERVICE=user'); process.exit(2); }"
	@$(MKDIR)
	go build -trimpath -o bin/$(SERVICE) ./cmd/$(SERVICE)

build-all:
	@$(MKDIR)
	go build -trimpath -o bin/ ./cmd/...

# 只删 bin 下的编译产物，不触碰 .tmp 与容器数据。
# 走 node 而非 rm -rf：Makefile 在 Windows 上也用，rm 在那里不存在。
clean:
	node -e "require('fs').rmSync('bin',{recursive:true,force:true})"

check: fmt lint test
