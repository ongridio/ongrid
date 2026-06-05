# Ongrid Roadmap

_Last updated: 2026-06-06 · Chinese version: [`ROADMAP.zh-CN.md`](ROADMAP.zh-CN.md)_

Working roadmap for the open-core Ongrid project. Merges user-driven
ideas from the 2026-06-06 planning session with the historical backlog
scattered across ADRs, HLDs, PRDs, and prior planning memos.

## Legend

- `✓` already landed — listed only as context for follow-ups
- `◐` in progress
- `□` queued, not started
- `◯` parked — explicitly deferred until a stated trigger condition

## Guiding principle

Ongrid's moat is the `geminio` two-way tunnel plus an AI agent that can
take action on customer hosts. The three pillars (metrics / logs /
traces) are the entry ticket, not the differentiator. Every roadmap
item answers one question: **does this let the agent act, or surface a
richer action in the incident timeline?** Pure-display features defer.

Ordering inside each section is rough priority within the section, not
across sections.

---

## A · Root-cause RCA diagnosis (HLD-013 Phase 3)

- **A.1** `✓` Phase 1 — investigator prompt as a causal traversal loop
  - `max_turns` 25 → 40
  - structured `root cause / causal chain / symptom / confidence` output
- **A.2** `✓` Phase 2 — `query_change_events` BaseTool
  - reads HLD-010 audit log as patient-zero candidates
  - wired through `Registry.SetAuditLister`
- **A.3** `□` Edge-side change watcher
  - today's audit log only sees Ongrid-mediated changes
  - subscribe to `journald` + `dockerd events` + `apt` / `dnf` history
  - surfaces external SSH, out-of-band deploys, container churn
- **A.4** `□` Directed dependency edges + baselines
  - topology graph today is undirected — can't always tell upstream from downstream
  - add direction + per-metric historical baseline
  - feed both into the RCA prompt
- **A.5** `□` Cross-session similar-incident retrieval
  - `incident_resolutions` table + embedding store
  - `query_similar_incidents` BaseTool
  - resolve-modal in the incident UI closes the "seen this before" loop
- **A.6** `□` RCA confidence calibration
  - track per-incident confidence vs operator-marked correctness
  - feed back into prompt / model selection
- **A.7** `□` Root-cause graph visualisation
  - render the causal chain as a node-edge graph alongside the report
  - hovering a node opens the evidence (PromQL, log line, trace span)

---

## B · Remote diagnostic action tools (Roadmap Step 3)

- **B.1** `□` Remote packet capture → object storage + UI render ★
  - `capture_pcap(iface, bpf, duration)` BaseTool
  - tarball uploaded to built-in MinIO / S3
  - incident timeline links to an embedded pcap viewer (web wireshark or
    a simplified flow table)
  - SaaS competitors can't ship this without a secure two-way tunnel
- **B.2** `□` Network probes as first-class BaseTools
  - `probe_tcp` / `probe_http` / `probe_dns`
  - `traceroute` / `mtr`
  - first-class output schemas so results render cleanly in the timeline
- **B.3** `□` File, log, kernel diagnostics
  - `tail_file`, `grep_file`
  - `strace`, `lsof`, `dmesg`, `sosreport`
- **B.4** `◐` Network Layer-1 — cmdpolicy expansion (shipped)
  - 9 binaries: OVS / nft / conntrack / ipset / ethtool / bpftool / `ip netns`
  - read-side only; write-side gated for SOP
  - source: `internal/edgeagent/cmdpolicy/policy.go`
- **B.5** `□` Network Layer-2/3 skills
  - `host_ovs_show`, `host_netfilter_dump`, `host_conntrack_summary`
  - eBPF preset library — preset IDs only, never raw `bpftrace -e <body>`

---

## C · Natural-language querying

- **C.1** `□` LLM-generated PromQL / LogQL / TraceQL
  - `chat_to_query` BaseTool
  - context augmented with label set + metric metadata
  - dry-run preview before execution
  - frequently-hit patterns auto-saved as templates
  - lowers the floor for operators who don't speak query DSLs
- **C.2** `□` Chat → Grafana panel
  - natural-language description picks a panel + range + variables
  - either deep-link or embed inline in chat
- **C.3** `□` Schema-aware autocomplete
  - inline editor for hand-written queries
  - suggests labels + functions from live `/api/v1/labels`

---

## D · Agent kernel quality

- **D.1** `□` Specialist sub-agents
  - personas: `specialist-network`, `specialist-disk`, `specialist-process`
  - coordinator dispatches by incident type, constrains tool bag
  - activates the sub-agent framework that currently sits idle
- **D.2** `□` Critic loop
  - primary ReAct finishes → critic LLM audits for
    - unevidenced claims
    - missed tool calls
    - broken causal chains
  - up to 2 corrective rounds
  - +10–30% accuracy in published reports
  - doubles tokens — gate at `severity ≥ critical`
- **D.3** `□` Eval / replay framework
  - 5–10 golden incidents annotated with expected tool set + answer keywords
  - `cmd/ongrid-eval` CLI
  - CI hook on prompt / persona / tool-description changes
- **D.4** `□` Proposal and confirmation mediation
  - **Purpose**: any agent-initiated mutating action — whether from chat,
    SOP execution (K), or a watch task (L.3) — flows through one pipeline.
  - **D.4.1** Proposal lifecycle
    - `pending` → `approved` / `rejected` / `expired` / `executed` / `rolled-back`
    - persisted in `mutating_proposal` (signed envelope)
  - **D.4.2** `review_gate` sub-agent
    - reviewer worker drafts the proposal, names the SOP step, surfaces
      blast radius, attaches dry-run output
  - **D.4.3** Approval policies
    - severity tiers (`safe` auto / `mutating` operator / `dangerous` two-person)
    - role gates (viewer never, user with scope, admin global)
    - per-edge / per-resource overrides
  - **D.4.4** IM-side approval
    - Slack / Lark / Telegram message with `Approve` / `Reject` / `Defer`
    - signed via short-lived token bound to the proposal
  - **D.4.5** Proposal preview (dry-run diff)
    - render the to-be-applied change before signing
    - file-mode shows unified diff; service-mode shows pre/post state probe
  - **D.4.6** Bulk batching
    - one proposal, N edges, all-or-nothing or rolling
    - per-edge confirmation can opt out
  - **D.4.7** Expiry + auto-decline
    - default 24h, configurable per policy
    - expired proposals never silently execute
  - **D.4.8** Delegation + on-call routing
    - off-hours proposals route to current on-call (see G.10)
    - delegate-when-busy with a fallback chain
  - **D.4.9** Proposal audit trail
    - hash-chained entries (ties into I.4)
    - shareable proposal URL for retrospectives
- **D.5** `□` Cost + token budget controls
  - per-org / per-user monthly cap
  - per-call hard timeout + token cap
  - graceful degradation (smaller model / fewer iterations) before cutoff

---

## E · User-facing assistant UX

- **E.1** `□` Global Side Panel assistant
  - floating button + `Cmd+K` on every page
  - auto-seeds prompt with current page context
  - biggest single UX uplift; landing dock for E.2 through E.5
- **E.2** `□` Multi-assistant / user-defined assistants
  - per-assistant prompt + tool subset + default scope
  - Side Panel switches between them
- **E.3** `□` Quick Action cards
  - dynamic slot above the chat input
  - page-context-aware "one-click ask" templates
- **E.4** `□` Knowledge bookmarking
  - pin useful agent answers to `agent_knowledge`
  - "related knowledge" panel on detail pages
  - search on list pages
- **E.5** `□` Reasoning timeline visualisation
  - chat toggle that renders
    `user → thought → tool_calls → tool_results → final` as a tree
- **E.6** `□` Approval UI
  - one-click approve / reject for D.4 proposals
  - mobile-friendly card view
  - swipe-through inbox for on-call shifts

---

## F · Data and resource integrations

### F.1 Kubernetes

- **F.1.1** `□` K8s edge plugin
  - node-level binary OR single in-cluster pod with service-account kubectl
  - RBAC manifests shipped
- **F.1.2** `□` K8s BaseTool suite
  - `kube_get_pods`, `kube_describe`
  - `kube_logs` (streaming), `kube_events`, `kube_top`
  - `kube_exec` gated as dangerous
- **F.1.3** `□` Operator / CRD deployment
  - `OngridEdge` CRD
  - DaemonSet mode
  - aligned with ADR-024 bundle upgrade
- **F.1.4** `□` K8s topology
  - Pod / Deployment / Service / Ingress as topology nodes
  - blast-radius BFS spans K8s resources

### F.2 Cloud resources

- **F.2.1** `□` Read-only inventory tools
  - AWS / GCP / Aliyun / Tencent Cloud
  - EC2 or CVM list, VPCs, security groups, RDS, LB, object storage
- **F.2.2** `□` Cloud-monitoring read-through
  - CloudWatch / Stackdriver / Aliyun CMS as RCA evidence sources
  - not a Prometheus replacement — fills the cloud-native resource gap
- **F.2.3** `□` Cloud cost
  - `cloud_cost_breakdown` BaseTool
  - tied to incidents so the expensive ones surface first
- **F.2.4** `□` Per-org cloud credentials
  - credential vault with rotation
  - modelled on ADR-023 (Git credentials dual rail)

### F.3 LLM providers

- **F.3.1** `□` Ollama support
  - Custom (OpenAI-compatible) hint pre-fills `http://localhost:11434/v1`
  - auto-pulls the model list
  - marketing angle: zero outbound data
- **F.3.2** `□` vLLM / SGLang / LMDeploy presets
  - self-hosted GPU customers
- **F.3.3** `□` Bedrock / Vertex
  - enterprise-cloud customers; defer until asked

### F.4 IM and collaboration

- **F.4.1** `✓` Slack / Telegram / Lark two-way (ADR-021 / ADR-031)
- **F.4.2** `□` DingTalk — completes the CN big three
- **F.4.3** `□` Microsoft Teams — overseas enterprise
- **F.4.4** `□` WeCom (企业微信) two-way
  - webhook-only today
  - bot mode needed for chat-driven actions

### F.5 Logs and traces (Roadmap Step 5)

- **F.5.1** `✓` Loki path (ADR-012)
- **F.5.2** `□` Tempo traces
  - edge OTel collector reverse-proxied through manager ingress
  - same pattern as the metrics path
  - hold until a real customer asks — storage + UX is a black hole
- **F.5.3** `□` eBPF auto-tracing (Pixie-inspired)
  - only after F.5.2 is live and stable

---

## G · Engineering and operations

### G.1 Install / first-boot

- **G.1.1** `□` Port-conflict preflight
  - `ss -tlnp | grep :443` first
  - auto-bump to `8443` / `8080` on collision
  - propagate to `ONGRID_PUBLIC_URL`
- **G.1.2** `□` Co-tenant vs sole-tenant prompt
  - preflight asks whether this box runs other web services
  - answer drives port and public-URL choice
- **G.1.3** `□` Internal vs public IP confirmation
  - single-box self-test users usually want the internal IP
  - cloud-metadata default isn't always right
- **G.1.4** `□` Mirror health check
  - after `daemon.json` + docker restart, `docker pull hello-world`
  - roll back or switch the mirror list on failure
- **G.1.5** `□` `read -s` for admin password
  - current prompt leaks into `.bash_history`
- **G.1.6** `□` First-boot guided wizard
  - admin reset → LLM provider → IM → first edge install in one flow
  - aligns with PRD-001
- **G.1.7** `✓` Uninstall script
  - wholesale-purges plugin binaries + work dir
  - stops units unconditionally with a `pkill -9` fallback
  - PR #46, PR #47
- **G.1.8** `□` Air-gapped install bundle
  - one tarball, no outbound calls
  - bundled docker images, mirror config, license check

### G.2 Edge lifecycle

- **G.2.1** `✓` ADR-024 one-touch bundle upgrade with `.previous` rollback
- **G.2.2** `□` Channels and canary
  - stable / beta / canary, gated by edge tag
- **G.2.3** `□` Health-aware rollout
  - auto-rollback if post-upgrade heartbeat doesn't green within 30s
- **G.2.4** `□` Offline upgrade bundle
  - internal-network customers
  - manager acts as the mirror
- **G.2.5** `□` Bulk edge re-enrol
  - rotate edge credentials / re-register across a fleet in one click
- **G.2.6** `□` Edge fleet labels + selectors
  - `env=prod`, `region=cn-east`, `role=db`
  - selector applies to upgrades, SOP targets, alert routing

### G.3 Prometheus production hardening (parked)

- **G.3.1** `◯` nginx `/prometheus/` `auth_request`
  - closes remote_write from the open internet
- **G.3.2** `◯` `promwrite.Ingester` ring buffer + worker pool + bbolt DLQ
- **G.3.3** `◯` VictoriaMetrics drop-in compose profile
- **G.3.4** `◯` `install.sh` TSDB selector
  - "built-in Prom / VictoriaMetrics / external TSDB"
- **G.3.5** `◯` Label whitelist to bound cardinality blow-ups
- **Trigger to unpark**
  - > 100 edges per customer, OR
  - an HA / SLA request, OR
  - `promwrite` write-timeout accumulation in manager logs

### G.4 Self-observability (ADR-026)

- **G.4.1** `✓` `/metrics`, 6 self-alerts, dashboard
- **G.4.2** `□` SLO board
  - availability, tool success rate, RCA accuracy (fed by D.3)
- **G.4.3** `□` Self-diagnostic agent
  - periodic self-RCA against the manager's own metrics

### G.5 Backup, restore, disaster recovery

- **G.5.1** `□` Manager state snapshot
  - dump MySQL + Qdrant + objects to a single tarball
  - configurable retention
- **G.5.2** `□` One-command restore
  - point a fresh manager at a snapshot tarball
  - verifies schema version + edge re-handshake plan
- **G.5.3** `□` Off-site replication
  - rsync / S3 sync to a cold-standby region
- **G.5.4** `□` Drill mode
  - timed restore exercise from latest backup; report MTTR
- **G.5.5** `□` Edge-side state checkpoint
  - plugin work dir snapshot before upgrade (already partial via `.previous`)

### G.6 HA and failover

- **G.6.1** `◯` Active-standby manager pair
  - shared MySQL via DRBD / managed cluster
  - VRRP / floating IP
  - trigger: customer asks SLA or > 100 edges
- **G.6.2** `◯` Multi-region read replica
- **G.6.3** `□` Manager health gate
  - `/healthz` deep-mode that exercises every dependency
- **G.6.4** `□` Tunnel session migration
  - move geminio sessions between manager replicas without edge reconnect

### G.7 Alert lifecycle

- **G.7.1** `□` Deduplication + grouping
  - hash on `alertname + labels` window
  - emit one notification per group, append details
- **G.7.2** `□` Silencing
  - operator-set time-bounded silence with reason field
  - silences flow through D.4 if they touch dangerous classes
- **G.7.3** `□` Correlation
  - graph-walk linked alerts (same edge, same service, same incident)
  - collapse into one incident artefact
- **G.7.4** `□` Routing policy DSL
  - per-team / per-severity / per-time-of-day routing
  - escalation if not acknowledged within N min
- **G.7.5** `□` Notification preferences
  - per-user channels, do-not-disturb windows

### G.8 On-call and maintenance windows

- **G.8.1** `□` On-call schedule
  - team + rotation calendar
  - escalation chain
  - integrates with G.7.4 routing
- **G.8.2** `□` Maintenance window
  - per-edge / per-fleet window
  - suppresses alerts AND parks SOP execution proposals
- **G.8.3** `□` Handover digest
  - shift-change summary: open incidents, paused proposals, recent changes

### G.9 Bulk operations and config drift

- **G.9.1** `□` Multi-edge SOP execution
  - apply the same playbook to a selector (G.2.6)
  - per-edge dry-run result, rolling apply
- **G.9.2** `□` Config-as-code (GitOps)
  - alerts / SOPs / channels live in a Git repo
  - manager reconciles
- **G.9.3** `□` Configuration drift detection
  - diff applied config vs desired
  - surface as a low-severity incident
- **G.9.4** `□` Asset inventory / CMDB
  - one table: edge → host → installed packages → exposed ports → owner
  - feed into RCA and topology

### G.10 NOC view and operational dashboard

- **G.10.1** `□` Single-pane status board
  - all edges, all incidents, all open proposals
  - colour-coded by severity
- **G.10.2** `□` Kiosk mode
  - wall-display friendly
  - auto-rotate between fleets
- **G.10.3** `□` Saved views per role
  - "L1 triage", "DBA on-call", "network on-call"

### G.11 Patch and vulnerability management

- **G.11.1** `□` OS / package CVE scan
  - per-edge inventory + CVE feed
  - propose patch SOP through D.4
- **G.11.2** `□` Edge agent self-update CVE awareness
  - manager warns on outdated edge bin with known CVE

### G.12 Postmortem and change management

- **G.12.1** `□` Incident postmortem template
  - auto-fill from incident timeline
  - LLM-drafted narrative, operator edits
- **G.12.2** `□` Change calendar
  - lightweight "what's deploying when"
  - cross-references SOP executions

### G.13 Credentials and knowledge

- **G.13.1** `✓` ADR-023 SSH key table + Git credentials dual rail
- **G.13.2** `□` ADR-018 RepoFetcher
  - per-repo auth without re-introducing the token leakage that tabled
    the first revision
- **G.13.3** `□` Offline vault bundle
  - customers without outbound internet
  - built-in vault + offline snapshot tarball

---

## H · Sandbox and execution isolation

A common substrate for "agent wants to try something risky" — covers
dry-run, capability gating, kernel isolation, and skill testing.
Powered today by `cmdpolicy` + tool classes; this section sketches
how it grows.

- **H.1** `◯` microsandbox runtime (escape hatch)
  - rootless + OCI single binary
  - first plugin-style sandbox; switched on per customer who asks for
    kernel isolation
  - cmdpolicy + bash stays the default channel
- **H.2** `◯` Python execution channel (script_python tool)
  - parked until microsandbox lands (N+16 memo)
  - seccomp + import allowlist + env scrub
- **H.3** `□` Dry-run sandbox for SOP playbooks
  - executes a playbook against a `--dry-run` shim
  - returns expected diff, no real side-effects
  - mandatory for `dangerous` class before D.4 confirmation
- **H.4** `□` Browser sandbox for web-fetch tools
  - ephemeral headless chromium in container
  - no persistent cookies, per-call profile
- **H.5** `□` Skill / playbook authoring sandbox
  - skill author edits + tests against a synthetic edge fixture
  - "publish" only after sandbox green
- **H.6** `□` Per-tenant compute budget
  - CPU / mem caps on agent + tool subprocesses
  - rate limit per-user-per-minute
- **H.7** `□` seccomp + capability profile library
  - per-tool profile committed in-repo
  - generator from `cmdpolicy` rules
- **H.8** `□` Dangerous-class ephemeral container wrapper
  - I/O-destructive commands run inside a throwaway container with
    bind-mounted target dirs
  - rollback by container delete

---

## I · Security and compliance

- **I.1** `□` SSO / SAML / OIDC
  - Okta / Azure AD / Google Workspace
  - just-in-time user provisioning with role mapping
- **I.2** `□` MFA enforcement
  - TOTP minimum
  - per-org policy: required / optional / off
- **I.3** `□` Session management
  - active sessions list per user
  - revoke from admin UI
  - IP allowlist per org
- **I.4** `□` Tamper-evident audit log
  - hash-chained rows (chain hash referenced in D.4 proposal envelope)
  - daily root hash anchored to Git for external verification
- **I.5** `□` Credential rotation policy
  - LLM keys, IM tokens, Git deploy keys
  - operator-defined rotate period; UI surfaces age
- **I.6** `□` TLS cert auto-rotation
  - manager via Let's Encrypt + self-signed fallback
  - edge agents pull fresh manager cert on heartbeat
- **I.7** `□` Edge binary vulnerability tracking
  - bundled bin versions exposed via `/edge/inventory`
  - CVE feed → action card
- **I.8** `□` LLM PII filtering on prompts
  - configurable redaction (IPs, emails, hostnames optional)
  - on-prem mode pins all LLM traffic to self-hosted
- **I.9** `□` Prompt-injection defense + output content filter
  - tool descriptions tagged with trust level
  - output scanned for leaked credentials before display
- **I.10** `□` Rate limiting per user / org
  - chat msgs / min, tool calls / hour, IM webhook / sec
- **I.11** `□` Encryption at rest
  - MySQL + Qdrant + object storage
  - per-org KMS option for enterprise
- **I.12** `□` Compliance evidence pack
  - one-click export: audit log, access list, change history,
    incident timelines
  - SOC2 / ISO 27001 / 等保 friendly format
- **I.13** `□` Per-org secrets vault
  - encrypted at rest, scoped to org
  - injected into SOP runtime via env
- **I.14** `□` Anomalous-usage detection
  - login from new geo, spike in dangerous tool calls
  - notification + auto-quarantine option

---

## J · Ecosystem / late-stage

- **J.1** `◯` Skill marketplace public listing (ADR-017)
  - unpark when the skill count crosses ~30
- **J.2** `□` HLD-009 coordinator e2e evaluation
  - currently a design
  - becomes runnable once D.3 eval framework lands
- **J.3** `□` HLD-012 code-aware analysis
  - combine a code repo with incidents
  - PR diff → impact analysis
- **J.4** `◯` Open ecosystem
  - plugin SDK docs + third-party BaseTool registration
  - defer until open-core split (ADR-030) attracts contributors

---

## K · SOP / Runbook execution loop

The most ambitious chunk on the roadmap. Listed near the end on
purpose: sections A–I either unblock SOP or have to be solid before
SOP can ship safely. **Do not start until B (diagnostic tools), D
(agent kernel + D.4 proposal mediation), H (sandbox), and I (security)
are in a reliable place** — otherwise the executable path becomes an
outage generator instead of a moat.

- **K.1** `□` Tool.Class three-tier
  - `safe` (read-only) / `mutating` (state change) / `dangerous` (irreversible)
  - replaces today's binary read/write split
- **K.2** `□` SOP DSL
  - YAML runbooks: `triggers` / `steps` / `approvals` / `rollback`
- **K.3** `□` Two-signature execution chain
  - manager RSA signs
  - edge verifies
  - both ends audit-log every step
  - flows through D.4 for the operator confirmation half
- **K.4** `□` Per-step rollback
  - every mutating step declares its inverse (or `noop` if irreversible)
  - aborted runs unwind in reverse order
- **K.5** `□` Initial playbook library
  - `host_restart_service`
  - `disk_cleanup`
  - `log_rotate`
  - `certificate_renew`
- **K.6** `□` Playbook marketplace (internal)
  - share + import playbooks between orgs
  - signed by author org
- **K.7** `□` SOP execution timeline
  - per-run view: step-by-step with logs, exit codes, diff before/after
  - replayable for postmortem

---

## L · Periodic agent jobs

Built on a shared scheduler primitive; sized for "agent watches over
time" workloads rather than synchronous chat.

- **L.1** `□` Inspection (巡检)
  - scheduled (daily / weekly) sweeps over every edge
  - lightweight RCA: `top_load_anomaly`, `dep_health_check`, `cert_expiry_check`
  - matches auto-create incidents
- **L.2** `□` Weekly / monthly digest
  - cross-edge aggregate of alerts, incidents, executed actions
  - LLM-written summary
  - IM-pushed
- **L.3** `□` Watch tasks / proactive notification
  - `create_watch(condition, expire_at)` BaseTool
  - when the condition fires, push back into the original session over SSE
- **L.4** `□` Capacity forecast
  - extrapolate disk / memory / cardinality trends
  - file a proposal (D.4) when a threshold projects within N days
- **L.5** `□` Cost roll-up
  - LLM token + storage + bandwidth attribution per org
  - mailed monthly
