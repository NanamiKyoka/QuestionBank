APP := qm-server
BIN := build/$(APP)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# -s -w 去掉符号表与调试信息；版本信息在编译期注入，运行时可直接查询。
LDFLAGS := -s -w \
	-X github.com/NanamiKyoka/questionmind/internal/version.Version=$(VERSION) \
	-X github.com/NanamiKyoka/questionmind/internal/version.Commit=$(COMMIT) \
	-X github.com/NanamiKyoka/questionmind/internal/version.BuildTime=$(BUILT)

.PHONY: build run test fmt vet tidy clean cross check

## build: 编译当前平台的单文件可执行程序（无需 CGO）
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/server

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

## cross: 交叉编译三个平台的产物（纯 Go，不需要额外工具链）
cross:
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(APP)-linux-amd64 ./cmd/server
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(APP)-windows-amd64.exe ./cmd/server
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(APP)-darwin-arm64 ./cmd/server
