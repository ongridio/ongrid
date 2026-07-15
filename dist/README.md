# dist/ — ongrid release pipeline

This directory owns the **release/packaging** pipeline for ongrid. One command
produces one artefact, ready to scp to any Linux box with docker + docker
compose installed.

## What `make package` produces

A single tarball:

```
dist/out/ongrid-v<VERSION>-linux-amd64.tar.xz
dist/out/ongrid-v<VERSION>-linux-amd64.tar.xz.sha256
dist/out/ongrid-v<VERSION>-linux-arm64.tar.xz
dist/out/ongrid-v<VERSION>-linux-arm64.tar.xz.sha256
```

Unpacked layout:

```
ongrid-v<VERSION>-linux-<arch>/
  VERSION
  README.md              (from deploy/install/README.md)
  install.sh             (from deploy/install/install.sh)
  uninstall.sh
  upgrade.sh
  docker-compose.yml     (prod compose, from deploy/install/)
  .env.example
  prometheus/
    prometheus.yml       (from deploy/prometheus/)
  images/
    ongrid.tar           (docker save ongrid:<VERSION>)
  edge/
    ongrid-edge-linux-amd64
    ongrid-edge-linux-arm64
    ongrid-edge-darwin-amd64
    ongrid-edge-darwin-arm64
    install-edge.sh
    ongrid-edge.yaml.example
    ongrid-edge.service
```

The cloud service ships as a docker image tarball (`images/ongrid.tar`);
`install.sh` runs `docker load -i images/ongrid.tar` and then
`docker compose up -d`. Edge agents ship as static binaries for four OS/arch
combos so users can run them directly on heterogeneous hosts.

## Release flow

1. Bump the version: edit `VERSION` at the repo root (e.g. `v0.1.1`), commit
   the change, then tag that commit with the same value:
   `git tag v0.1.1 && git push origin v0.1.1`.
2. The `Release` GitHub Actions workflow runs on `v*.*.*` tag pushes and
   publishes the multi-architecture Kubernetes Edge image and matching Helm
   chart before building both server packages. The chart is published as an
   OCI artifact at `oci://helm.cnb.cool/ongridio/ongrid-edge`; it is not copied
   into the manager installation tarball. Use
   `make package TARGET_ARCH=arm64` locally only when you need a single ARM64
   package. The release build will:
   - `docker-push-k8s-edge` — publish the amd64/arm64 Edge image to CNB
   - `publish-k8s-chart` — package and publish the version-matched Helm chart
   - `build-edge-all`    — cross-compile ongrid-edge for 4 targets
   - `docker-build`      — build `ongrid:<VERSION>` image for `linux/<arch>`
   - stage everything under `dist/stage/ongrid-<VERSION>-linux-<arch>/`
   - emit the amd64/arm64 tarballs + sha256 files under `dist/out/`
3. Ship the matching package, for example:
   `scp dist/out/ongrid-v<VERSION>-linux-<arch>.tar.xz user@host:~/`.
4. On the target: untar, `sudo ./install.sh`.

## Checksum

`dist/out/ongrid-v<VERSION>-linux-<arch>.tar.xz.sha256` sits next to the
tarball. The install script can verify integrity with `sha256sum -c` on
Linux or `shasum -a 256 -c` on macOS.

## Local dry-run

Test the tarball without shipping:

```
make package
mkdir -p /tmp/ongrid-test && tar -xf dist/out/ongrid-v*.tar.xz -C /tmp/ongrid-test
cd /tmp/ongrid-test/ongrid-v*
ls -R
# Inside a disposable Ubuntu container with docker socket mounted:
#   docker run --rm -it -v $PWD:/pkg -v /var/run/docker.sock:/var/run/docker.sock \
#     ubuntu:22.04 bash -c 'cd /pkg && ./install.sh'
```

## Files in this directory

- `package.sh` — assembly script invoked by `make package`. Tolerates missing
  `deploy/install/*` files (warns, continues) so the pipeline is testable
  before the on-target scripts land.
- `README.md` — this file.

## What this directory does NOT own

- `deploy/install/**` — on-target install/uninstall/upgrade scripts and prod
  `docker-compose.yml`. Owned by the install-agent.
- `deploy/Dockerfile.*`, `deploy/docker-compose.yml` — build contexts and dev
  compose file.
- Images are **never** pushed to a registry from this pipeline. The tarball
  is the distribution channel.
