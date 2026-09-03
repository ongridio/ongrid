# dist/ — ongrid release pipeline

This directory owns the **release/packaging** pipeline for ongrid. One command
produces one installation artefact; Compose runtime images are published and
pulled separately from the project CNB registry.

## What `make package` produces

A single tarball:

```
dist/out/ongrid-v<VERSION>-linux.tar.xz
dist/out/ongrid-v<VERSION>-linux.tar.xz.sha256
```

Unpacked layout:

```
ongrid-v<VERSION>-linux/
  VERSION
  README.md              (from deploy/install/README.md)
  install.sh             (from deploy/install/install.sh)
  uninstall.sh
  upgrade.sh
  public-url.sh          (installer URL validation helper)
  data-permissions.sh    (upgrade ownership helper)
  docker-compose.yml     (prod compose, from deploy/install/)
  .env.example
  prometheus.yml         (Compose scrape config)
  embeddings/            (optional bundled local embedding model)
  edge/
    fetch-edge-assets.sh
    edge-artifacts.env   (immutable public dependency Release tag)
    build-edge-bundle.sh
    install-edge.sh
    ongrid-edge.env.example
    ongrid-edge.service
```

The package supports Compose installation only and does not embed image
tarballs or Manager systemd binaries. `install.sh` renders the production
Compose model, pulls every exact image from `docker.cnb.cool/ongridio/ongrid`,
then runs `docker compose up -d`. Before changing the served `/edge` tree, the
installer downloads checksum-verified third-party dependencies and the current
`ongrid-edge` binary directly from CNB Release attachments. Set
`ONGRID_BUNDLE_EDGE_ASSETS=1` only for an intentionally offline package.

## Release flow

1. Bump the version in `VERSION`, commit the change, and push the matching tag
   to GitHub. The `Release` GitHub Actions workflow publishes runtime images,
   the Helm chart, and the universal thin Linux server package.
   For release validation, use a matching prerelease such as
   `v0.14.0-rc.1`; it is marked as a prerelease on GitHub and CNB. Publish
   `v0.14.0` after validation instead of deleting the candidate artifacts.
2. The `Release` GitHub Actions workflow runs on `v*.*.*` tag pushes and
   publishes the multi-architecture manager, Web, and Kubernetes Edge images
   plus the matching Helm chart before building the universal server package. The chart is published as an
   OCI artifact at `oci://helm.cnb.cool/ongridio/ongrid-edge`; it is not copied
   into the manager installation tarball. The release build will:
   - `docker-push-release-images` — publish manager, Web, and Edge amd64/arm64 images to CNB
   - `verify-release-images` — verify both architectures exist on all three image manifests
   - `publish-k8s-chart` — package and publish the version-matched Helm chart
   - `package` — stage the thin Compose install assets without Edge binaries
   - stage everything under `dist/stage/ongrid-<VERSION>-linux/`
   - emit one architecture-neutral tarball plus its sha256 file under `dist/out/`
3. The `edge-release` job verifies that the immutable shared dependency
   Release is complete, creates the version-matched CNB Release in the
   lightweight `ongridio/ongrid-edge` repository, and uploads only the two
   self-developed `ongrid-edge` binaries plus their checksums. The final
   GitHub Release waits for this job, so it cannot publish with missing Edge
   downloads. Configure the GitHub Actions `CNB_TOKEN` secret with registry
   and Helm access plus CNB `repo-release:rw` and `repo-contents:rw` scopes.

   Shared dependencies are not rebuilt on every release. Only when
   `make edge-deps-tag` changes or the corresponding immutable Release is
   absent, publish them once:

   ```bash
   CNB_TOKEN=... make publish-edge-deps-attachments
   ```

   The dependency Release contains two archives and is reused across Ongrid
   versions. The version Release contains only two `ongrid-edge` binaries and
   their checksums. Both publishers are idempotent: complete Releases are
   reused and partially populated immutable Releases fail closed.
4. Ship the universal package, for example:
   `scp dist/out/ongrid-v<VERSION>-linux.tar.xz user@host:~/`.
5. On the target: untar, `sudo ./install.sh`.

On a fresh install, `install.sh` maps `x86_64/amd64` to `linux-amd64` and
`aarch64/arm64` to `linux-arm64`, then downloads only the matching Edge
attachments. Upgrades preserve the already installed Edge target set unless
`ONGRID_EDGE_TARGETS` explicitly overrides it.

## Checksum

`dist/out/ongrid-v<VERSION>-linux.tar.xz.sha256` sits next to the
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
- Edge public dependencies are immutable CNB Release attachments. Their tag
  changes only when the component versions or archive layout changes.
