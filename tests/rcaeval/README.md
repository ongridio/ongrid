# RCA evaluation corpus

Run the real v3 investigator, its agent personas, SQLite session/report stores,
structured extraction, and evidence checks against synthetic observations. No
Manager, Edge, Docker, or production data is needed. Only `live` mode contacts
the configured model endpoint.

## Run

From the repository root:

```sh
make test-rca-eval
go run ./tests/rcaeval -mode smoke -out /tmp/rca-smoke
```

The eight smoke scripts verify pipeline wiring. They are **not model evaluations**.
They run in normal `go test ./...` / CI without a key. Tests cover query scope,
failed and mixed-device observations, real specialist sessions, cancellation,
shared budgets, rubric isolation, and review/result binding.

Before changing a scenario or using it for optimization, read the
[scenario facts and acceptance rules](../../docs/design/rca-evaluation-scenarios.md).
The corpus checks run real tool executors against fixed backends, compare wire
responses, and reject contradictory labels, timestamps, batch counts, and smoke
references. Host responses use production wire DTO checks. Run `make test-rca-eval`
before a paid evaluation; a successful scripted run alone is not corpus validation.

For a real model, set `RCA_EVAL_API_KEY` in the process environment. Existing
`ONGRID_OPENAI_*` and `OPENAI_*` variables are also accepted, in that order.
Never pass a key on the command line or put it in a case file.

```sh
export RCA_EVAL_MODEL=deepseek-flash
export RCA_EVAL_BASE_URL=https://api.deepseek.com/v1

# First measure one case. Use a new output directory each time.
go run ./tests/rcaeval -mode live -case cpu_process \
  -max-tokens 100000 -out /tmp/rca-pilot

# Then run 8 cases × 3 repetitions, sequentially.
go run ./tests/rcaeval -mode live -repeat 3 \
  -max-tokens 100000 -batch-tokens 2400000 -out /tmp/rca-baseline
```

Each run has a fresh SQLite database. The output directory must not exist;
existing results are never overwritten by an evaluation. Ctrl-C cancels active
model work and keeps completed results. Exit code 1 means a coverage, execution,
budget or harness issue needs attention; it is not a diagnostic accuracy score.

## Cases and query matching

| ID | Review focus |
|---|---|
| `cpu_process` | Identify the process in the current CPU sample without inventing its history or trigger |
| `memory_oom` | Correlate OOM with memory pressure; do not assert a proven memory leak |
| `memory_unproven` | Separate memory growth from OOM/leak hypotheses |
| `disk_log_growth` | Identify growing log files without claiming recovery |
| `downstream_dependency` | Follow a sampled request's failing dependency; retain specialist log evidence |
| `query_timeout` | Treat failed queries as missing knowledge, not negative findings |
| `mixed_devices` | Keep device 42's success separate from device 43's failure |
| `stale_logs` | Do not attribute yesterday's OOM to today's memory incident |

Case files contain incident metadata, argument/result fixtures, a review rubric,
and a separate scripted smoke conversation. Live models receive only the normal
investigation prompt, tool schemas, and results of queries they actually make.
Neither the rubric nor the smoke replies enter live model requests.
Smoke replies must use tools actually offered to their current agent. The
specialist example keeps Trace lookup in the investigator and log lookup in SRE,
matching the existing persona permissions.

Tools use current production `Info` schemas. Executors are replaced with a fixed
fixture map. JSON object key order and explicitly documented defaults normalize;
device selectors, expressions, time windows, array order and unknown fields do
not. Each `args` entry explicitly declares one supported query. Returned metric
samples must lie in that query's range. Relative metric ranges use `incident.at`
as the corpus clock; fixed process snapshots also describe that instant.

For `memory_oom` and `query_timeout`, unmatched log queries also use the real
log tool with a local backend that filters only the case's existing log rows.
This requires the incident device ID and explicit RFC3339 start/end times.
Supported syntax is an equality/regexp stream selector with at most one literal
(`|=`) or regexp (`|~`) line filter. Time, labels and case-sensitive filters still
apply; valid timeout queries remain failures. Unsupported syntax or scope remains
a coverage gap. This is bounded observation replay, not a general LogQL engine.

Unsupported queries return `fixture_coverage_gap`, never fabricated success or
an empty result. A run with any gap is unscorable. Inspect its actual query, add
a justified observation/variant, and start a new corpus version. Do not label
the model wrong because the corpus was incomplete. Do not add expected answers
to incident descriptions to make the model choose a preferred query.

The catalog is a declared subset of production read tools. It does not simulate
PromQL/LogQL generally, external MCP, skills, real query performance or collection
failures outside the fixtures. Production tool memoization and persona filters
still apply. The harness explicitly enables the AgentTool gate in its isolated
bag to exercise delegation; it has no infrastructure write executors. This is a
test profile, not a claim that auto-RCA enables delegation in every deployment.

## Results and review

- `manifest.json`: Git commit, hashes of actual source/case/prompt files (including
  uncommitted work), requested model, endpoint, parameters, budgets, completion.
- `report.md`: run table, review state, and optional baseline comparison.
- `<case>-<repeat>/sessions.sqlite`: durable sessions, tool calls and report.
- `case.json`: the exact incident, observation fixtures and rubric used by that run.
- `sessions.json`: readable main/specialist transcript and session relationships.
- `model_calls.json`: all model inputs/outputs, phase, session, latency and usage.
- `result.json`: generated report, provenance issues, tool requests/executions,
  repeated queries, missing observations and rubric.
- `review.md` / `review.json`: evidence to inspect and the human review form.

Execution status is `awaiting_review`, `fixture_gap`, `model_error`,
`pipeline_error`, or `budget_exceeded`. Provenance issues are shown independently.
An unsupported/empty/failed citation is a review finding; it does not by itself
prove that an uncertain diagnosis is wrong. Every recorded source is read back
through the production evidence-detail function and actual investigation tree.

Reviewers inspect the narrative, structured conclusion, raw observations and
rubric. Set `verdict` to `pass`, `fail`, or `unscorable`; fill `reviewer`, `notes`,
and all four dimensions with `yes`/`no`/`uncertain`. A pass requires four yeses.
Coverage/execution failures can only be marked unscorable. Pending reviews remain
pending; the program never converts keyword matches or model self-confidence
into diagnostic accuracy. Reviews are bound to the exact result hash.

```sh
go run ./tests/rcaeval -mode report -out /tmp/rca-baseline
go run ./tests/rcaeval -mode report -out /tmp/rca-candidate \
  -compare /tmp/rca-baseline
```

Comparison pairs case ID + repetition and rejects changed case hashes or
smoke/live mismatches. Change one variable per experiment. Model aliases can
change provider-side; source hashes do not make live inference deterministic.
Eight synthetic cases and three repeats do not estimate production accuracy.

## Budget limits

The default hard limits are 16 model requests, 24 tool executions and five minutes
per case. Specialists and extraction share the same model budget. All requested
tool calls are counted separately, including memoized or blocked calls. Model
requests are serialized within a case for predictable token accounting.

The default reported-token stop thresholds are 60,000 per case and 200,000 per
batch. They stop **subsequent** requests/cases. One reply/case can overshoot; these
are not hard billing caps. A provider that omits usage makes token totals
incomplete, not zero-cost. The report does not invent prices or costs. Tool
failure messages follow the real graph adapter (a deadline may persist as an
`error` result containing `context deadline exceeded`).

All shipped data is synthetic. Local output files have restrictive permissions.
Only add real incidents after redaction and a separate data review.
