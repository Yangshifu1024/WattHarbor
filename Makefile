.PHONY: fmt lint test layers layers-selftest table-refs current-state evidence build build-all check clean
.NOTPARALLEL: check

# cmd 下的服务名，编译产物统一落到 bin/
SERVICES := user admin central gateway worker migrate devseed simulator-dc589

fmt:
	go fmt ./...

lint:
	go vet ./...

test:
	go test ./...

layers:
	python3 scripts/check/layers.py

# 护栏自检：注入已知违规，确认每条规则真的会失败。
# "检查通过"不能证明规则有效——护栏最常见的失败是**靠放行通过**
# （路径范围没覆盖、正则只匹配一种写法），那种情况下它一路绿灯。
layers-selftest:
	python3 scripts/check/layers_selftest.py

# 代码引用的表名必须真实存在于 migrations。删表后最容易漏的就是
# db.Table("x") 这类字符串形式的引用——编译器不报错，只有运行时才炸。
table-refs:
	python3 scripts/check/table_refs.py

# 重生成 docs/target-structure.md 的「当前状态」小节。
# 那份文档同时是目标方案与实施记录，两种角色的数字会互相矛盾；
# 把它无法自己更新的部分（当前值）交给脚本，文档与代码就不会脱节。
current-state:
	python3 scripts/check/current_state.py

# 一次性打印重构完成证据（database 零业务依赖、宣告与注册表一致、
# 无已删表登记、OpenAPI 无孤儿、.tmp 未被跟踪、死代码已清、暂存区为空）。
# 它的第一版判据说 database 依赖 2 个 use case 包，那是正则漏了分组导致的
# 误报；修正后注入依赖验证过有判别力。任何独立复核者跑这一条就能自证。
evidence:
	python3 scripts/check/completion_evidence.py

# build 编译单个服务，用法：make build SERVICE=central
# 经 node 建目录：Makefile 在 Windows 上也用，mkdir -p 在那里不可用。
MKDIR = node -e "require('fs').mkdirSync('bin',{recursive:true})"

build:
	@test -n "$(SERVICE)" || { echo '用法：make build SERVICE=$(word 1,$(SERVICES))'; exit 2; }
	@$(MKDIR)
	go build -trimpath -o bin/$(SERVICE) ./cmd/$(SERVICE)

build-all:
	@$(MKDIR)
	@for s in $(SERVICES); do echo 'building' $$s; go build -trimpath -o bin/$$s ./cmd/$$s || exit 1; done

# 只删 bin 下的编译产物，不触碰 .tmp 与容器数据。
# 走 node 而非 rm -rf：Makefile 在 Windows 上也用，rm 在那里不存在。
clean:
	node -e "require('fs').rmSync('bin',{recursive:true,force:true})"

check: fmt lint test layers table-refs current-state evidence
