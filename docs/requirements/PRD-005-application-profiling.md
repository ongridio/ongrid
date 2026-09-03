# PRD-005 Application Profiling

## Goal

Let an operator sample an application's exposed profiling endpoint from **Tools** for one Edge, then inspect the result in the native analysis views.

## Scope

- CPU, `heap`, `allocs`, `goroutine`, `mutex`, and `block` from a pprof-compatible HTTP endpoint through the official Collector pprof receiver.
- Target selection: one Edge plus service name, scheme, IP/host, port, and path. Go-compatible Ongrid Edge defaults are prefilled but editable.
- Transport and storage: authenticated OTLP/HTTP Profiles ingress, Collector gateway, Pyroscope storage, Grafana data source.

## Constraints

- OpenTelemetry Profiles and the pprof receiver are alpha; collector and Pyroscope versions stay pinned.
- Profiles are not universally available without runtime instrumentation. The target application must expose a compatible pprof payload.
- Profiling is disabled by default and is explicitly started/stopped by an operator.

## Acceptance

1. Every profile type requires an application endpoint and is collected by `otelcol-contrib`.
2. Changing type derives the standard Go pprof path while keeping all endpoint fields editable.
3. Invalid schemes, credential-bearing URLs, unsupported types and unreasonable durations are rejected before reaching the Edge.
4. Profiles reach Pyroscope through the authenticated data-plane path and open automatically in the native viewer when sampling completes.
