# Saved tool results

Ongrid can keep large chat tool results in a private persistent directory, compress text/JSON in independent Zstd frames, and give the model a bounded preview with read/query tools. Capture is opt-in. Existing saved results remain readable when capture is disabled. This backend supports one Manager per result directory.

The [design](../design/tool-result-storage.md) describes the contracts and deferred work. This change covers v2/v3 chat, workers, MCP capture and chat approvals. It does not remove existing Edge/Shell capture limits or convert workflow and report payloads to references.

## Configure storage and budgets

| Setting | Default | Meaning |
| --- | --- | --- |
| `ONGRID_TOOL_RESULTS_ENABLED` | `false` | Capture new tool results; exact value `true` enables capture and maintenance |
| `ONGRID_TOOL_RESULTS_DIR` | `/var/lib/ongrid/tool-results` | Manager path; Compose mounts this fixed container path |
| `ONGRID_TOOL_RESULTS_MAX_BYTES` | `10737418240` | Directory file-byte quota, minimum 128 MiB; excludes SQL inline data |
| `ONGRID_LLM_CONTEXT_WINDOW` | `131072` | Conservative total token budget for each v2/v3 model request |
| `ONGRID_LLM_OUTPUT_TOKENS` | `8192` | Reserved output tokens, also sent to the provider |

Use a context window no larger than the smallest model that users can select. These LLM limits apply even with capture disabled. Input estimation uses serialized UTF-8 bytes, including schemas and reasoning data; it is conservative and may reject large image inputs. After removing old evidence, requests that still exceed the budget fail before reaching the provider.

The installer Compose file mounts `${ONGRID_DATA_DIR:-/var/lib/ongrid}/tool-results` from the host. The development Compose file mounts `../.cache/ongrid-tool-results`. Existing installer permission repair creates the directory for UID/GID 65532. For a manual development deployment, create the bind-mount directory with that owner before starting the non-root Manager. Do not place this directory under nginx's web root or expose it as a static download directory.

The quota counts compressed payload, index and manifest file sizes. It is not a filesystem reservation. Keep at least 73 MiB free for write preflight and allow more headroom for concurrent processes and filesystem overhead. Small results up to 32 KiB remain in SQL; results of at least 1 MiB use compression when they are text or JSON. A captured result is limited to 64 MiB; the MCP response limit is 16 MiB.

## Upgrade and enable

1. Back up the database and result directory together. For a consistent local backup, stop Manager writes while taking both copies. Also preserve the deployment's existing encryption/key configuration securely; never place secrets in this directory.
2. Upgrade to the reference-aware version with capture disabled. Startup AutoMigrate adds result tables, nullable result associations and LONGTEXT capacity for the affected result fields and MCP tool catalog. On MySQL, assess large-table DDL and `max_allowed_packet` before the maintenance window; the MCP catalog can reach 16 MiB plus SQL framing. Production MySQL DDL has not been exercised by the local SQLite tests.
3. Keep old Manager processes from running their TEXT-based AutoMigrate during the upgrade. The capture flag does not disable schema migration. Do not mix this schema change with an old process that may shrink columns.
4. Verify the result volume is persistent and writable by the Manager, then set `ONGRID_TOOL_RESULTS_ENABLED=true` in the deployment's `.env` and recreate the Manager service using the usual installer/Compose procedure. No additional service is required.
5. In an isolated chat, run a representative large read, search a tail record, and download the captured result. Verify the bytes or checksum against the source. Restart the Manager and repeat the read. Check both allowed and unrelated-session access.
6. Before broad rollout, test the actual MCP/provider, approval path, MySQL restart/upgrade, disk capacity, joint backup restore and expected concurrency. Local regression tests do not establish production capacity.

The model uses `read_tool_result` and `query_tool_result`; the console's saved-result dialog uses the authenticated session API. JSON array output uses a `data_json` string so large integer IDs retain their exact digits. Search counts cover only the displayed scan interval. Partial or expired captures must not be presented as complete evidence.

## Recovery and retention

Execution, capture and storage have separate status fields. A tool may have completed even if storing its result failed. Do not repeat a write to retrieve its output.

File publication precedes SQL metadata. If SQL fails after publication, the manifest and captured bytes remain available for recovery. With capture enabled, the Manager runs recovery and cleanup at startup and every five minutes. Recovery publishes up to 100 new records per pass; maintenance has a 30-second timeout. It also repairs linked chat approval results without executing the approval again. Investigate persistence/recovery errors in the Manager logs and check database health, directory permissions, free space and quota.

Recovery currently scans the local tree. If one corrupted manifest stops a pass, preserve its files for diagnosis and restore a verified copy from backup; do not blindly delete evidence or rerun the tool. Scale testing and a dedicated pending-work queue are required before using this backend for a very large result population.

Ordinary results expire after 30 days. Chat approval references retain results for 90 days. Reads enforce expiration even before file cleanup. Cleanup retains a metadata tombstone and removes unpinned payloads. Automatic report/RCA retention references are not yet wired. Disabling capture also stops maintenance; monitor disk usage if leaving it disabled for a long period.

Back up the result directory and SQL together, and validate a restore with checksums. Saving only the database is insufficient for file-backed results. Compressing files does not reduce model context consumption; only bounded previews, queries and per-request history selection do that.

## Roll back

Set `ONGRID_TOOL_RESULTS_ENABLED=false` to stop new captures. Keep the reference-aware code, result tables, LONGTEXT columns and volume so existing results can still be read. Context budgets remain active and can be adjusted to the model's supported limits.

An arbitrary older binary is not a safe rollback target: it may not understand references and may run TEXT migrations. Do not shrink columns or delete the result volume as part of rollback. Returning to a version without reference support requires a separate data-conversion plan; results beyond its inline capacity cannot be restored losslessly to that format.
