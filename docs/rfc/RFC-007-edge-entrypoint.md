# RFC-007: Edge entrypoint decomposition

Status: implemented.

## Scope

Keep `cmd/ongrid-edge` responsible for command parsing, configuration, dependency
assembly, process signals and exit status. Move runtime implementations to their
existing Edge owners or focused subpackages, without changing CLI arguments,
environment defaults, enrollment payloads, credential formats, permissions,
plugin registration order or telemetry configuration.

## Structure

- `main.go` retains `main()` and `run()` together. The command still dispatches
  subcommands and help/version before loading configuration. It owns the root
  cancellation and errgroup; an agent return, including a successful upgrade,
  still cancels sibling workers before waiting for shutdown.
- `bootstrap_collector.go` and `bootstrap_plugins.go` assemble the existing
  collectors and plugins. The collector type bridge remains beside its wiring.
  Platform paths, capability registration, data-plane mode selection and the
  already thin Kubernetes upgrade command remain in `cmd/ongrid-edge`.
- `internal/edgeagent/k8s` owns cluster enrollment, persisted credentials,
  telemetry Secret updates and application metrics scope loading. The command
  passes the linker-provided version explicitly. Identity discovery precedes
  credential loading; bootstrap fallback and required/optional telemetry rules
  remain unchanged.
- `k8s/dataplane` owns gateway and scraper lifecycles, projected configuration,
  remote-write transport replacement, diagnostics and Pod metrics rendering.
  These modes retain their fixed diagnostic port and do not start a tunnel.
- `k8s/hostruntime` owns atomic runtime installation, legacy link cleanup, host
  namespace entry and capability retention. CLI parsing remains in the command.
  Linux build tags, locked-thread operations, securebits, the capability allowlist
  and unsupported-capability filtering remain unchanged.
- `enrollment` owns ordinary host enrollment, response validation and the atomic
  private environment file. The command removes the bootstrap token from its
  environment before calling it and retains the success/error output.
- `plugins` owns heartbeat wire projection and reload notifications. `plugins/traces`
  owns the receiver RPC; a narrow consumer interface injects instance resolution
  from Auto APM without introducing a package cycle. Zero health timestamps stay
  zero, and only a running traces plugin exposes a receiver.
- `supervisorhealth` owns the Windows heartbeat loop and the non-Windows no-op.
  Shared Edge environment parsing lives in `edgeagent/config` with the same
  accepted aliases and fallback rules.

No dependency, deployment manifest, API schema or Manager behavior changes.
The repository-wide `CODEOWNERS` rule covers the new directories.

## Validation and rollback

Move implementation tests with their packages. Update `test-k8s-capabilities`
and `test-k8s-autoapm` to compile the host-runtime test binary at its new path.
Verify affected packages with the race detector, the full Linux Go suite and
vet, Windows cross-build/vet, and the privileged capability regression in an
isolated Linux container. Compare baseline and candidate CLI/startup/shutdown
behavior using temporary configuration and no live Manager or Kubernetes access.

Rollback is a code revert and rebuild. No data migration or credential rewrite
is introduced. Native Windows SCM/DPAPI and live Kubernetes/OBI acceptance are
separate from cross-compilation and isolated capability tests.

Validation completed: affected-package race tests, Linux `make test` and
`go vet ./...`, Windows amd64 build and vet, and `make test-k8s-capabilities`
(24 capability/UID cases). Baseline and candidate binaries matched seven CLI
cases and six lifecycle cases: host, node, controller, gateway, scraper with
remote-write, and scraper waiting for projected credentials. Health/readiness,
metrics delivery and SIGTERM exit were checked against local fake services.
After normalizing package qualifiers and exported names, 93 existing helper
function bodies were identical; the remaining extractions and command wiring
were reviewed separately.

`make arch-lint` remains blocked by the unchanged repository configuration:
`edgeagent/data` does not exist and the shared/API dependency lists are empty.
The new Edge files were checked for cross-domain imports separately. No live
Manager, Kubernetes cluster or installed Edge was changed during validation.
