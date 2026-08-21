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
# Package payloads stay architecture-specific for compatibility with existing
# download URLs and upgrade clients. Edge binaries remain external CNB assets,
# and every production package caches both architectures so mixed fleets can
# be upgraded; device dispatch still selects the matching architecture.
PACKAGE_EDGE_TARGETS ?= linux-amd64 linux-arm64
EDGE_PLUGIN_ARCHES ?= linux-amd64
STAGE       := dist/stage/ongrid-$(VERSION)-$(PACKAGE_TARGET)
OUT         := dist/out
PACKAGE_CLEAN ?= 1
# Local builds default to amd64. Release publishing produces one multi-arch
# manifest independently of the manager package architecture.
CLOUD_IMAGE_PLATFORMS ?= linux/amd64,linux/arm64
CLOUD_IMAGE_REPO ?= docker.cnb.cool/ongridio/ongrid
CLOUD_MANAGER_IMAGE_REF ?= $(CLOUD_IMAGE_REPO):$(VERSION)
CLOUD_WEB_IMAGE_REF ?= $(CLOUD_IMAGE_REPO)/ongrid-web:$(VERSION)
FRONTIER_VERSION ?= v1.2.4
K8S_EDGE_IMAGE_PLATFORM ?= linux/amd64
K8S_EDGE_IMAGE_PLATFORMS ?= linux/amd64,linux/arm64
K8S_EDGE_IMAGE_TAG ?= $(VERSION)
K8S_EDGE_IMAGE_REPO ?= docker.cnb.cool/ongridio/ongrid-edge
K8S_EDGE_IMAGE_REF ?= $(K8S_EDGE_IMAGE_REPO):$(K8S_EDGE_IMAGE_TAG)
# Edge installer payloads are direct CNB Release attachments. Public
# dependencies use an immutable tag derived from every upstream version and
# are uploaded only once; the small self-developed binary follows VERSION.
EDGE_ATTACHMENT_TARGETS ?= linux-amd64 linux-arm64
EDGE_DEPS_TAG ?= edge-deps-layout2-o$(OTELCOL_VERSION)-n$(NODE_EXPORTER_VERSION)-pr$(PROCESS_EXPORTER_VERSION)-my$(MYSQLD_EXPORTER_VERSION)-pg$(POSTGRES_EXPORTER_VERSION)-r$(REDIS_EXPORTER_VERSION)-m$(MONGODB_EXPORTER_VERSION)
EDGE_ATTACHMENTS_OUT ?= $(OUT)/edge-attachments
CNB_RELEASE_BASE_URL ?= https://cnb.cool/ongridio/ongrid-edge/-/releases/download
CNB_REPO_SLUG ?= ongridio/ongrid-edge
CNB_ATTACHMENTS_IMAGE ?= cnbcool/attachments@sha256:37c2d53fed9accee6ea0a509a05a4d05e4b36af37d5319451c2284e287b9e935
CNB_API_ENDPOINT ?= https://api.cnb.cool
CNB_RELEASE_TARGET_COMMITISH ?= main
K8S_CHART_VERSION ?= $(patsubst v%,%,$(VERSION))
K8S_CHART_PACKAGE ?= $(BIN_DIR)/k8s/ongrid-edge.tgz
K8S_CHART_REF ?= oci://helm.cnb.cool/ongridio/ongrid-edge
K8S_CHART_PUSH_TARGET ?= oci://helm.cnb.cool/ongridio
CNB_HELM_REGISTRY ?= helm.cnb.cool
CNB_HELM_USERNAME ?= cnb
RELEASE_MANIFEST_PLATFORM_FILTER ?= $(CURDIR)/scripts/release-manifest-platforms.jq
RELEASE_IMAGE_PUBLISHER ?= $(CURDIR)/scripts/publish-release-image.sh
RELEASE_IMAGE_PLATFORM_PUBLISHER ?= $(CURDIR)/scripts/publish-release-image-platform.sh
RELEASE_IMAGE_MANIFEST_MERGER ?= $(CURDIR)/scripts/merge-release-image-manifest.sh
RELEASE_IMAGE_PLATFORM ?= linux/amd64
RELEASE_IMAGE_DIGEST_DIR ?= dist/release-digests
RELEASE_IMAGE_METADATA_FILE ?= $(RELEASE_IMAGE_DIGEST_DIR)/manager-$(subst /,-,$(RELEASE_IMAGE_PLATFORM)).metadata.json
RELEASE_IMAGE_DIGEST_FILE ?= $(RELEASE_IMAGE_DIGEST_DIR)/manager-$(subst /,-,$(RELEASE_IMAGE_PLATFORM)).digest
RELEASE_MANAGER_AMD64_DIGEST_FILE ?= $(RELEASE_IMAGE_DIGEST_DIR)/manager-linux-amd64.digest
RELEASE_MANAGER_ARM64_DIGEST_FILE ?= $(RELEASE_IMAGE_DIGEST_DIR)/manager-linux-arm64.digest

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
			$$(find . -name '*.proto' -not -path './gen/*' -print); \
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
# Produces a release tarball ready to scp to any Linux box with docker +
# docker compose installed. Compose runtime images are pulled from CNB:
#
#     dist/out/ongrid-$(VERSION)-linux-amd64.tar.xz
#     dist/out/ongrid-$(VERSION)-linux-arm64.tar.xz
#
# Pipeline:
#   1. build-edge-attachments — build public dependency archives plus the
#      release-versioned ongrid-edge binaries for direct CNB downloads.
#   2. package — stage the thin Compose installer without those binaries.

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

.PHONY: docker-push-cloud-manager docker-push-cloud-manager-platform docker-merge-cloud-manager docker-push-cloud-web docker-push-cloud-images docker-push-release-images release-image-refs verify-release-images test-release-manifest-filter test-release-image-publish
docker-push-cloud-manager: ## [release] 发布 manager 多架构镜像到 CNB（兼容本地串行发布）
	bash "$(RELEASE_IMAGE_PUBLISHER)" \
		"$(CLOUD_MANAGER_IMAGE_REF)" \
		"$(RELEASE_MANIFEST_PLATFORM_FILTER)" \
		-- docker buildx build \
		--platform $(CLOUD_IMAGE_PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		-t $(CLOUD_MANAGER_IMAGE_REF) \
		-f deploy/Dockerfile.ongrid \
		$(DOCKER_BUILD_CACHE_ARGS) \
		--push .

docker-push-cloud-manager-platform: ## [release] 发布一个原生 manager 平台 digest，供 CI 并行聚合
	bash "$(RELEASE_IMAGE_PLATFORM_PUBLISHER)" \
		"$(CLOUD_MANAGER_IMAGE_REF)" \
		"$(RELEASE_MANIFEST_PLATFORM_FILTER)" \
		"$(RELEASE_IMAGE_METADATA_FILE)" \
		"$(RELEASE_IMAGE_DIGEST_FILE)" \
		-- docker buildx build \
		--platform "$(RELEASE_IMAGE_PLATFORM)" \
		--build-arg VERSION=$(VERSION) \
		--provenance=false \
		--metadata-file "$(RELEASE_IMAGE_METADATA_FILE)" \
		--output "type=image,name=$(CLOUD_IMAGE_REPO),push-by-digest=true,name-canonical=true,push=true" \
		-f deploy/Dockerfile.ongrid \
		$(DOCKER_BUILD_CACHE_ARGS) \
		.

docker-merge-cloud-manager: ## [release] 将原生 amd64/arm64 manager digest 聚合为不可变版本标签
	bash "$(RELEASE_IMAGE_MANIFEST_MERGER)" \
		"$(CLOUD_MANAGER_IMAGE_REF)" \
		"$(CLOUD_IMAGE_REPO)" \
		"$(RELEASE_MANIFEST_PLATFORM_FILTER)" \
		"$(RELEASE_MANAGER_AMD64_DIGEST_FILE)" \
		"$(RELEASE_MANAGER_ARM64_DIGEST_FILE)"

docker-push-cloud-web: ## [release] 发布 Web 多架构镜像到 CNB
	bash "$(RELEASE_IMAGE_PUBLISHER)" \
		"$(CLOUD_WEB_IMAGE_REF)" \
		"$(RELEASE_MANIFEST_PLATFORM_FILTER)" \
		-- docker buildx build \
		--platform $(CLOUD_IMAGE_PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		-t $(CLOUD_WEB_IMAGE_REF) \
		-f deploy/Dockerfile.web \
		$(DOCKER_BUILD_WEB_CACHE_ARGS) \
		--push .

docker-push-cloud-images: docker-push-cloud-manager docker-push-cloud-web ## [release] 发布 manager + Web 多架构镜像到 CNB

.PHONY: docker-build-k8s-edge docker-push-k8s-edge k8s-edge-image-ref
docker-build-k8s-edge: ## [dev] 构建本地 Kubernetes ongrid-edge 镜像（默认 linux/amd64）
	docker buildx build \
		--platform $(K8S_EDGE_IMAGE_PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg NODE_EXPORTER_VERSION=$(NODE_EXPORTER_VERSION) \
		--build-arg PROCESS_EXPORTER_VERSION=$(PROCESS_EXPORTER_VERSION) \
		--build-arg OTELCOL_VERSION=$(OTELCOL_VERSION) \
		-t ongrid-edge:$(VERSION) \
		-t $(K8S_EDGE_IMAGE_REF) \
		-f deploy/Dockerfile.ongrid-edge \
		--load .

docker-push-k8s-edge: ## [release] 发布 Kubernetes ongrid-edge 多架构镜像到 CNB
	bash "$(RELEASE_IMAGE_PUBLISHER)" \
		"$(K8S_EDGE_IMAGE_REF)" \
		"$(RELEASE_MANIFEST_PLATFORM_FILTER)" \
		-- docker buildx build \
		--platform $(K8S_EDGE_IMAGE_PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg NODE_EXPORTER_VERSION=$(NODE_EXPORTER_VERSION) \
		--build-arg PROCESS_EXPORTER_VERSION=$(PROCESS_EXPORTER_VERSION) \
		--build-arg OTELCOL_VERSION=$(OTELCOL_VERSION) \
		-t $(K8S_EDGE_IMAGE_REF) \
		-f deploy/Dockerfile.ongrid-edge \
		--push .

k8s-edge-image-ref:
	@printf '%s\n' "$(K8S_EDGE_IMAGE_REF)"

docker-push-release-images: docker-push-cloud-images docker-push-k8s-edge ## [release] 发布全部项目自身多架构镜像

release-image-refs: ## [release] 打印本次发布的项目自身镜像
	@printf '%s\n' "$(CLOUD_MANAGER_IMAGE_REF)" "$(CLOUD_WEB_IMAGE_REF)" "$(K8S_EDGE_IMAGE_REF)"

verify-release-images: ## [release] 校验项目自身镜像均包含 amd64 + arm64 manifest
	@command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }
	bash scripts/verify-release-images.sh \
		"$(RELEASE_MANIFEST_PLATFORM_FILTER)" \
		"$(CLOUD_MANAGER_IMAGE_REF)" \
		"$(CLOUD_WEB_IMAGE_REF)" \
		"$(K8S_EDGE_IMAGE_REF)"

test-release-manifest-filter: ## [test] 校验 release manifest 架构过滤器
	@command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }
	@printf '%s\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}},{"platform":{"os":"unknown","architecture":"unknown"}}]}' \
		| jq -e -f "$(RELEASE_MANIFEST_PLATFORM_FILTER)" >/dev/null
	@! printf '%s\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}' \
		| jq -e -f "$(RELEASE_MANIFEST_PLATFORM_FILTER)" >/dev/null

test-release-image-publish: test-release-manifest-filter ## [test] 校验 release 镜像幂等发布
	bash scripts/test-publish-release-image.sh
	bash scripts/test-release-image-platform-publish.sh

.PHONY: verify-compose-images
verify-compose-images: ## [test] 渲染并校验 Compose 运行镜像全部按预期指向 CNB
	bash scripts/verify-cnb-compose-images.sh

# Optional local-dev fallback for rebuilding the upstream Frontier broker.
# Release packages and Compose deployments use the existing CNB mirror.
FRONTIER_SRC     ?= $(HOME)/frontier
FRONTIER_BUILD_FORCE ?= 1

.PHONY: docker-build-broker
docker-build-broker: ## [dev] 从上游源码本地构建 singchia/frontier:$(FRONTIER_VERSION)
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

FETCH_CURL_FLAGS ?= -fL --retry 3 --retry-all-errors --retry-delay 3 --connect-timeout 15 --speed-time 60 --speed-limit 1024 --show-error

# OpenTelemetry Collector contrib bundle (logs and traces plugins).
# Cached under bin/<os>-<arch>/otelcol-contrib. Note: contrib build is
# ~200MB uncompressed per platform — operators wanting a slimmer agent can
# swap in a custom OCB build (otel-collector-builder); we ship contrib so
# default install works without forcing users to compile their own.
OTELCOL_VERSION ?= 0.157.0

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
		asset=otelcol-contrib_$(OTELCOL_VERSION)_$${os}_$${arch}.tar.gz; \
		base=https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v$(OTELCOL_VERSION); \
		tmpdir=$$(mktemp -d); tgz=$$tmpdir/$$asset; checksums=$$tmpdir/checksums.txt; \
		url=$$base/$$asset; \
		echo "[otelcol] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "otelcol-contrib download failed for $$target"; exit 1; }; \
		curl $(FETCH_CURL_FLAGS) -o $$checksums $$base/opentelemetry-collector-releases_otelcol-contrib_checksums.txt || { rm -rf $$tmpdir; echo "otelcol-contrib checksums download failed"; exit 1; }; \
		expected=$$(awk -v asset="$$asset" '$$2 == asset || $$2 == "*" asset { print $$1; exit }' $$checksums); \
		test -n "$$expected" || { rm -rf $$tmpdir; echo "otelcol-contrib checksum missing for $$asset"; exit 1; }; \
		if command -v sha256sum >/dev/null 2>&1; then actual=$$(sha256sum $$tgz | awk '{print $$1}'); else actual=$$(shasum -a 256 $$tgz | awk '{print $$1}'); fi; \
		test "$$actual" = "$$expected" || { rm -rf $$tmpdir; echo "otelcol-contrib checksum mismatch for $$asset"; exit 1; }; \
		tar -xzf $$tgz -C $(BIN_DIR)/$$target otelcol-contrib || { rm -rf $$tmpdir; echo "extract failed for $$target"; exit 1; }; \
		chmod +x $$dest; \
		rm -rf $$tmpdir; \
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
#   - the Compose installer pulls the published manager image at install time.
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

.PHONY: package-k8s-chart publish-k8s-chart test-k8s-chart test-publish-k8s-chart
package-k8s-chart: ## [dev/release] 打 Kubernetes Helm chart 到 bin/k8s/ongrid-edge.tgz
	@mkdir -p bin/k8s
	@rm -f bin/k8s/registry-setup.sh
	bash dist/package-k8s-chart.sh deploy/kubernetes/ongrid-edge $(K8S_CHART_PACKAGE) $(VERSION) $(K8S_EDGE_IMAGE_TAG)

publish-k8s-chart: package-k8s-chart ## [release] 发布 Kubernetes Helm chart 到 CNB OCI 制品库
	bash scripts/publish-helm-chart.sh \
		"$(K8S_CHART_REF)" \
		"$(K8S_CHART_VERSION)" \
		"$(K8S_CHART_PACKAGE)" \
		"$(K8S_CHART_PUSH_TARGET)" \
		"$(CNB_HELM_REGISTRY)" \
		"$(CNB_HELM_USERNAME)"

test-k8s-chart: package-k8s-chart ## [test] 校验 Kubernetes Helm Chart 的兼容、拆分、暂停与非法配置
	bash scripts/test-k8s-chart.sh deploy/kubernetes/ongrid-edge $(K8S_CHART_PACKAGE) $(K8S_EDGE_IMAGE_REF)

test-publish-k8s-chart: ## [test] 校验 Helm Chart 幂等发布
	bash scripts/test-publish-helm-chart.sh

.PHONY: fetch-embedding-model
fetch-embedding-model: ## [release] 预拉 BGE 离线嵌入模型到 .cache/（幂等；package 会把它打进 tarball）
	bash dist/fetch-embedding-model.sh

.PHONY: check-release-target package package-all test-release-package
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
	@targets="$(PACKAGE_EDGE_TARGETS)"; \
		[ -n "$$targets" ] || { echo "PACKAGE_EDGE_TARGETS must not be empty"; exit 2; }; \
		for target in $$targets; do \
			case "$$target" in linux-amd64|linux-arm64) ;; \
				*) echo "unsupported PACKAGE_EDGE_TARGETS value: $$target"; exit 2 ;; \
			esac; \
		done

# Direct CNB Release attachments. Target-specific EDGE_PLUGIN_ARCHES makes the
# existing fetch targets populate both Linux architectures. The dependency tag
# is immutable; the publisher skips it once every expected file exists.
.PHONY: edge-deps-tag verify-edge-deps-release verify-edge-version-release build-edge-deps-attachments build-edge-version-attachments build-edge-attachments publish-edge-deps-attachments publish-edge-version-attachments publish-edge-attachments test-edge-attachments test-release-workflow
edge-deps-tag: ## [release] 打印当前不可变公共依赖 Release tag
	@printf '%s\n' "$(EDGE_DEPS_TAG)"

verify-edge-deps-release: ## [release] 校验一次性公共 Edge 依赖 Release 已完整发布
	@tmp_dir=$$(mktemp -d); trap 'rm -rf "$$tmp_dir"' EXIT; \
	files=""; for target in $(EDGE_ATTACHMENT_TARGETS); do \
		files="$$files edge-deps-$$target.tar.xz"; \
	done; \
	bash "$(CURDIR)/scripts/verify-cnb-release-attachments.sh" \
		"$(CNB_RELEASE_BASE_URL)" "$(EDGE_DEPS_TAG)" --output-dir "$$tmp_dir" $$files; \
	for target in $(EDGE_ATTACHMENT_TARGETS); do \
		bash "$(CURDIR)/deploy/install/edge/verify-edge-deps-archive.sh" \
			"$$tmp_dir/edge-deps-$$target.tar.xz" "$$target" "$(EDGE_DEPS_TAG)"; \
	done
	@echo "verified immutable Edge dependency release $(EDGE_DEPS_TAG)"

verify-edge-version-release: ## [release] 校验当前 VERSION 的 Edge Release 已完整发布
	@files=""; for target in $(EDGE_ATTACHMENT_TARGETS); do \
		files="$$files ongrid-edge-$$target-$(VERSION)"; \
	done; \
	bash "$(CURDIR)/scripts/verify-cnb-release-attachments.sh" \
		"$(CNB_RELEASE_BASE_URL)" "$(VERSION)" $$files
	@echo "verified immutable Edge release $(VERSION)"

build-edge-deps-attachments: EDGE_PLUGIN_ARCHES := $(EDGE_ATTACHMENT_TARGETS)
build-edge-deps-attachments: fetch-otelcol fetch-node-exporter fetch-process-exporter fetch-db-exporters ## [release] 构建一次性公共 Edge 依赖附件
	OTELCOL_VERSION="$(OTELCOL_VERSION)" \
	NODE_EXPORTER_VERSION="$(NODE_EXPORTER_VERSION)" \
	PROCESS_EXPORTER_VERSION="$(PROCESS_EXPORTER_VERSION)" \
	MYSQLD_EXPORTER_VERSION="$(MYSQLD_EXPORTER_VERSION)" \
	POSTGRES_EXPORTER_VERSION="$(POSTGRES_EXPORTER_VERSION)" \
	REDIS_EXPORTER_VERSION="$(REDIS_EXPORTER_VERSION)" \
	MONGODB_EXPORTER_VERSION="$(MONGODB_EXPORTER_VERSION)" \
		bash dist/build-edge-attachments.sh deps "$(EDGE_DEPS_TAG)" "$(EDGE_ATTACHMENTS_OUT)" $(EDGE_ATTACHMENT_TARGETS)

build-edge-version-attachments: build-edge-linux-amd64 build-edge-linux-arm64 ## [release] 构建随 VERSION 变化的 ongrid-edge 附件
	bash dist/build-edge-attachments.sh edge "$(VERSION)" "$(EDGE_ATTACHMENTS_OUT)" $(EDGE_ATTACHMENT_TARGETS)

build-edge-attachments: build-edge-deps-attachments build-edge-version-attachments ## [release] 构建全部 CNB Edge 直链附件

publish-edge-deps-attachments: ## [release] 幂等创建并上传一次性公共依赖 Release
	@set -e; \
	if $(MAKE) --no-print-directory verify-edge-deps-release >/dev/null 2>&1; then \
		echo "CNB dependency release $(EDGE_DEPS_TAG) is complete; skip build and upload"; \
		exit 0; \
	fi; \
	$(MAKE) --no-print-directory build-edge-deps-attachments; \
	CNB_API_ENDPOINT="$(CNB_API_ENDPOINT)" \
	CNB_RELEASE_TARGET_COMMITISH="$(CNB_RELEASE_TARGET_COMMITISH)" \
		bash scripts/ensure-cnb-release.sh "$(EDGE_DEPS_TAG)" "$(CNB_REPO_SLUG)" \
		"Ongrid Edge shared dependencies" \
		"Immutable third-party Edge runtime dependencies. Source: https://github.com/ongridio/ongrid"; \
	files=""; for target in $(EDGE_ATTACHMENT_TARGETS); do \
		files="$$files $(CURDIR)/$(EDGE_ATTACHMENTS_OUT)/edge-deps-$$target.tar.xz $(CURDIR)/$(EDGE_ATTACHMENTS_OUT)/edge-deps-$$target.tar.xz.sha256"; \
	done; \
	CNB_API_ENDPOINT="$(CNB_API_ENDPOINT)" \
		bash scripts/publish-cnb-release-attachments.sh "$(EDGE_DEPS_TAG)" "$(CNB_REPO_SLUG)" "$(CNB_RELEASE_BASE_URL)" "$(CNB_ATTACHMENTS_IMAGE)" $$files

publish-edge-version-attachments: ## [release] 自动创建并上传当前 VERSION 的 ongrid-edge Release
	@set -e; \
	$(MAKE) --no-print-directory verify-edge-deps-release; \
	$(MAKE) --no-print-directory build-edge-version-attachments; \
	files=""; for target in $(EDGE_ATTACHMENT_TARGETS); do \
		files="$$files $(CURDIR)/$(EDGE_ATTACHMENTS_OUT)/ongrid-edge-$$target-$(VERSION) $(CURDIR)/$(EDGE_ATTACHMENTS_OUT)/ongrid-edge-$$target-$(VERSION).sha256"; \
	done; \
	if $(MAKE) --no-print-directory verify-edge-version-release >/dev/null 2>&1; then \
		CNB_API_ENDPOINT="$(CNB_API_ENDPOINT)" \
			bash scripts/publish-cnb-release-attachments.sh "$(VERSION)" "$(CNB_REPO_SLUG)" "$(CNB_RELEASE_BASE_URL)" "$(CNB_ATTACHMENTS_IMAGE)" $$files; \
		exit 0; \
	fi; \
	CNB_API_ENDPOINT="$(CNB_API_ENDPOINT)" \
	CNB_RELEASE_TARGET_COMMITISH="$(CNB_RELEASE_TARGET_COMMITISH)" \
		bash scripts/ensure-cnb-release.sh "$(VERSION)" "$(CNB_REPO_SLUG)" \
		"Ongrid Edge $(VERSION)" \
		"Ongrid Edge binaries for $(VERSION). Source: https://github.com/ongridio/ongrid"; \
	CNB_API_ENDPOINT="$(CNB_API_ENDPOINT)" \
		bash scripts/publish-cnb-release-attachments.sh "$(VERSION)" "$(CNB_REPO_SLUG)" "$(CNB_RELEASE_BASE_URL)" "$(CNB_ATTACHMENTS_IMAGE)" $$files

publish-edge-attachments: publish-edge-deps-attachments publish-edge-version-attachments ## [release] 上传公共依赖与当前版本 Edge 附件

test-edge-attachments: ## [test] 校验附件构建、直链下载和 checksum 拒绝路径
	bash scripts/test-edge-assets.sh
	bash scripts/test-edge-assets-lib.sh
	bash scripts/test-verify-cnb-release-attachments.sh
	bash scripts/test-ensure-cnb-release.sh
	bash scripts/test-publish-cnb-release-attachments.sh

test-release-workflow: ## [test] 校验 GitHub Release 必须等待 CNB Edge Release 发布
	bash scripts/test-release-workflow.sh

# Edge binaries are no longer package prerequisites. install.sh/upgrade.sh
# download and verify CNB Release files before changing the Manager /edge tree.
#
# NB: fetch-embedding-model is intentionally NOT a dep — pulling the BGE
# model is slow/brittle over CN networks, so it stays a one-off step.
# For offline RAG (ONGRID_EMBEDDING_PROVIDER=local) run
# `make fetch-embedding-model` once before `make package`, otherwise
# dist/package.sh warns and ships a tarball without the model.
package: check-release-target ## [release] 打兼容命名的单架构安装包（安装时缓存双架构 Edge 制品）
	@if [ "$(PACKAGE_CLEAN)" = "1" ]; then rm -rf dist/stage dist/out; fi
	@mkdir -p dist/stage dist/out
	@if [ "$(ONGRID_BUNDLE_EDGE_ASSETS)" = "1" ]; then \
		$(MAKE) --no-print-directory \
			$(addprefix build-edge-,$(PACKAGE_EDGE_TARGETS)) \
			fetch-otelcol fetch-node-exporter fetch-process-exporter fetch-db-exporters \
			EDGE_PLUGIN_ARCHES="$(PACKAGE_EDGE_TARGETS)"; \
	fi
	PACKAGE_TARGET="$(PACKAGE_TARGET)" \
	ONGRID_EDGE_DEPS_TAG="$(EDGE_DEPS_TAG)" \
	EDGE_TARGETS="$(PACKAGE_EDGE_TARGETS)" \
		bash dist/package.sh "$(VERSION)" "$(STAGE)" "$(OUT)"
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

test-release-package: ## [test] 校验安装 URL 与 Compose 发布包内容
	bash scripts/test-public-url.sh
	bash scripts/test-upgrade-data-permissions.sh
	bash scripts/test-install-asset-modes.sh
	$(MAKE) --no-print-directory test-edge-attachments
	bash scripts/test-compose-release-package.sh
	bash scripts/test-apply-pending-upgrade.sh

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
