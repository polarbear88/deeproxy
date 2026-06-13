# deeproxy v2 Makefile
#
# 常用目标：
#   make web       前端构建（pnpm build → api/dist，被 embed）
#   make build     本机单平台构建（先确保 api/dist 存在）
#   make release   交叉编译全平台单一静态二进制（调用 build.sh，含前端构建）
#   make test      go test ./...
#   make race      go test -race ./...
#   make vet       go vet ./...
#   make deps-gate AC-43 静态依赖门禁：转发热路径包零 sql/sqlite/store
#   make smoke     构建本机二进制并冒烟启动（双端口 + -v 版本）
#   make e2e       完整二进制功能 E2E（.omc/research/e2e）
#   make clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := deeproxy

.PHONY: web build release test race vet deps-gate smoke e2e clean

web:
	cd web && pnpm install --frozen-lockfile && pnpm build

build:
	@test -f api/dist/index.html || (echo "api/dist 缺失，请先 make web 或保留占位"; exit 1)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/deeproxy

release:
	VERSION=$(VERSION) ./build.sh

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

# AC-43 静态依赖门禁：转发热路径包不得静态依赖 database/sql / sqlite / store。
deps-gate:
	@echo ">> AC-43 静态依赖门禁：server/rule/dialer/detect/auth/pool"
	@fail=0; \
	for p in server rule dialer detect auth pool; do \
	  if go list -deps ./$$p 2>/dev/null | grep -Eiq 'database/sql|sqlite|deeproxy/store'; then \
	    echo "   !! $$p 违规：静态依赖含 sql/sqlite/store"; fail=1; \
	  else echo "   ok $$p"; fi; \
	done; \
	if [ $$fail -ne 0 ]; then echo "AC-43 静态依赖门禁失败"; exit 1; fi; \
	echo "AC-43 静态依赖门禁通过"

smoke: build
	@echo ">> 版本冒烟"; ./$(BIN) -v
	@echo ">> 双端口启动冒烟（2s 后退出）"; \
	tmp=$$(mktemp -d); bin=$$(pwd)/$(BIN); \
	( cd $$tmp && $$bin --socks5 17680 --web 17690 & pid=$$!; sleep 2; kill $$pid 2>/dev/null ); \
	echo "冒烟启动 OK"; rm -rf $$tmp

e2e: build
	go run ./.omc/research/e2e/e2e.go -bin ./$(BIN)

clean:
	rm -f $(BIN); rm -rf dist
