# ADR-032 OpenTelemetry Application Profiling

Status: Accepted

## Decision

Use the OpenTelemetry Profiles signal end to end:

- `otelcol-contrib` with its `pprof` receiver for all application-exposed profiles, including CPU;
- OTLP/HTTP `/v1development/profiles` behind the existing Edge data-plane authentication;
- a pinned Collector gateway forwarding OTLP/gRPC to a pinned Pyroscope backend;
- Pyroscope storage queried by Ongrid's native profile views.

The existing Edge plugin supervisor owns the collector, and `profiles` remains disabled by default.

## Rationale

This keeps process control, authentication, retries and health reporting in existing Ongrid paths while using one already-bundled collector. Requiring an application endpoint makes the capability boundary explicit and avoids shipping a second privileged profiler.

## Consequences

- The Edge dependency archive contains only `otelcol-contrib` for profiling.
- Versions must be upgraded as a compatibility set while Profiles remains alpha.
- Applications must expose a compatible endpoint that is reachable from the Edge host.
