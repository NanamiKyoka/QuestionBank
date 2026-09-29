APP := qm-server
DESKTOP := qm-desktop
BIN := build/$(APP)
DESKTOP_BIN := build/$(DESKTOP)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

FRONTEND_DIR := ../frontend
WEBDIST      := internal/web/dist

# -s -w 去掉符号表与调试信息；版本信息在编译期注入，运行时可直接查询。
LDFLAGS := -s -w \
	-X github.com/NanamiKyoka/questionmind/internal/version.Version=$(VERSION) \
	-X github.com/NanamiKyoka/questionmind/internal/version.Commit=$(COMMIT) \
	-X github.com/NanamiKyoka/questionmind/internal/version.BuildTime=$(BUILT)

.PHONY: build run test fmt vet tidy clean cross check web web-clean full desktop

## build: 编译服务器版与桌面版（均内嵌前端，无需 CGO）
build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/server
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DESKTOP_BIN) ./cmd/desktop
	@echo "==> 产出: $(BIN) $(DESKTOP_BIN)"

## desktop: 只编译桌面版
desktop: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DESKTOP_BIN) ./cmd/desktop

## build-api: 只编译后端，不内嵌前端（前端改坏了也能出后端产物）
build-api:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/server

## web: 构建前端并复制到 embed 目录
#
# 先清空再复制：上一次构建残留的旧哈希文件会一起被打进二进制，
# 白白增大体积，还可能让用户加载到已经不该存在的资源。
#
# 前端目录不存在时只告警不失败——只做后端开发时不该被迫装 node。
web:
	@if [ -f "$(FRONTEND_DIR)/package.json" ]; then \
		echo "==> 构建前端"; \
		cd $(FRONTEND_DIR) && pnpm install --frozen-lockfile && pnpm build; \
		echo "==> 复制产物到 $(WEBDIST)"; \
		find $(WEBDIST) -mindepth 1 ! -name '.gitkeep' -delete; \
		cp -r $(FRONTEND_DIR)/dist/. $(WEBDIST)/; \
		echo "==> 前端产物就绪: $$(find $(WEBDIST) -type f | wc -l) 个文件"; \
	else \
		echo "==> 未找到前端目录，跳过前端构建（产物将只含 API）"; \
	fi

web-clean:
	find $(WEBDIST) -mindepth 1 ! -name '.gitkeep' -delete

## run: 直接运行（开发用）
run:
	go run ./cmd/server

## test: 运行全部单元测试
test:
	go test ./... -count=1

## check: 格式化检查 + 静态检查 + 测试，提交前跑一遍
check:
	@test -z "$$(gofmt -l .)" || (echo "以下文件未格式化:"; gofmt -l .; exit 1)
	go vet ./...
	go test ./... -count=1

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf build
	$(MAKE) web-clean

## cross: 交叉编译三个平台的产物（纯 Go，不需要额外工具链）
#
# 依赖 build 已经生成 $(WEBDIST)，因此这里不再重复构建前端。
cross:
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(APP)-linux-amd64 ./cmd/server
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(APP)-windows-amd64.exe ./cmd/server
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(APP)-darwin-arm64 ./cmd/server
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(DESKTOP)-windows-amd64.exe ./cmd/desktop
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(DESKTOP)-darwin-arm64 ./cmd/desktop
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(DESKTOP)-linux-amd64 ./cmd/desktop

## full: 前端 + 后端 + 测试，交付前跑一遍
full: web check
	$(MAKE) build-api
