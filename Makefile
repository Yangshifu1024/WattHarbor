.PHONY: fmt lint test layers build build-all check clean
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
	bash scripts/check/layers.sh

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

check: fmt lint test layers
