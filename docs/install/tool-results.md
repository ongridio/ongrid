# Saved tool results

Ongrid can keep large chat tool results in a private persistent directory, compress text/JSON in independent Zstd frames, and give the model a bounded preview with read/query tools. Result capture, recovery and cleanup are always enabled; Linux Managers also enable analysis. There is no feature toggle. This backend supports one Manager per result directory.

The [design](../design/tool-result-storage.md) describes the contracts and deferred work. This change covers v2/v3 chat, workers, MCP capture and chat approvals. It does not remove existing Edge/Shell capture limits or convert workflow and report payloads to references.

## Configure storage

| Setting | Default | Meaning |
| --- | --- | --- |
| `ONGRID_TOOL_RESULTS_DIR` | `/var/lib/ongrid/tool-results` | Manager path; Compose mounts this fixed container path |
| `ONGRID_TOOL_RESULTS_MAX_BYTES` | `10737418240` | Directory file-byte quota, minimum 128 MiB; excludes SQL inline data |

Saved-result previews and read/query pages have byte limits. This feature does not add a model-request context budget, trim conversation history by token size, or set a new output-token limit. Existing history-count, iteration and daily-token limits remain in place; the provider enforces its actual context capacity.

The installer Compose file mounts `${ONGRID_DATA_DIR:-/var/lib/ongrid}/tool-results` from the host. The development Compose file mounts `../.cache/ongrid-tool-results`. Existing installer permission repair creates the directory for UID/GID 65532. For a manual development deployment, create the bind-mount directory with that owner before starting the non-root Manager. Do not place this directory under nginx's web root or expose it as a static download directory.

The quota counts compressed payload, index and manifest file sizes. It is not a filesystem reservation. Keep at least 73 MiB free for write preflight and allow more headroom for concurrent processes and filesystem overhead. Small results up to 50 KiB remain in SQL; results of at least 1 MiB use compression when they are text or JSON. A captured result is limited to 64 MiB; the MCP response limit is 16 MiB. Known Shell/Edge output truncation marks new captures as `partial`: the saved bytes remain readable, but full-result analysis is rejected. Bytes already discarded by the source cannot be recovered; existing capture metadata is not backfilled.

## Upgrade

1. Back up the database and result directory together. For a consistent local backup, stop Manager writes while taking both copies. Also preserve the deployment's existing encryption/key configuration securely; never place secrets in this directory.
2. Verify the result volume is persistent and writable before starting the upgraded Manager. Startup AutoMigrate adds result tables, nullable result associations and LONGTEXT capacity for the affected result fields and MCP tool catalog. On MySQL, assess large-table DDL and `max_allowed_packet` before the maintenance window; the MCP catalog can reach 16 MiB plus SQL framing. Production MySQL DDL has not been exercised by the local SQLite tests.
3. Keep old Manager processes from running their TEXT-based AutoMigrate during the upgrade. Do not mix this schema change with an old process that may shrink columns.
4. Recreate the Manager service using the usual installer/Compose procedure. No additional service is required.
5. In an isolated chat, run a representative large read, search a tail record, and download the captured result. Verify the bytes or checksum against the source. Restart the Manager and repeat the read. Check both allowed and unrelated-session access.
6. Before broad rollout, test the actual MCP/provider, approval path, MySQL restart/upgrade, disk capacity, joint backup restore and expected concurrency. Local regression tests do not establish production capacity.

The model uses `read_tool_result` and `query_tool_result`; the console's saved-result dialog uses the authenticated session API. JSON array output uses a `data_json` string so large integer IDs retain their exact digits. Search counts cover only the displayed scan interval. Partial or expired captures must not be presented as complete evidence.

For successful MCP queries, the model can answer directly from the complete `structuredContent` when it and the saved-result reference fit in the 4,000-byte tool response. This inline view can include the MCP display text when space permits. The original response remains available by `result_id` for exports and follow-up queries. A text-only MCP response can also pass through directly when its decoded text fits.

The excerpt view lists arrays in `collection_pointers`, strings in `text_pointers`, and native MCP objects, numbers, booleans and nulls in `value_pointers`. Use `json_value` to analyze one of these values. If `more_indexed_paths` is true, call `read_tool_result` with `mode="structure"` and follow `next_cursor` for more indexed paths. If `path_index_partial` is true, the index may omit paths. A terminal structure page with `scan_complete=false` explains the limit and has no next cursor. For earlier file-backed JSON results, structure mode rebuilds the inventory on read when the payload fits the 8 MiB scan limit. Larger historical results expose their saved paths and a notice. Byte reads and analysis at known paths still use the original payload.

| Saved data | How to select it |
| --- | --- |
| MCP business records in a JSON array | Use the matching `/structuredContent/…` path. Both tools accept `pointer`. The reader also accepts `collection_pointer` and infers JSON mode. |
| MCP display blocks | Select `/content` to read or count the blocks. A single block can describe many business records. |
| JSON encoded in a string | Use analysis format `json_text`, set `pointer` to the string, and set `collection_pointer` to the array inside the decoded JSON. The corresponding `text_pointer` and `rows_pointer` from the saved receipt are accepted aliases. |
| Single JSON object or scalar | Use analysis format `json_value`. It emits one record and applies the 1 MiB record limit. |

## Complete lists and HTML exports

For a complete list from a read-only MCP query with a cursor, page number or offset in its input schema, the chat tool accepts `ongrid_all_pages`. Set `rows_pointer` to the business array inside `structuredContent` or the JSON in a single text block. Supply `total_pointer` and `next_pointer` for nonstandard response fields, and `id_pointer` when rows have a unique key. The Manager keeps the original query filters on every page. Tools that require approval show the all-pages request as one approval. Single-page calls use the upstream tool as usual.

The saved aggregate exposes `/structuredContent/rows`. Page and offset queries require an upstream total. Cursor queries follow the cursor to its end and verify a supplied total. Missing rows, repeating cursors or pages, and upstream failures produce partial evidence. One collection runs for up to a minute, with at most 100 pages, 100,000 rows and 16 MiB of business response data.

For a saved inventory, pass the model-authored `html` and its `table` source in the same `serve_page` call. The model writes the page, its CSS, the headers and one example of the repeated layout. Each record can be a table row, list item or card. For example, this document renders one card per selected host and displays the verified count.

```json
{
  "title": "Inventory",
  "html": "<!doctype html><html><head><title>Inventory</title><style>.asset{border-inline-start:3px solid teal;padding:1rem}</style></head><body><h1>Inventory</h1><p>Hosts: <strong data-ongrid-count='exported_rows'></strong></p><section><article class='asset' data-ongrid-row><h2><span data-ongrid-field='/hostname'></span></h2><p>Department: <span data-ongrid-field='/department'></span></p></article></section></body></html>",
  "table": {"result_id":"<saved-result-uuid>","rows_pointer":"/structuredContent/rows","filters":[{"pointer":"/department","op":"not_equals","value":"Retired"}]}
}
```

Place the repeatable element alone in its container, e.g. a `tr` inside a `tbody`. Empty cells or spans with `data-ongrid-field` receive the saved values as escaped text. Use `data-ongrid-count` for source, excluded or exported row totals outside the repeated layout. The tool verifies these bindings and counts. The author supplies the selection criteria, headings and surrounding prose. If the current chat run has received a recognized saved collection, `serve_page` also requests a source when called with an unbound HTML table, article or list. Independent prose pages still accept plain HTML. Stored table-only calls retain the earlier layout for compatibility.

The page tool reads the authorized session result, applies all filters and returns the page link with source, excluded and exported row counts. For JSON inside an MCP text block, use `text_pointer` (such as `/content/0/text`) and a row pointer relative to the decoded text. A reported upstream total or an explicit `expected_rows` is compared with the number of source rows before filtering. Without upstream pagination metadata, the counts cover the selected saved array. A table accepts up to 32 columns, 16 MiB of input and 16 MiB of rendered output. Pages without a saved data source can still use `html` alone.

For a successful MCP result, a bounded excerpt can include `table_source` for a unique array of objects in native structured content or a single JSON text block. It includes the row count of this capture and field pointers observed in the first three rows. Captures up to 1 MiB with at most 2,000 rows can also include exact counts for selected low-cardinality text fields. Each distribution describes its own field within the saved page. Filtering and export evaluate the actual intersection on the original records.

For a unique row array, `read_tool_result` and `query_tool_result` in `rows` mode and `serve_page.table` resolve the business rows from `result_id`. When several arrays are possible, select a candidate using `rows_pointer` and, for encoded text, `text_pointer`. For example, this query counts each status and region among matching rows and returns a small sample.

```json
{"result_id":"<saved-result-uuid>","mode":"rows","filters":[{"pointer":"/team","op":"equals","value":"Platform"}],"group_by":["status","region"],"fields":["id","status"],"limit":2}
```

Row queries verify declared upstream pagination and accept up to 8 MiB of saved input. The sample is bounded to five rows and the tool response budget. A group with more than eight distinct values, long or structured values, or insufficient response space has `values_omitted` set. Missing fields are counted separately from null. Exact `matched_rows` is independent of whether samples or distributions fit. Use analysis for custom calculations or joins.

All filters must pass. Use `or_pointers` when a keyword may occur in several fields, and `ignore_case` for case-insensitive comparisons. For example, the following source specification for a model-authored page selects rows with “svn” in either the hostname or the description, excluding rows where either ownership field contains “Example Team”.

```json
{
  "result_id": "<saved-result-uuid>",
  "text_pointer": "/content/0/text",
  "rows_pointer": "/data",
  "filters": [
    {"pointer": "/hostname", "or_pointers": ["/description"], "op": "contains", "value": "svn", "ignore_case": true},
    {"pointer": "/owner", "or_pointers": ["/department"], "op": "not_contains", "value": "Example Team"}
  ]
}
```

In a filter with alternatives, missing and null values do not match. On a nonempty source, each specified alternative must exist on at least one row. All filters together can reference up to 32 fields.

## Recovery and retention

Execution, capture and storage have separate status fields. A tool may have completed even if storing its result failed. Do not repeat a write to retrieve its output.

File publication precedes SQL metadata. If SQL fails after publication, the manifest and captured bytes remain available for recovery. The Manager runs recovery and cleanup at startup and every five minutes. Recovery publishes up to 100 new records per pass; maintenance has a 30-second timeout. It also repairs linked chat approval results without executing the approval again. For completed approvals, recovery refreshes a matching pending storage view while preserving execution status, time and preview. Investigate persistence/recovery errors in the Manager logs and check database health, directory permissions, free space and quota.

Recovery currently scans the local tree. If one corrupted manifest stops a pass, preserve its files for diagnosis and restore a verified copy from backup; do not blindly delete evidence or rerun the tool. Scale testing and a dedicated pending-work queue are required before using this backend for a very large result population.

Ordinary results expire after 30 days. Chat approval references retain results for 90 days. Reads enforce expiration even before file cleanup. Cleanup retains a metadata tombstone and removes unpinned payloads. Automatic report/RCA retention references are not yet wired.

Back up the result directory and SQL together, and validate a restore with checksums. Saving only the database is insufficient for file-backed results. Compressing files does not reduce model context consumption; bounded previews, queries and analysis reduce the data sent to the model.

## Roll back

Keep the reference-aware code, result tables, LONGTEXT columns and volume so existing results can still be read. Result capture has no configuration switch.

An arbitrary older binary is not a safe rollback target: it may not understand references and may run TEXT migrations. Do not shrink columns or delete the result volume as part of rollback. Returning to a version without reference support requires a separate data-conversion plan; results beyond its inline capacity cannot be restored losslessly to that format.

## Streaming result analysis

On Linux, the Manager registers `analyze_tool_results` for v2/v3 root chats and workers. The Manager launches its own executable with an internal worker argument; no broker, Docker socket, Python, Node or additional deployment service is needed. Non-Linux Managers keep saved-result reads but do not register analysis because the required memory limit is unavailable.

The model supplies 1–4 `inputs` (result ID, format and JSON pointers) and a jq `expression`. JSON arrays are streamed record by record. `json_text` also streams JSON encoded inside an MCP text string. CSV uses Go's standard parser, including quoted commas and newlines; columns remain strings. `lines` streams plain text. The jq `inputs` iterator yields `{source: result_id, value: record}` once, in source order. For example:

```json
{"inputs":[{"result_id":"<saved-result-uuid>","format":"json","pointer":"/structuredContent/projects"}],"expression":"reduce inputs as $r ({total:0,cost_cents:0}; .total+=1 | .cost_cents+=$r.value.cost_cents)"}
```

The existing result reader verifies compressed blocks and supplies bytes directly to the child through a pipe. The parent does not load the full inputs or base64-encode them. The child retains one decoded record at a time unless the expression retains additional state. No module, environment, filesystem, network or external tool functions are registered. The fixed executable is started through the existing Runner without a shell, with a fresh working directory and a replacement environment that contains no business credentials. This is a restricted data interpreter with process separation, not a general-purpose sandbox or VM boundary against interpreter vulnerabilities.

Limits: 1–4 inputs totaling 64 MiB, 16 KiB expression, 1 MiB per record, 64 KiB output, five seconds per execution, two concurrent executions per Manager and six calls per model run. Linux applies a 384 MiB `RLIMIT_DATA` hard allocation limit, a 256 MiB Go GC target, and disables core dumps before reading input. `RLIMIT_DATA` is not an exact RSS cap; `GOMEMLIMIT` alone is not hard isolation. Failure to apply limits fails closed. The parent enforces timeout/cancellation and reaps the process group. Available input bytes do not guarantee an arbitrary query fits these limits.

Filtering, counts, sums, fixed-cardinality groups and bounded Top N can run with bounded state. `[inputs]`, full sorting, growing sets and joins can retain the dataset; they still need the allocation limit. JSON nesting is capped at 128 and wrapper keys at 4 KiB. Oversized records and malformed/ambiguous input fail explicitly. Use exact integer minor units for money; floating-point arithmetic is not decimal arithmetic.

Outputs include expression values and server-measured coverage (`records_read`, `complete`) for each selected source. These counts measure iterator consumption, not whether the expression logically examined every field. Reading only a prefix yields incomplete coverage. Source parse errors cannot be converted into successful evidence by catching the error in jq. An exit failure, timeout or output truncation is not a complete successful answer. The model must inspect the format and verify its query logic; arbitrary semantic interpretation is not guaranteed.

Expressions, source selections/hashes, output, status and duration use the existing result store. Tool arguments use LONGTEXT through AutoMigrate. Partial, expired, unavailable and unrelated-session inputs are rejected before execution. Derived results keep the ordinary 30-day retention and do not extend source retention. Session deletion does not yet immediately purge tool-result payloads; existing expiration cleanup applies.

When rolling back to a compatible version, retain the result volume, tables and LONGTEXT columns for existing reads. No analysis service needs removal. For reproducible synthetic validation, see `scripts/result-analysis/README.md` and [the acceptance record](../test/tool-result-analysis-20261117.md).
