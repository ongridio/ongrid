# ongrid Makefile — 唯一构建/测试/部署入口（gospec 红线）
# 所有 CI / Dockerfile / README 都应只调 make target，禁裸 go build / docker build。

MODULE      := github.com/ongridio/ongrid
BIN_DIR     := bin
VERSION     := $(shell cat VERSION 2>/dev/null || git describe --tags --always --dirty 2>/dev/null || echo v0.0.0-dev)
LDFLAGS     := -X main.version=$(VERSION)
GO_BUILD    := go build -trimpath -ldflags '$(LDFLAGS)'

# Release/packaging paths
STAGE       := dist/stage/ongrid-$(VERSION)-linux-amd64
OUT         := dist/out
PLATFORM    ?= linux/amd64

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
	docker build -t ongrid:$(VERSION) -f deploy/Dockerfile.ongrid .

docker-ongrid-edge: ## 构建 ongrid-edge 镜像
	docker build -t ongrid-edge:$(VERSION) -f deploy/Dockerfile.ongrid-edge .

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
#
# Pipeline (wired via `make package`):
#   1. build-linux      — cross-compile ongrid for linux/amd64 (CGO off).
#   2. build-edge-all   — cross-compile ongrid-edge for 4 targets.
#   3. docker-build     — docker build ongrid:$(VERSION).
#   4. dist/package.sh  — stage + docker save + tar.xz + sha256.

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
		--load .

# Frontier broker is upstream singchia/frontier (ADR-007). Docker Hub pull
# is unreliable in some networks, so we build the image locally from the
# upstream source and ship it in the release tarball.
FRONTIER_SRC     ?= $(HOME)/frontier
FRONTIER_VERSION ?= v1.2.4

.PHONY: docker-build-broker
docker-build-broker: ## [release] 本地构建 singchia/frontier:$(FRONTIER_VERSION)（已存在则跳过）
	@if docker image inspect singchia/frontier:$(FRONTIER_VERSION) >/dev/null 2>&1; then \
		echo "[broker] singchia/frontier:$(FRONTIER_VERSION) already present locally — skipping rebuild"; \
	else \
		test -d $(FRONTIER_SRC) || { echo "FRONTIER_SRC=$(FRONTIER_SRC) not found and image absent locally"; exit 1; }; \
		docker buildx build \
			--platform $(PLATFORM) \
			-t singchia/frontier:$(FRONTIER_VERSION) \
			-f $(FRONTIER_SRC)/images/Dockerfile.frontier \
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

.PHONY: fetch-promtail
fetch-promtail: ## [release] 下载 promtail 到 bin/<os>-<arch>/promtail (Grafana 只发 linux 版本)
	@for target in linux-amd64 linux-arm64; do \
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
		curl -fsL -o $$zip $$url || { echo "promtail download failed for $$target"; exit 1; }; \
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
	@for target in linux-amd64 linux-arm64; do \
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
		curl -fsL -o $$tgz $$url || { echo "otelcol-contrib download failed for $$target"; exit 1; }; \
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

.PHONY: fetch-node-exporter
fetch-node-exporter: ## [release] 下载 node_exporter 到 bin/<os>-<arch>/node_exporter (linux-only)
	@for target in linux-amd64 linux-arm64; do \
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
		curl -fsL -o $$tgz $$url || { echo "node_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz --strip-components=1 -C $(BIN_DIR)/$$target node_exporter-$(NODE_EXPORTER_VERSION).$${os}-$${arch}/node_exporter || { echo "extract failed for $$target"; exit 1; }; \
		chmod +x $$dest; \
		rm -f $$tgz; \
		echo "[node_exporter] staged $$dest"; \
	done
	@echo "[node_exporter] note: linux-only (upstream doesn't ship darwin in releases)"

.PHONY: fetch-process-exporter
fetch-process-exporter: ## [release] 下载 process-exporter 到 bin/<os>-<arch>/process_exporter (linux-only)
	@for target in linux-amd64 linux-arm64; do \
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
		curl -fsL -o $$tgz $$url || { echo "process-exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz --strip-components=1 -C $(BIN_DIR)/$$target process-exporter-$(PROCESS_EXPORTER_VERSION).$${os}-$${arch}/process-exporter || { echo "extract failed for $$target"; exit 1; }; \
		mv $(BIN_DIR)/$$target/process-exporter $$dest; \
		chmod +x $$dest; \
		rm -f $$tgz; \
		echo "[process_exporter] staged $$dest"; \
	done
	@echo "[process_exporter] note: linux-only"

# package deps deliberately exclude `build-linux` and `build-web`:
#   - build-linux produces bin/linux-amd64/ongrid which dist/package.sh
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
	bash dist/build-edge-bundle.sh $(VERSION) linux-amd64 $(OUT)/edge-bundles

.PHONY: fetch-embedding-model
fetch-embedding-model: ## [release] 预拉 BGE 离线嵌入模型到 .cache/（幂等；package 会把它打进 tarball）
	bash dist/fetch-embedding-model.sh

.PHONY: package
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
package: fetch-promtail fetch-otelcol fetch-node-exporter fetch-process-exporter build-edge-all docker-build docker-build-broker docker-build-web ## [release] 打 release tarball 到 dist/out/
	@rm -rf dist/stage dist/out
	@mkdir -p dist/stage dist/out
	@$(MAKE) --no-print-directory build-edge-bundle
	bash dist/package.sh "$(VERSION)" "$(STAGE)" "$(OUT)"
	@echo ""
	@echo "=== release artefact ==="
	@ls -lh $(OUT)/ongrid-$(VERSION)-linux-amd64.tar.xz
	@if [ -f $(OUT)/ongrid-$(VERSION)-linux-amd64.tar.xz.sha256 ]; then \
		cat $(OUT)/ongrid-$(VERSION)-linux-amd64.tar.xz.sha256; \
	fi

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
