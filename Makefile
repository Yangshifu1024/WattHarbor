.PHONY: fmt lint test test-integration build build-all check clean e2e e2e-down
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
#   pwsh -File scripts/db/test-integration.ps1
# 脚本使用 compose.test.yaml 的独立三库（宿主3307）与Redis（6381）；不要指向开发3306。
# 跑测试：
#   make test-integration
#
# 刻意没有跳过路径：没配 TEST_MYSQL_URL 就直接失败而不是 skip——
# 跳过的测试在输出里和通过的一模一样，CI 没配库就会一路绿灯。
test-integration:
	@node -e "if (!process.env.TEST_MYSQL_URL) { console.error('请先设置 TEST_MYSQL_URL，例如 mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/central_db'); process.exit(2); }"
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

# 端到端验收：五个服务以 docker/Dockerfile 在容器内编译并运行，不在本机编译；
# 复用 compose.test.yaml 的 3307 测试三库与 6381 Redis，场景与断言见
# docs/operations.md「隔离栈全链路验收」。central 8082 不映射宿主机端口。
# 第一步构建并启动（migrate 容器执行完迁移即退出），第二步等五个服务全部
# running 后由宿主机轮询 /health/ready，避免 distroless 无 shell 无法做
# 容器内健康检查导致的就绪竞态。模拟器与场景驱动仍按 runbook 在宿主机执行。
E2E_COMPOSE = -f compose.test.yaml -f compose.e2e.yaml

e2e:
	@node -e "require('fs').mkdirSync('.tmp/e2e',{recursive:true});require('fs').mkdirSync('.tmp/e2e-logs',{recursive:true})"
	docker compose $(E2E_COMPOSE) up -d --build
	docker compose $(E2E_COMPOSE) up -d --wait central gateway user admin worker
	@node -e "const ports=[8080,8081,8083,8085];const t0=Date.now();(async()=>{for(;;){try{const ok=(await Promise.all(ports.map(p=>fetch('http://127.0.0.1:'+p+'/health/ready').then(r=>r.ok)))).every(Boolean);if(ok){console.log('e2e services ready');return}}catch{}if(Date.now()-t0>60000)throw new Error('e2e 服务未就绪');await new Promise(r=>setTimeout(r,1000))}})().catch(e=>{console.error(e.message);process.exit(1)})"
	node scripts/dev/prepare.mjs
	@echo
	@echo "端到端栈已就绪。模拟器（独立终端）：scripts/dev/simulator.ps1 -Number 1"
	@echo "场景驱动：node scripts/dev/e2e/scenarios.mjs <budget|s3|s1|s2|s5|s7|s4|s8>"
	@echo "收尾：make e2e-down"

e2e-down:
	docker compose $(E2E_COMPOSE) down
