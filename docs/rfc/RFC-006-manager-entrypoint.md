# RFC-006: Manager entrypoint decomposition

Status: implemented.

## Scope

Split `cmd/ongrid/main.go` without changing routes, configuration defaults,
database migrations, tool inventories, approval rules or background schedules.
The command remains the composition root for IAM and Manager. Existing package
boundaries and constructor injection remain in use; no DI framework is added.

## Structure

- `main.go` owns the linker-injected version and process exit status. Its
  `run()` function lists startup stages in their existing order and owns cleanup.
- `bootstrap_*.go` construct related dependencies; `http.go` mounts the existing
  public, protected and internal routes; `workers.go` starts existing workers.
- Page file storage, sharing rules and HTTP handlers belong to Manager's data,
  biz and server packages respectively.
- AIOps approval/runtime adapters belong to `service/aiops`; workflow adapters
  belong to `service/flow`, with pure argument coercion in `biz/flow`.
- IM provider adapters belong to `service/imbridge`, keeping provider imports
  out of the AIOps service and avoiding the existing IM-to-AIOps dependency cycle.
- Kubernetes maintenance loops belong to `service/k8s`.
- Kubernetes action audit aggregation belongs to `service/k8s`; its sanitized
  read model belongs to `model/k8s`, with a server type alias retaining callers.
- APM/agent receiver access, report notification delivery and WebSSH recording
  belong to data adapters. The infrastructure-menu upgrade seed stays with
  setting storage and retains its existing-install gate and insert-only rule.
- Log overlays and reload broadcasts belong to `service/logs`; shared telemetry
  endpoint selection belongs to `service/telemetry`; data-plane authentication
  adapters belong to `server/edgeauth`.
- Presence, topology and metrics loops, initial integration syncs and MCP tool
  refreshes live with their owning modules. The command retains their original
  launch order, optional guards, cancellation and shutdown coordination.
- Live model selection belongs to the settings resolver and `pkg/llm`; runtime
  skill catalog projection belongs to `service/skill`. Type-only bridges remain
  beside their bootstrap constructors; there are no separate command adapters.

Do not move the composition root into Manager or shared infrastructure: it
imports both IAM and Manager. Service adapters consume repository interfaces,
not concrete data implementations. Keep late tool registration and nil guards
in their existing order. Startup errors keep their existing log messages and
exit status. Shutdown hooks retain their reverse acquisition order on normal
return and continue after a hook panics, matching separate Go defers; fatal
errors still exit without running those hooks.

## Validation and rollback

Establish a command-package race-test baseline. After extraction, run affected
packages with `-race`, the full Go suite, the Manager build and architecture
checks. Cover page compatibility/authentication/sharing, workflow coercion,
approval execution, credential fallback order, MCP catalog refresh, upgrade
seed idempotence and cleanup. Use an isolated SQLite startup smoke test for
route availability, version injection, disabled integrations and termination.
No running deployment or existing database is changed by this refactor.
Rollback is the code revert and a normal rebuild; no data rollback is required.

Validation completed: affected-package race tests, Linux `make test` and
`go vet ./...`, and `make build-ongrid`. Isolated before/after processes matched
API payloads, workflow tool catalogs and SQLite schemas with no model provider,
the Graph kernel and the legacy kernel. Login, page sharing/deletion, SIGTERM
shutdown and invalid-configuration exit status were checked.
Six isolated MySQL E2E cases also passed: login/self, skill inventory, workflow
catalog/CRUD/execution, workflow page hosting, MCP tool dispatch and MCP
credential injection. Cleanup tests cover both normal return and a panicking
shutdown hook.

The macOS full suite retains three baseline failures in Linux-only Edge paths
and the upgrade path test. `make arch-lint` fails on the unchanged configuration
(a nonexistent `edgeagent/data` component and empty dependency declarations),
including on the baseline; new domain files were separately checked for direct
import boundary violations. Live integrations were not exercised.
