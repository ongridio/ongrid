#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workflow="$repo_root/.github/workflows/release.yml"
makefile="$repo_root/Makefile"

command -v ruby >/dev/null 2>&1 || { echo "ruby is required" >&2; exit 1; }

ruby -ryaml -e '
workflow = YAML.safe_load(File.read(ARGV.fetch(0)), aliases: true)
jobs = workflow.fetch("jobs")

manager = jobs.fetch("manager-image")
raise "manager platform jobs must run on their native matrix runner" unless manager.fetch("runs-on") == "${{ matrix.runner }}"
matrix = manager.fetch("strategy").fetch("matrix").fetch("include")
platforms = matrix.map { |entry| [entry.fetch("arch"), entry.fetch("platform"), entry.fetch("runner")] }.sort
expected_platforms = [
  ["amd64", "linux/amd64", "ubuntu-24.04"],
  ["arm64", "linux/arm64", "ubuntu-24.04-arm"]
].sort
raise "manager must build amd64 and arm64 on native runners" unless platforms == expected_platforms
manager_publish = manager.fetch("steps").find { |step| step["name"] == "Publish native manager platform digest" }
raise "missing native manager platform publish step" unless manager_publish
raise "manager platform publish must call its Make target" unless manager_publish.fetch("run").include?("make docker-push-cloud-manager-platform")
manager_upload = manager.fetch("steps").find { |step| step["name"] == "Upload manager platform digest" }
raise "missing manager digest artifact upload" unless manager_upload
raise "manager digest upload path must match the Make digest directory" unless manager_upload.fetch("with").fetch("path").start_with?("dist/release-digests/")

web = jobs.fetch("web-image")
web_publish = web.fetch("steps").find { |step| step["name"] == "Publish Web image" }
raise "Web image publish must call its Make target" unless web_publish&.fetch("run")&.include?("make docker-push-cloud-web")

k8s_edge_image = jobs.fetch("k8s-edge-image")
k8s_edge_publish = k8s_edge_image.fetch("steps").find { |step| step["name"] == "Publish Kubernetes Edge image" }
raise "Kubernetes Edge image publish must call its Make target" unless k8s_edge_publish&.fetch("run")&.include?("make docker-push-k8s-edge")

image = jobs.fetch("image")
image_needs = Array(image.fetch("needs"))
raise "image finalization must wait for all parallel image builds" unless image_needs.sort == ["k8s-edge-image", "manager-image", "web-image"]
download = image.fetch("steps").find { |step| step["name"] == "Download manager platform digests" }
raise "missing manager digest artifact download" unless download
raise "manager digest download must merge both matrix artifacts" unless download.fetch("with").fetch("merge-multiple") == true
raise "manager digest download path must match the Make digest directory" unless download.fetch("with").fetch("path") == "dist/release-digests"
merge = image.fetch("steps").find { |step| step["name"] == "Merge manager multi-arch manifest" }
raise "manager manifest merge must call its Make target" unless merge&.fetch("run")&.include?("make docker-merge-cloud-manager")
verify = image.fetch("steps").find { |step| step["name"] == "Verify multi-arch manifests" }
raise "release images must be verified after manifest merge" unless verify&.fetch("run")&.include?("make verify-release-images")
helm = image.fetch("steps").find { |step| step["name"] == "Publish Helm chart" }
raise "Helm publish must call its Make target" unless helm&.fetch("run")&.include?("make publish-k8s-chart")

[manager, web, k8s_edge_image, image].each do |job|
  qemu = job.fetch("steps").any? { |step| step["uses"]&.start_with?("docker/setup-qemu-action@") }
  raise "optimized image jobs must not install QEMU" if qemu
  direct_docker = job.fetch("steps").any? { |step| step["run"]&.match?(/\bdocker buildx build\b/) }
  raise "release workflow must use Make targets instead of direct docker builds" if direct_docker
end

edge = jobs.fetch("edge-release")
raise "edge-release must wait for image publication" unless edge.fetch("needs") == "image"

build = jobs.fetch("build")
raise "package build must wait for finalized release images" unless build.fetch("needs") == "image"
matrix = build.fetch("strategy").fetch("matrix").fetch("include")
arches = matrix.map { |entry| entry.fetch("arch") }.sort
raise "release package matrix must contain amd64 and arm64" unless arches == ["amd64", "arm64"]
build_step = build.fetch("steps").find { |step| step["name"] == "Build ${{ matrix.arch }} package" }
raise "missing architecture-specific package build step" unless build_step
build_run = build_step.fetch("run")
raise "package build must pass the matrix architecture" unless build_run.include?("TARGET_ARCH=${{ matrix.arch }}")
upload = build.fetch("steps").find { |step| step["name"] == "Upload package artifact" }
raise "missing package upload step" unless upload
upload_path = upload.fetch("with").fetch("path")
raise "release package artifact is not architecture-specific" unless upload_path.include?("linux-${{ matrix.arch }}.tar.xz")

publish = edge.fetch("steps").find { |step| step["name"] == "Publish versioned Edge release to CNB" }
raise "missing Edge publish step" unless publish
raise "Edge publish step must call the Make target" unless publish.fetch("run").include?("make publish-edge-version-attachments")
raise "Edge publish step must use the CNB_TOKEN secret" unless publish.fetch("env").fetch("CNB_TOKEN") == "${{ secrets.CNB_TOKEN }}"
go_setup = edge.fetch("steps").find { |step| step["name"] == "Set up Go" }
raise "missing Edge Go setup step" unless go_setup
raise "Edge Go toolchain must be exact for immutable binary comparison" unless go_setup.fetch("with").fetch("go-version") == "1.25.11"

release_needs = Array(jobs.fetch("release").fetch("needs"))
raise "GitHub Release must wait for Edge publication" unless release_needs.sort == ["build", "edge-release"]
release = jobs.fetch("release")
download_release = release.fetch("steps").find { |step| step["name"] == "Download package artifacts" }
raise "release does not merge both package artifacts" unless download_release&.fetch("with")&.fetch("merge-multiple") == true
publish_release = release.fetch("steps").find { |step| step["name"] == "Publish GitHub Release" }
raise "missing GitHub Release publish step" unless publish_release
release_run = publish_release.fetch("run")
raise "GitHub Release does not publish the amd64 package" unless release_run.include?("linux-amd64.tar.xz")
raise "GitHub Release does not publish the arm64 package" unless release_run.include?("linux-arm64.tar.xz")
raise "GitHub Release still publishes the merged universal package" if release_run.match?(/linux\.tar\.xz/)
' "$workflow"

grep -Fxq 'PACKAGE_EDGE_TARGETS ?= linux-amd64 linux-arm64' "$makefile" \
    || { echo "production packages do not cache both Edge architectures" >&2; exit 1; }
grep -Fxq 'EDGE_ATTACHMENT_TARGETS ?= linux-amd64 linux-arm64' "$makefile" \
    || { echo "CNB Edge releases do not declare both architectures" >&2; exit 1; }
grep -Fq 'build-edge-version-attachments: build-edge-linux-amd64 build-edge-linux-arm64' "$makefile" \
    || { echo "versioned CNB Edge release does not build both architectures" >&2; exit 1; }

grep -Eq '^RELEASE_IMAGE_DIGEST_DIR \?= dist/release-digests$' "$makefile" \
    || { echo "Makefile manager digest directory does not match the workflow artifacts" >&2; exit 1; }
grep -Fq 'FROM --platform=$BUILDPLATFORM node:20-alpine AS builder' "$repo_root/deploy/Dockerfile.web" \
    || { echo "Web builder still runs once per emulated target architecture" >&2; exit 1; }
[[ $(grep -Fc 'FROM --platform=$BUILDPLATFORM' "$repo_root/deploy/Dockerfile.ongrid-edge") -eq 4 ]] \
    || { echo "Edge build/download stages are not all pinned to the native build platform" >&2; exit 1; }

if grep -Fq 'cnbcool/attachments:latest' "$makefile"; then
    echo "release configuration uses a mutable attachment uploader image" >&2
    exit 1
fi
grep -Eq '^CNB_ATTACHMENTS_IMAGE \?= cnbcool/attachments@sha256:[0-9a-f]{64}$' "$makefile" \
    || { echo "Makefile attachment uploader is not pinned by digest" >&2; exit 1; }
grep -Fq 'CNB dependency release $(EDGE_DEPS_TAG) is complete; skip build and upload' "$makefile" \
    || { echo "complete dependency Releases are not skipped before rebuilding" >&2; exit 1; }
grep -Fq 'scripts/verify-cnb-release-attachments.sh' "$makefile" \
    || { echo "Makefile release skip does not verify attachment contents" >&2; exit 1; }
[[ ! -e "$repo_root/.cnb.yml" ]] \
    || { echo "a second CNB tag publisher can bypass GitHub immutable checks" >&2; exit 1; }

ruby -e '
makefile = File.read(ARGV.fetch(0))
body = makefile[/^publish-edge-version-attachments:.*?\n(.*?)(?=^\S|\z)/m, 1]
raise "missing publish-edge-version-attachments recipe" unless body
build_at = body.index("build-edge-version-attachments")
verify_at = body.index("verify-edge-version-release")
publish_at = body.index("publish-cnb-release-attachments.sh")
raise "versioned Edge is not built before remote reuse is considered" unless build_at && verify_at && build_at < verify_at
raise "remote reuse is not compared through the immutable publisher" unless publish_at && verify_at < publish_at
' "$makefile"

echo "release workflow tests passed"
