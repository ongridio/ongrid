# ongrid Makefile — 唯一构建/测试/部署入口（gospec 红线）
# 所有 CI / Dockerfile / README 都应只调 make target，禁裸 go build / docker build。

MODULE      := github.com/ongridio/ongrid
BIN_DIR     := bin
VERSION     := $(shell cat VERSION 2>/dev/null || git describe --tags --always --dirty 2>/dev/null || echo v0.0.0-dev)
LDFLAGS     := -X main.version=$(VERSION)
GO_BUILD    := go build -trimpath -ldflags '$(LDFLAGS)'

# Release/packaging paths
ifneq ($(filter command line environment,$(origin PLATFORM)),)
PLATFORM_PARTS := $(subst /, ,$(PLATFORM))
TARGET_OS   ?= $(word 1,$(PLATFORM_PARTS))
TARGET_ARCH ?= $(word 2,$(PLATFORM_PARTS))
else
TARGET_OS   ?= linux
TARGET_ARCH ?= amd64
PLATFORM    ?= $(TARGET_OS)/$(TARGET_ARCH)
endif
PACKAGE_TARGET := $(TARGET_OS)-$(TARGET_ARCH)
# Edge plugin / agent binaries ship amd64-only by default (edges are amd64 in
# our deployments) — independent of the manager's per-arch TARGET_ARCH. This is
# the big size lever: otelcol-contrib alone is ~290M per arch. Override to
# "linux-amd64 linux-arm64" to fetch/bundle more edge arches. Kept in sync with
# package.sh's EDGE_TARGETS (the staging side).
EDGE_PLUGIN_ARCHES ?= linux-amd64
STAGE       := dist/stage/ongrid-$(VERSION)-$(PACKAGE_TARGET)
OUT         := dist/out
PACKAGE_CLEAN ?= 1
# Local builds default to amd64. Release publishing produces one multi-arch
# manifest independently of the manager package architecture.
K8S_EDGE_IMAGE_PLATFORM ?= linux/amd64
K8S_EDGE_IMAGE_PLATFORMS ?= linux/amd64,linux/arm64
K8S_EDGE_IMAGE_TAG ?= $(VERSION)
K8S_EDGE_IMAGE_REPO ?= docker.cnb.cool/ongridio/ongrid-edge
K8S_EDGE_IMAGE_REF ?= $(K8S_EDGE_IMAGE_REPO):$(K8S_EDGE_IMAGE_TAG)
K8S_CHART_VERSION ?= $(patsubst v%,%,$(VERSION))
K8S_CHART_PACKAGE ?= $(BIN_DIR)/k8s/ongrid-edge.tgz
K8S_CHART_REF ?= oci://helm.cnb.cool/ongridio/ongrid-edge
K8S_CHART_PUSH_TARGET ?= oci://helm.cnb.cool/ongridio
CNB_HELM_REGISTRY ?= helm.cnb.cool
CNB_HELM_USERNAME ?= cnb

DB_DSN     ?= root:root@tcp(127.0.0.1:3306)/ongrid?charset=utf8mb4&parseTime=true&loc=Local
MIGRATIONS := db/migrations

.DEFAULT_GOAL := help

# ----------------------------------------------------------------------------
# help
# ----------------------------------------------------------------------------

.PHONY: help
help: ## 列出全部 target
	@awk 'BEGIN{FS=":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} \
	     /^[a-zA-Z0-9_\/-]+:.*##/ {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ----------------------------------------------------------------------------
# build
# ----------------------------------------------------------------------------

.PHONY: build build-ongrid build-ongrid-edge
build: build-ongrid build-ongrid-edge ## 构建 ongrid 与 ongrid-edge

build-ongrid: ## 构建云端 ongrid
	@mkdir -p $(BIN_DIR)
	$(GO_BUILD) -o $(BIN_DIR)/ongrid ./cmd/ongrid

build-ongrid-edge: ## 构建边端 ongrid-edge
	@mkdir -p $(BIN_DIR)
	$(GO_BUILD) -o $(BIN_DIR)/ongrid-edge ./cmd/ongrid-edge

# ----------------------------------------------------------------------------
# test
# ----------------------------------------------------------------------------

.PHONY: test test-race test-integration test-e2e test-e2e-live
test: ## 单元测试
	go test ./...

test-race: ## 单元测试 + race
	go test -race ./...

test-integration: ## 集成测试（build tag: integration）
	go test -tags=integration ./...

test-e2e: ## E2E（默认 fakes，无外部凭证；catalog: docs/test/e2e-catalog.md）
	go test -tags=e2e -count=1 ./tests/e2e/...

test-e2e-live: ## E2E live mode（用 tests/e2e/secrets.local.env 打通真实外部服务）
	E2E_LIVE_ALL=1 go test -tags=e2e -count=1 -timeout=15m ./tests/e2e/...

# ----------------------------------------------------------------------------
# lint
# ----------------------------------------------------------------------------

.PHONY: lint arch-lint
lint: ## 运行 golangci-lint
	golangci-lint run

arch-lint: ## 运行 go-arch-lint（校验 BC 边界）
	@command -v go-arch-lint >/dev/null 2>&1 || { echo "go-arch-lint not installed; skipping"; exit 0; }
	go-arch-lint check

# ----------------------------------------------------------------------------
# proto
# ----------------------------------------------------------------------------

.PHONY: proto
proto: ## [api] 重新生成 proto（优先 buf，回退 protoc + protoc-gen-go/grpc）
	@if command -v buf >/dev/null 2>&1; then \
		echo "buf generate"; \
		cd api && buf generate; \
	else \
		echo "buf not installed; falling back to protoc"; \
		command -v protoc >/dev/null 2>&1 || { echo "protoc also missing"; exit 1; }; \
		command -v protoc-gen-go >/dev/null 2>&1 || { echo "protoc-gen-go missing (go install google.golang.org/protobuf/cmd/protoc-gen-go@latest)"; exit 1; }; \
		command -v protoc-gen-go-grpc >/dev/null 2>&1 || { echo "protoc-gen-go-grpc missing (go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest)"; exit 1; }; \
		mkdir -p api/gen; \
		cd api && protoc --proto_path=. \
			--go_out=gen --go_opt=paths=source_relative \
			--go-grpc_out=gen --go-grpc_opt=paths=source_relative \
			--go-grpc_opt=require_unimplemented_servers=true \
			frontierbound/v1/frontierbound.proto; \
	fi

# ----------------------------------------------------------------------------
# migrate
# ----------------------------------------------------------------------------

.PHONY: migrate-up migrate-down
migrate-up: ## DB migrate up（DB_DSN 可覆盖）
	migrate -path $(MIGRATIONS) -database "mysql://$(DB_DSN)" up

migrate-down: ## DB migrate down 1 步
	migrate -path $(MIGRATIONS) -database "mysql://$(DB_DSN)" down 1

# ----------------------------------------------------------------------------
# docker
# ----------------------------------------------------------------------------

.PHONY: docker docker-ongrid docker-ongrid-edge
docker: docker-ongrid docker-ongrid-edge ## 构建全部镜像

docker-ongrid: ## 构建 ongrid 镜像
	docker build --build-arg VERSION=$(VERSION) -t ongrid:$(VERSION) -f deploy/Dockerfile.ongrid .

docker-ongrid-edge: ## 构建 ongrid-edge 镜像
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg PROMTAIL_VERSION=$(PROMTAIL_VERSION) \
		--build-arg NODE_EXPORTER_VERSION=$(NODE_EXPORTER_VERSION) \
		--build-arg PROCESS_EXPORTER_VERSION=$(PROCESS_EXPORTER_VERSION) \
		--build-arg OTELCOL_VERSION=$(OTELCOL_VERSION) \
		-t ongrid-edge:$(VERSION) \
		-f deploy/Dockerfile.ongrid-edge .

# ----------------------------------------------------------------------------
# compose
# ----------------------------------------------------------------------------

.PHONY: compose-up compose-down
compose-up: ## 本地 docker compose 启动
	docker compose -f deploy/docker-compose.yml up -d

compose-down: ## 本地 docker compose 停止
	docker compose -f deploy/docker-compose.yml down

# ----------------------------------------------------------------------------
# run
# ----------------------------------------------------------------------------

.PHONY: run-ongrid run-ongrid-edge
run-ongrid: ## 本地直接跑 ongrid
	go run ./cmd/ongrid

run-ongrid-edge: ## 本地直接跑 ongrid-edge
	go run ./cmd/ongrid-edge

# ----------------------------------------------------------------------------
# Release / packaging
# ----------------------------------------------------------------------------
# Produces a single, self-contained tarball ready to scp to any Linux box with
# docker + docker compose installed:
#
#     dist/out/ongrid-$(VERSION)-linux-amd64.tar.xz
#     dist/out/ongrid-$(VERSION)-linux-arm64.tar.xz  (make package TARGET_ARCH=arm64)
#
# Pipeline (wired via `make package`):
#   1. build-edge-all   — cross-compile ongrid-edge for 4 targets.
#   2. docker-build     — docker build ongrid:$(VERSION) for $(PLATFORM).
#   3. dist/package.sh  — stage + docker save + tar.xz + sha256.

.PHONY: build-linux
build-linux: ## [release] 交叉编译 ongrid linux/amd64
	@mkdir -p $(BIN_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/linux-amd64/ongrid ./cmd/ongrid
	@echo "built $(BIN_DIR)/linux-amd64/ongrid"

.PHONY: build-edge-all
build-edge-all: build-edge-linux-amd64 build-edge-linux-arm64 build-edge-darwin-amd64 build-edge-darwin-arm64 ## [release] 交叉编译 ongrid-edge 全部 4 个目标
	@echo "built all edge binaries in $(BIN_DIR)/<os>-<arch>/ongrid-edge"

.PHONY: build-edge-linux-amd64
build-edge-linux-amd64: ## [release] edge linux/amd64
	@mkdir -p $(BIN_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/linux-amd64/ongrid-edge ./cmd/ongrid-edge

.PHONY: build-edge-linux-arm64
build-edge-linux-arm64: ## [release] edge linux/arm64
	@mkdir -p $(BIN_DIR)/linux-arm64
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/linux-arm64/ongrid-edge ./cmd/ongrid-edge

.PHONY: build-edge-darwin-amd64
build-edge-darwin-amd64: ## [release] edge darwin/amd64
	@mkdir -p $(BIN_DIR)/darwin-amd64
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/darwin-amd64/ongrid-edge ./cmd/ongrid-edge

.PHONY: build-edge-darwin-arm64
build-edge-darwin-arm64: ## [release] edge darwin/arm64
	@mkdir -p $(BIN_DIR)/darwin-arm64
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/darwin-arm64/ongrid-edge ./cmd/ongrid-edge

.PHONY: docker-build
docker-build: ## [release] 构建 ongrid:$(VERSION) 镜像（默认 linux/amd64，可用 PLATFORM 覆盖）
	docker buildx build \
		--platform $(PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		-t ongrid:$(VERSION) \
		-f deploy/Dockerfile.ongrid \
		$(DOCKER_BUILD_CACHE_ARGS) \
		--load .

# Frontend SPA + nginx (ADR-008). The image bakes web/dist/ into nginx so it
# can serve standalone; nginx.conf and TLS certs are bind-mounted at runtime.
.PHONY: build-web
build-web: ## [release] 编译前端 SPA 到 web/dist/
	cd web && npm ci && npm run build

.PHONY: docker-build-web
docker-build-web: ## [release] 构建 ongrid-web:$(VERSION) 镜像（前端 SPA + nginx）
	docker buildx build \
		--platform $(PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		-t ongrid-web:$(VERSION) \
		-f deploy/Dockerfile.web \
		$(DOCKER_BUILD_WEB_CACHE_ARGS) \
		--load .

.PHONY: docker-build-k8s-edge docker-push-k8s-edge k8s-edge-image-ref
docker-build-k8s-edge: ## [dev] 构建本地 Kubernetes ongrid-edge 镜像（默认 linux/amd64）
	docker buildx build \
		--platform $(K8S_EDGE_IMAGE_PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg PROMTAIL_VERSION=$(PROMTAIL_VERSION) \
		--build-arg NODE_EXPORTER_VERSION=$(NODE_EXPORTER_VERSION) \
		--build-arg PROCESS_EXPORTER_VERSION=$(PROCESS_EXPORTER_VERSION) \
		--build-arg OTELCOL_VERSION=$(OTELCOL_VERSION) \
		-t ongrid-edge:$(VERSION) \
		-t $(K8S_EDGE_IMAGE_REF) \
		-f deploy/Dockerfile.ongrid-edge \
		--load .

docker-push-k8s-edge: ## [release] 发布 Kubernetes ongrid-edge 多架构镜像到 CNB
	docker buildx build \
		--platform $(K8S_EDGE_IMAGE_PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg PROMTAIL_VERSION=$(PROMTAIL_VERSION) \
		--build-arg NODE_EXPORTER_VERSION=$(NODE_EXPORTER_VERSION) \
		--build-arg PROCESS_EXPORTER_VERSION=$(PROCESS_EXPORTER_VERSION) \
		--build-arg OTELCOL_VERSION=$(OTELCOL_VERSION) \
		-t $(K8S_EDGE_IMAGE_REF) \
		-f deploy/Dockerfile.ongrid-edge \
		--push .

k8s-edge-image-ref:
	@printf '%s\n' "$(K8S_EDGE_IMAGE_REF)"

# Frontier broker is upstream singchia/frontier (ADR-007). Docker Hub pull
# is unreliable in some networks, so we build the image locally from the
# upstream source and ship it in the release tarball.
FRONTIER_SRC     ?= $(HOME)/frontier
FRONTIER_VERSION ?= v1.2.4
FRONTIER_BUILD_FORCE ?= 1

.PHONY: docker-build-broker
docker-build-broker: ## [release] 本地构建 singchia/frontier:$(FRONTIER_VERSION)
	@existing_platform=$$(docker image inspect -f '{{.Os}}/{{.Architecture}}' singchia/frontier:$(FRONTIER_VERSION) 2>/dev/null || true); \
	if [ "$(FRONTIER_BUILD_FORCE)" != "1" ] && [ "$$existing_platform" = "$(PLATFORM)" ]; then \
		echo "[broker] singchia/frontier:$(FRONTIER_VERSION) already present for $(PLATFORM) — skipping rebuild"; \
	else \
		test -d $(FRONTIER_SRC) || { echo "FRONTIER_SRC=$(FRONTIER_SRC) not found and local image is not for $(PLATFORM)"; exit 1; }; \
		docker buildx build \
			--platform $(PLATFORM) \
			-t singchia/frontier:$(FRONTIER_VERSION) \
			-f deploy/Dockerfile.frontier \
			$(DOCKER_BUILD_BROKER_CACHE_ARGS) \
			--load $(FRONTIER_SRC); \
	fi

.PHONY: docker-save
docker-save: ## [release] docker save ongrid:$(VERSION) 到 stage
	@mkdir -p $(STAGE)/images
	docker save ongrid:$(VERSION) -o $(STAGE)/images/ongrid.tar
	@echo "saved $(STAGE)/images/ongrid.tar"

# Promtail bundle (ADR-012 / ADR-015 logs plugin).
# Cached under bin/<os>-<arch>/promtail to avoid re-downloading on every build.
PROMTAIL_VERSION ?= 3.4.0
FETCH_CURL_FLAGS ?= -fL --retry 3 --retry-all-errors --retry-delay 3 --connect-timeout 15 --speed-time 60 --speed-limit 1024 --show-error

.PHONY: fetch-promtail
fetch-promtail: ## [release] 下载 promtail 到 bin/<os>-<arch>/promtail (Grafana 只发 linux 版本)
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/promtail; \
		if [ -f $$dest ]; then \
			echo "[promtail] $$dest already present — skip"; \
			continue; \
		fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		zip=/tmp/promtail-$$os-$$arch.zip; \
		url=https://github.com/grafana/loki/releases/download/v$(PROMTAIL_VERSION)/promtail-$$os-$$arch.zip; \
		echo "[promtail] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$zip $$url || { echo "promtail download failed for $$target"; exit 1; }; \
		unzip -p $$zip > $$dest; \
		chmod +x $$dest; \
		rm -f $$zip; \
		echo "[promtail] staged $$dest"; \
	done
	@echo "[promtail] note: Grafana doesn't ship darwin binaries — edge on macOS hosts will see logs plugin disabled (warned by install-edge.sh)"

# OpenTelemetry Collector contrib bundle (ADR-013 / ADR-015 traces plugin).
# Cached under bin/<os>-<arch>/otelcol-contrib. Note: contrib build is
# ~200MB uncompressed per platform — operators wanting a slimmer agent can
# swap in a custom OCB build (otel-collector-builder); we ship contrib so
# default install works without forcing users to compile their own.
OTELCOL_VERSION ?= 0.118.0

.PHONY: fetch-otelcol
fetch-otelcol: ## [release] 下载 otelcol-contrib 到 bin/<os>-<arch>/otelcol-contrib (linux-only)
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/otelcol-contrib; \
		if [ -f $$dest ]; then \
			echo "[otelcol] $$dest already present — skip"; \
			continue; \
		fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/otelcol-contrib-$$os-$$arch.tar.gz; \
		url=https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v$(OTELCOL_VERSION)/otelcol-contrib_$(OTELCOL_VERSION)_$${os}_$${arch}.tar.gz; \
		echo "[otelcol] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { echo "otelcol-contrib download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $(BIN_DIR)/$$target otelcol-contrib || { echo "extract failed for $$target"; exit 1; }; \
		chmod +x $$dest; \
		rm -f $$tgz; \
		echo "[otelcol] staged $$dest"; \
	done
	@echo "[otelcol] note: contrib distro is ~200MB per platform; operators wanting smaller agent can build a custom OCB collector and drop it under /usr/local/lib/ongrid-edge/otelcol-contrib"

# node_exporter — host metric source bundled with the edge package
# (CPU / memory / disk / network / load). Without this, install-edge
# leaves the operator without a metric source on the host and Monitor
# panels stay empty. Cached under bin/<os>-<arch>/node_exporter.
NODE_EXPORTER_VERSION ?= 1.8.2

# process-exporter — per-process metrics (groupable by comm / cmdline)
# used to back the "Top N processes timeline" panel via PromQL
# instead of the on-demand gopsutil RPC. Cached under
# bin/<os>-<arch>/process_exporter. Sticks with the Prometheus
# ecosystem (matches node_exporter's deploy + metric-naming model)
# rather than mixing in otelcol hostmetrics.
PROCESS_EXPORTER_VERSION ?= 0.8.4
MYSQLD_EXPORTER_VERSION ?= 0.19.0
POSTGRES_EXPORTER_VERSION ?= 0.19.1
REDIS_EXPORTER_VERSION ?= 1.86.0
MONGODB_EXPORTER_VERSION ?= 0.51.0

.PHONY: fetch-node-exporter
fetch-node-exporter: ## [release] 下载 node_exporter 到 bin/<os>-<arch>/node_exporter (linux-only)
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/node_exporter; \
		if [ -f $$dest ]; then \
			echo "[node_exporter] $$dest already present — skip"; \
			continue; \
		fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/node_exporter-$$os-$$arch.tar.gz; \
		url=https://github.com/prometheus/node_exporter/releases/download/v$(NODE_EXPORTER_VERSION)/node_exporter-$(NODE_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[node_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { echo "node_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz --strip-components=1 -C $(BIN_DIR)/$$target node_exporter-$(NODE_EXPORTER_VERSION).$${os}-$${arch}/node_exporter || { echo "extract failed for $$target"; exit 1; }; \
		chmod +x $$dest; \
		rm -f $$tgz; \
		echo "[node_exporter] staged $$dest"; \
	done
	@echo "[node_exporter] note: linux-only (upstream doesn't ship darwin in releases)"

.PHONY: fetch-process-exporter
fetch-process-exporter: ## [release] 下载 process-exporter 到 bin/<os>-<arch>/process_exporter (linux-only)
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/process_exporter; \
		if [ -f $$dest ]; then \
			echo "[process_exporter] $$dest already present — skip"; \
			continue; \
		fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/process_exporter-$$os-$$arch.tar.gz; \
		url=https://github.com/ncabatoff/process-exporter/releases/download/v$(PROCESS_EXPORTER_VERSION)/process-exporter-$(PROCESS_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[process_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { echo "process-exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz --strip-components=1 -C $(BIN_DIR)/$$target process-exporter-$(PROCESS_EXPORTER_VERSION).$${os}-$${arch}/process-exporter || { echo "extract failed for $$target"; exit 1; }; \
		mv $(BIN_DIR)/$$target/process-exporter $$dest; \
		chmod +x $$dest; \
		rm -f $$tgz; \
		echo "[process_exporter] staged $$dest"; \
	done
	@echo "[process_exporter] note: linux-only"

.PHONY: fetch-db-exporters fetch-mysqld-exporter fetch-postgres-exporter fetch-redis-exporter fetch-mongodb-exporter
fetch-db-exporters: fetch-mysqld-exporter fetch-postgres-exporter fetch-redis-exporter fetch-mongodb-exporter ## [release] 下载数据库 exporter 到 bin/<os>-<arch>/ (linux-only)

fetch-mysqld-exporter: ## [release] 下载 mysqld_exporter 到 bin/<os>-<arch>/mysqld_exporter
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/mysqld_exporter; \
		if [ -f $$dest ]; then echo "[mysqld_exporter] $$dest already present — skip"; continue; fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/mysqld_exporter-$$os-$$arch.tar.gz; tmpdir=$$(mktemp -d); \
		url=https://github.com/prometheus/mysqld_exporter/releases/download/v$(MYSQLD_EXPORTER_VERSION)/mysqld_exporter-$(MYSQLD_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[mysqld_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "mysqld_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $$tmpdir || { rm -rf $$tmpdir $$tgz; echo "extract failed for $$target"; exit 1; }; \
		found=$$(find $$tmpdir -type f -name mysqld_exporter -print -quit); \
		test -n "$$found" || { rm -rf $$tmpdir $$tgz; echo "mysqld_exporter binary missing in archive for $$target"; exit 1; }; \
		install -m 0755 "$$found" $$dest; \
		rm -rf $$tmpdir $$tgz; \
		echo "[mysqld_exporter] staged $$dest"; \
	done

fetch-postgres-exporter: ## [release] 下载 postgres_exporter 到 bin/<os>-<arch>/postgres_exporter
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/postgres_exporter; \
		if [ -f $$dest ]; then echo "[postgres_exporter] $$dest already present — skip"; continue; fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/postgres_exporter-$$os-$$arch.tar.gz; tmpdir=$$(mktemp -d); \
		url=https://github.com/prometheus-community/postgres_exporter/releases/download/v$(POSTGRES_EXPORTER_VERSION)/postgres_exporter-$(POSTGRES_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[postgres_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "postgres_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $$tmpdir || { rm -rf $$tmpdir $$tgz; echo "extract failed for $$target"; exit 1; }; \
		found=$$(find $$tmpdir -type f -name postgres_exporter -print -quit); \
		test -n "$$found" || { rm -rf $$tmpdir $$tgz; echo "postgres_exporter binary missing in archive for $$target"; exit 1; }; \
		install -m 0755 "$$found" $$dest; \
		rm -rf $$tmpdir $$tgz; \
		echo "[postgres_exporter] staged $$dest"; \
	done

fetch-redis-exporter: ## [release] 下载 redis_exporter 到 bin/<os>-<arch>/redis_exporter
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/redis_exporter; \
		if [ -f $$dest ]; then echo "[redis_exporter] $$dest already present — skip"; continue; fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/redis_exporter-$$os-$$arch.tar.gz; tmpdir=$$(mktemp -d); \
		url=https://github.com/oliver006/redis_exporter/releases/download/v$(REDIS_EXPORTER_VERSION)/redis_exporter-v$(REDIS_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[redis_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "redis_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $$tmpdir || { rm -rf $$tmpdir $$tgz; echo "extract failed for $$target"; exit 1; }; \
		found=$$(find $$tmpdir -type f -name redis_exporter -print -quit); \
		test -n "$$found" || { rm -rf $$tmpdir $$tgz; echo "redis_exporter binary missing in archive for $$target"; exit 1; }; \
		install -m 0755 "$$found" $$dest; \
		rm -rf $$tmpdir $$tgz; \
		echo "[redis_exporter] staged $$dest"; \
	done

fetch-mongodb-exporter: ## [release] 下载 mongodb_exporter 到 bin/<os>-<arch>/mongodb_exporter
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/mongodb_exporter; \
		if [ -f $$dest ]; then echo "[mongodb_exporter] $$dest already present — skip"; continue; fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/mongodb_exporter-$$os-$$arch.tar.gz; tmpdir=$$(mktemp -d); \
		url=https://github.com/percona/mongodb_exporter/releases/download/v$(MONGODB_EXPORTER_VERSION)/mongodb_exporter-$(MONGODB_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[mongodb_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "mongodb_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $$tmpdir || { rm -rf $$tmpdir $$tgz; echo "extract failed for $$target"; exit 1; }; \
		found=$$(find $$tmpdir -type f -name mongodb_exporter -print -quit); \
		test -n "$$found" || { rm -rf $$tmpdir $$tgz; echo "mongodb_exporter binary missing in archive for $$target"; exit 1; }; \
		install -m 0755 "$$found" $$dest; \
		rm -rf $$tmpdir $$tgz; \
		echo "[mongodb_exporter] staged $$dest"; \
	done

# package deps deliberately exclude `build-linux` and `build-web`:
#   - build-linux produces a host-side ongrid binary which dist/package.sh
#     never consumes (the manager binary inside ongrid:VERSION docker
#     image is what's shipped; the host-side cross-compile was dead
#     code costing ~1-3 min per run).
#   - build-web produces web/dist/ which docker-build-web doesn't use
#     either — the web Dockerfile runs its own `npm ci && npm run
#     build` inside the builder stage. Removing the host-side npm pass
#     saves another ~2-5 min per run.
# Run those targets manually if you need the host-side artefacts
# (e.g. for `make run-ongrid` debugging).
.PHONY: build-edge-bundle
build-edge-bundle: ## [release] 打 ADR-024 edge upgrade bundle 到 dist/out/edge-bundles/
	@mkdir -p $(OUT)/edge-bundles
	@for arch in $(EDGE_PLUGIN_ARCHES); do \
		bash dist/build-edge-bundle.sh $(VERSION) $$arch $(OUT)/edge-bundles; \
	done

.PHONY: package-k8s-chart publish-k8s-chart
package-k8s-chart: ## [dev/release] 打 Kubernetes Helm chart 到 bin/k8s/ongrid-edge.tgz
	@mkdir -p bin/k8s
	@rm -f bin/k8s/registry-setup.sh
	bash dist/package-k8s-chart.sh deploy/kubernetes/ongrid-edge $(K8S_CHART_PACKAGE) $(VERSION) $(K8S_EDGE_IMAGE_TAG)

publish-k8s-chart: package-k8s-chart ## [release] 发布 Kubernetes Helm chart 到 CNB OCI 制品库
	@command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
	@test -n "$$CNB_TOKEN" || { echo "CNB_TOKEN is required" >&2; exit 1; }
	@printf '%s' "$$CNB_TOKEN" | helm registry login $(CNB_HELM_REGISTRY) \
		--username $(CNB_HELM_USERNAME) \
		--password-stdin
	helm push $(K8S_CHART_PACKAGE) $(K8S_CHART_PUSH_TARGET)
	helm show chart $(K8S_CHART_REF) --version $(K8S_CHART_VERSION) >/dev/null

.PHONY: fetch-embedding-model
fetch-embedding-model: ## [release] 预拉 BGE 离线嵌入模型到 .cache/（幂等；package 会把它打进 tarball）
	bash dist/fetch-embedding-model.sh

.PHONY: check-release-target package package-all
check-release-target:
	@if [ "$(PLATFORM)" != "$(TARGET_OS)/$(TARGET_ARCH)" ]; then \
		echo "PLATFORM=$(PLATFORM) does not match TARGET_OS/TARGET_ARCH=$(TARGET_OS)/$(TARGET_ARCH)"; \
		echo "Use TARGET_ARCH=arm64 or PLATFORM=linux/arm64, but keep them consistent."; \
		exit 2; \
	fi
	@case "$(PACKAGE_TARGET)" in \
		linux-amd64|linux-arm64) ;; \
		*) echo "unsupported PACKAGE_TARGET=$(PACKAGE_TARGET); expected linux-amd64 or linux-arm64"; exit 2 ;; \
	esac

# Order matters: fetch-* / build-edge-all populate bin/ → docker-* bake
# the images → recipe-time we rebuild the edge bundle (because dist/out
# gets wiped first) and only then dist/package.sh assembles the
# release tarball that includes the bundle as a sibling of the per-arch
# edge binaries (ADR-024).
#
# NB: fetch-embedding-model is intentionally NOT a dep — pulling the BGE
# model is slow/brittle over CN networks, so it stays a one-off step.
# For offline RAG (ONGRID_EMBEDDING_PROVIDER=local) run
# `make fetch-embedding-model` once before `make package`, otherwise
# dist/package.sh warns and ships a tarball without the model.
package: check-release-target fetch-promtail fetch-otelcol fetch-node-exporter fetch-process-exporter fetch-db-exporters build-edge-all docker-build docker-build-broker docker-build-web ## [release] 打单架构 release tarball 到 dist/out/（TARGET_ARCH 可覆盖）
	@if [ "$(PACKAGE_CLEAN)" = "1" ]; then rm -rf dist/stage dist/out; fi
	@mkdir -p dist/stage dist/out
	@$(MAKE) --no-print-directory build-edge-bundle
	PACKAGE_TARGET="$(PACKAGE_TARGET)" DOCKER_PLATFORM="$(PLATFORM)" bash dist/package.sh "$(VERSION)" "$(STAGE)" "$(OUT)"
	@echo ""
	@echo "=== release artefact ==="
	@ls -lh $(OUT)/ongrid-$(VERSION)-$(PACKAGE_TARGET).tar.xz
	@if [ -f $(OUT)/ongrid-$(VERSION)-$(PACKAGE_TARGET).tar.xz.sha256 ]; then \
		cat $(OUT)/ongrid-$(VERSION)-$(PACKAGE_TARGET).tar.xz.sha256; \
	fi

package-all: ## [release] 打 amd64 + arm64 两个生产安装包到 dist/out/
	@rm -rf dist/stage dist/out
	@mkdir -p dist/stage dist/out
	@$(MAKE) --no-print-directory package TARGET_OS=linux TARGET_ARCH=amd64 PLATFORM=linux/amd64 PACKAGE_CLEAN=0
	@$(MAKE) --no-print-directory package TARGET_OS=linux TARGET_ARCH=arm64 PLATFORM=linux/arm64 PACKAGE_CLEAN=0
	@echo ""
	@echo "=== release artefacts ==="
	@ls -lh $(OUT)/ongrid-$(VERSION)-linux-amd64.tar.xz $(OUT)/ongrid-$(VERSION)-linux-arm64.tar.xz
	@for f in $(OUT)/ongrid-$(VERSION)-linux-amd64.tar.xz.sha256 $(OUT)/ongrid-$(VERSION)-linux-arm64.tar.xz.sha256; do \
		[ -f "$$f" ] && cat "$$f"; \
	done

.PHONY: dist-clean
dist-clean: ## [release] 清理 release 产物（dist/stage dist/out bin/<os>-*）
	rm -rf dist/stage dist/out $(BIN_DIR)/linux-* $(BIN_DIR)/darwin-* $(BIN_DIR)/windows-*

.PHONY: version-print
version-print: ## [release] 打印当前 VERSION（CI 消费用）
	@echo $(VERSION)

# ----------------------------------------------------------------------------
# clean
# ----------------------------------------------------------------------------

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR) coverage.out coverage.html
