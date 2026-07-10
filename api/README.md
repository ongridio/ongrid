# ongrid API — proto definitions

Single source of truth for all public API contracts. REST routes are hand-written
(`internal/*/server/`, chi) against these Go types — no grpc-gateway in MVP.

## Layout

```
api/
├── buf.yaml
├── buf.gen.yaml
├── iam/v1/iam.proto                  ongrid.iam.v1
├── manager/
│   ├── edge/v1/edge.proto            ongrid.manager.edge.v1
│   ├── k8s/v1/k8s.proto              ongrid.manager.k8s.v1
│   ├── metric/v1/metric.proto        ongrid.manager.metric.v1
│   └── aiops/v1/aiops.proto          ongrid.manager.aiops.v1
├── tunnel/v1/tunnel.proto            ongrid.tunnel.v1 (geminio payloads, NOT gRPC)
└── gen/                              generated, .gitignored
```

## Conventions

- Proto package: `ongrid.<bc>[.<subdomain>].v<major>`.
- `go_package = "github.com/ongridio/ongrid/api/gen/<path>/v1;<name>v1"`.
- `uint64` for IDs, `google.protobuf.Timestamp` for times, `string` for tokens.
- Every RPC gets its own Request / Response type (even if empty) for forward-compat.
- `org_id` is NEVER a user-supplied request field — it comes from JWT claims /
  URL path via `internal/pkg/auth` middleware (ADR-003). It MAY appear in
  response messages as a read-only echo.
- `optional` is used sparingly; omit unless explicit presence is load-bearing.
- Each service's messages live in a single `.proto` file (don't split).

## Regenerate Go stubs

```
make proto
```

Generated `*.pb.go` files land in `api/gen/<service>/v1/` and are NOT committed.
Breaking-change detection is handled by `buf breaking` in CI.
