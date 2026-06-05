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

## A · Causal RCA depth (HLD-013 Phase 3)

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

### G.2 Edge lifecycle

- **G.2.1** `✓` ADR-024 one-touch bundle upgrade with `.previous` rollback
- **G.2.2** `□` Channels and canary
  - stable / beta / canary, gated by edge tag
- **G.2.3** `□` Health-aware rollout
  - auto-rollback if post-upgrade heartbeat doesn't green within 30s
- **G.2.4** `□` Offline upgrade bundle
  - internal-network customers
  - manager acts as the mirror

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

### G.5 Security / sandbox

- **G.5.1** `◯` microsandbox upgrade path
  - only when a customer demands kernel isolation
  - `host_bash` + `cmdpolicy` covers 90% of diagnostic surface
- **G.5.2** `□` WebSSH (ADR-019)
  - geminio stream + xterm.js
  - exposed as a fallback inside SOP execution
- **G.5.3** `□` Per-tool RBAC + audit replay
  - ADR-022 gates by viewer role
  - granularity needs to drop to per-tool

### G.6 Credentials and knowledge

- **G.6.1** `✓` ADR-023 SSH key table + Git credentials dual rail
- **G.6.2** `□` ADR-018 RepoFetcher
  - per-repo auth without re-introducing the token leakage that tabled
    the first revision
- **G.6.3** `□` Offline vault bundle
  - customers without outbound internet
  - built-in vault + offline snapshot tarball

---

## H · Ecosystem / late-stage

- **H.1** `◯` Skill marketplace public listing (ADR-017)
  - unpark when the skill count crosses ~30
- **H.2** `□` HLD-009 coordinator e2e evaluation
  - currently a design
  - becomes runnable once D.3 eval framework lands
- **H.3** `□` HLD-012 code-aware analysis
  - combine a code repo with incidents
  - PR diff → impact analysis
- **H.4** `◯` Open ecosystem
  - plugin SDK docs + third-party BaseTool registration
  - defer until open-core split (ADR-030) attracts contributors

---

## I · SOP / Runbook execution loop

The most ambitious chunk on the roadmap. Listed last on purpose:
sections A–H either unblock SOP or have to be solid before SOP can
ship safely. **Do not start until B and D are in a reliable place** —
otherwise the executable path becomes an outage generator instead of
a moat.

- **I.1** `□` Tool.Class three-tier
  - `safe` (read-only) / `mutating` (state change) / `dangerous` (irreversible)
  - replaces today's binary read/write split
- **I.2** `□` SOP DSL
  - YAML runbooks: `triggers` / `steps` / `approvals` / `rollback`
- **I.3** `□` Two-signature execution chain
  - manager RSA signs
  - edge verifies
  - both ends audit-log every step
- **I.4** `□` `review_gate` sub-agent
  - mutating step spawns a reviewer worker
  - writes a `mutating_proposal` row
  - UI surfaces an action card
  - operator signs → edge executes
  - timeline gets a `mutating_action_executed` entry
- **I.5** `□` Initial playbook library
  - `host_restart_service`
  - `disk_cleanup`
  - `log_rotate`
  - `certificate_renew`

---

## J · Periodic agent jobs

Built on a shared scheduler primitive; sized for "agent watches over
time" workloads rather than synchronous chat.

- **J.1** `□` Inspection (巡检)
  - scheduled (daily / weekly) sweeps over every edge
  - lightweight RCA: `top_load_anomaly`, `dep_health_check`, `cert_expiry_check`
  - matches auto-create incidents
- **J.2** `□` Weekly / monthly digest
  - cross-edge aggregate of alerts, incidents, executed actions
  - LLM-written summary
  - IM-pushed
- **J.3** `□` Watch tasks / proactive notification
  - `create_watch(condition, expire_at)` BaseTool
  - when the condition fires, push back into the original session over SSE
