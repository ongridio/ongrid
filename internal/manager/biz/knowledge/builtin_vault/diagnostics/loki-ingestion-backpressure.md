---
title: Loki Ingestion Backpressure / 429s & Missing Logs
kind: howto
tags: [loki, logs, ingestion, rate-limit, 429, backpressure, observability]
applies_to: [manager]
---

# Loki Ingestion Backpressure / 429s & Missing Logs

Use when logs stop showing up in Grafana, the Edge OTel logs collector
logs `429 Too Many Requests` or `entry out of order`, or Loki memory
grows. Loki pushes back with 429 when a tenant/stream exceeds its rate or
when stream cardinality explodes. **Distinguish rate-limit (429) from
ordering/timestamp rejections from a real ingester problem.**

| Shipper error | Probable cause |
|---|---|
| `429 Too Many Requests` | per-tenant/stream rate or burst limit hit |
| `entry out of order` / `too far behind` | timestamps not monotonic per stream / clock skew |
| `stream limit` / `max streams` | label cardinality explosion (too many streams) |
| connection refused / 5xx | Loki distributor/ingester down or overloaded |

## Step 1 — Where is it failing: shipper or Loki

```bash
# Edge logs subprocess: is the OTel exporter being throttled?
tail -n 100 /var/lib/ongrid-edge/plugins/logs/logs.log 2>/dev/null | grep -iE '429|out of order|error|sending_queue'
# Supervisor/config failures are recorded by the parent service.
journalctl -u ongrid-edge -n 100 2>/dev/null | grep -iE 'logs|otelcol|error'
# Loki side: distributor/ingester health + discards
curl -sS http://<loki>:3100/metrics | grep -E 'loki_discarded_samples_total|loki_request_duration'
```

`loki_discarded_samples_total` broken down by `reason` tells you exactly
why lines are dropped (rate_limited, out_of_order, stream_limit, …).

## Step 2 — Rate / burst limits (the 429s)

Loki limits per tenant: ingestion rate (MB/s) and burst. A log flood
(one chatty service, a crash loop spewing stack traces) blows the limit
and 429s everything for that tenant. Either cap the noisy source
(`diagnostics/journal-disk-full.md` mindset — fix the spam) or raise
`ingestion_rate_mb` / `ingestion_burst_size_mb` if the volume is
legitimate.

## Step 3 — Cardinality / stream explosion

A *stream* in Loki = a unique label set. High-cardinality labels (pod
hash, request id, level+path combos) create millions of streams and blow
`max_streams_per_user`. The fix mirrors Prometheus cardinality: **keep
labels low-cardinality; put high-cardinality data in the log line, not
in labels** (you grep the line content with LogQL, you don't need it as
a label).

## Step 4 — Ordering / clock skew

`entry out of order` (older Loki) or `too far behind` means a stream's
timestamps aren't monotonic — usually multiple shippers writing the same
stream, or node clock skew (see `diagnostics/clock-skew-ntp.md`). Ensure
unique stream labels per source and synced clocks.

## Decision tree

| Signal | Action |
|---|---|
| 429, one noisy source | cap the log spammer at the source |
| 429, legitimate volume | raise ingestion_rate/burst; scale ingesters |
| stream/max-streams limit | cut label cardinality (move data into the line) |
| out-of-order / too far behind | unique stream labels; fix clock skew |
| 5xx / refused from Loki | distributor/ingester down or OOM — scale/restart |

## References

- [Loki — Limits & configuration](https://grafana.com/docs/loki/latest/configure/)
- [Loki label best practices](https://grafana.com/docs/loki/latest/get-started/labels/bp-labels/)
- vault: `diagnostics/prometheus-high-cardinality.md`, `diagnostics/clock-skew-ntp.md`, `systems/observability-stack/loki.md`
