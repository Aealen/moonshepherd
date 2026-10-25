# 牧云 Herd 构建脚本
# 开发机为 Windows + Git Bash：Makefile 目标均可直接在此环境执行（docs/02 §4）。
# 交叉编译产物输出到 dist/。

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/maxonphong/herd/version.Version=$(VERSION)
GOFLAGS := CGO_ENABLED=0

.PHONY: all build frontend backend agent panel test lint clean docker cross dev

all: frontend backend

## dev：本地开发（前端 vite 热更 + 后端 :8080，vite proxy 转发 /api 与 /agent）
dev:
	$(MAKE) -C web dev & \
	$(GOFLAGS) go run ./cmd/herd server --dev --data ./data

## frontend：构建前端到 web/dist（embed 源）
frontend:
	$(MAKE) -C web build

## backend：编译面板与 Agent（本机平台）
backend:
	$(GOFLAGS) go build -ldflags '$(LDFLAGS)' -o dist/herd ./cmd/herd
	$(GOFLAGS) go build -ldflags '$(LDFLAGS)' -o dist/shepherd ./cmd/shepherd

panel:
	$(GOFLAGS) go build -ldflags '$(LDFLAGS)' -o dist/herd ./cmd/herd

agent:
	$(GOFLAGS) go build -ldflags '$(LDFLAGS)' -o dist/shepherd ./cmd/shepherd

## cross：交叉编译 Linux 产物（shepherd 含 armv7，docs/02 §1.3）
cross: frontend
	mkdir -p dist
	$(GOFLAGS) GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/herd-linux-amd64 ./cmd/herd
	$(GOFLAGS) GOOS=linux GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/herd-linux-arm64 ./cmd/herd
	$(GOFLAGS) GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/shepherd-linux-amd64 ./cmd/shepherd
	$(GOFLAGS) GOOS=linux GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/shepherd-linux-arm64 ./cmd/shepherd
	$(GOFLAGS) GOOS=linux GOARCH=arm GOARM=7 go build -ldflags '$(LDFLAGS)' -o dist/shepherd-linux-armv7 ./cmd/shepherd

## test：全量测试
test:
	$(GOFLAGS) go test ./...

## lint
lint:
	$(GOFLAGS) go vet ./...

## docker：构建两个镜像
docker:
	docker build -f packaging/Dockerfile.panel -t herd-panel:$(VERSION) .
	docker build -f packaging/Dockerfile.agent -t shepherd:$(VERSION) .

## release：交叉编译 + 校验和（发布通道产物，docs/03 §6.2）
release: cross
	cd dist && sha256sum herd-* shepherd-* > sha256sums

clean:
	rm -rf dist web/dist/assets web/dist/index.html web/dist/favicon.svg
