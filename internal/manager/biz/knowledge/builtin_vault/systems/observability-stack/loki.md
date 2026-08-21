---
title: Loki — How It Actually Works
tags: [loki, logs, observability]
---

# Loki — How It Actually Works

Loki is "Prometheus for logs" by design: it indexes only **labels**
(not log content), so storage stays cheap. The query language (LogQL)
mirrors PromQL. The trade-off you must internalize: **bad labeling
turns Loki into a slow grep**.

## Architecture

Single-binary mode (default for small deploys) bundles all components in
one process. Microservices mode splits them:

- **distributor** — receives writes, hashes by stream, fans out to ingesters
- **ingester** — buffers in memory, periodically flushes chunks to object store
- **query-frontend** — splits queries, caches results
- **querier** — fans queries across ingesters + object store
- **compactor** — merges and dedupes chunks in the background

Storage:

- **Chunks** (the log content) — sit in object storage (S3 / GCS /
  filesystem)
- **Index** — maps labels → chunks. Modern Loki uses TSDB-style
  index files in object storage too (no Cassandra / BoltDB-shipper
  legacy unless you inherited an old install)

## A "stream" is the unit of work

A stream is the set of log lines sharing the same label set, e.g.
`{job="api", level="error", pod="api-1"}`. Loki indexes the stream's
labels and stores the line bodies in chunks ordered by timestamp per
stream.

Why this matters:

- **Adding a label to a stream creates a new stream.** A new pod name
  → new stream → new chunk lineage. Stream churn is expensive.
- **High-cardinality labels are catastrophic.** `request_id` as a label
  produces a stream per request — the index blows up and queries
  degrade catastrophically.
- **Lots of low-cardinality labels are fine.** That's the design.

Targets:

| Item | Healthy | Yellow | Red |
|------|---------|--------|-----|
| Active streams per tenant | < 100k | 100k – 500k | > 500k |
| Streams per label combination | bounded | — | unbounded |

## LogQL — two halves

### Log queries (return log lines)

```logql
{job="api"} |= "error"                    # contains
{job="api"} != "healthcheck"              # does not contain
{job="api"} |~ "(?i)timeout|deadline"     # regex
{job="api"} | json | level="error"        # extract JSON, filter on field
{job="api"} | logfmt | latency > 1s       # extract logfmt, numeric filter
```

The order of pipeline stages matters: `|= "error"` is much cheaper than
`| json | level = "error"` because it filters on the raw bytes before
parsing.

### Metric queries (return number series, exactly like PromQL)

```logql
rate({job="api"} |= "error" [5m])
sum by (status) (count_over_time({job="api"} | json [1m]))
```

These drive Loki-backed alert rules and dashboards. The trick: a metric
query that fans out across millions of lines becomes a giant batch job.
Tighten the label selector first, *then* the line filter, *then*
parsers.

## Cost model — what you pay for

- **Ingest bandwidth** — bytes received
- **Storage** — chunks in object store
- **Query** — CPU and memory at query time; large time ranges over
  high-volume streams need lots of both
- **Index size** — driven by stream cardinality

Most operators are surprised that Loki *doesn't* charge for log volume
within a stream — it's the **number of streams** that bites.

## Configuration knobs that matter

```yaml
limits_config:
  ingestion_rate_mb: 10            # per-tenant rate (MB/s)
  ingestion_burst_size_mb: 20
  max_streams_per_user: 100000     # cap stream cardinality
  max_query_length: 720h           # max query window
  max_query_parallelism: 32        # querier shard count
  retention_period: 168h           # 7 days
```

Per-tenant overrides go in `overrides.yaml`. The two limits that produce
"runs fine until..." pages: `max_streams_per_user` (label drift) and
`max_query_length` (someone runs a 30-day query).

## OTel Collector / Alloy / Vector — the agents

Loki itself doesn't read log files; an agent does. Options:

- **OpenTelemetry Collector Contrib** — vendor-neutral receivers,
  processors, persistent queues, and OTLP/Elasticsearch exporters
- **Grafana Alloy** — replaces Promtail; superset that also handles
  metrics and traces
- **Vector / Fluent Bit** — third-party; richer transform pipeline,
  multi-sink

Ongrid edge runs a bundled `otelcol-contrib` subprocess as the `logs`
plugin. For built-in Loki the data plane is Edge → nginx OTLP endpoint
→ Loki; for an external backend it is Edge → Elasticsearch directly.
Log payloads never traverse the control tunnel or Manager Go process.

## Operational signals

- `cortex_distributor_ingester_appends_failures_total` → ingester
  overload
- `cortex_ingester_memory_chunks` → chunks waiting to flush
- `loki_request_duration_seconds` p99 by route → query latency
- `loki_distributor_lines_received_total` — input volume
- `loki_ingester_streams_created_total` rate → stream churn (the early
  warning that someone added a high-cardinality label)
