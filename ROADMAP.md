# Ongrid Roadmap

_Last updated: 2026-06-06_

Working roadmap for the open-core Ongrid project. Items merge user-driven
ideas (2026-06-06 planning session) with the historical backlog scattered
across ADRs, HLDs, PRDs, and prior planning memos.

## Legend

| Mark | Meaning |
|---|---|
| ✓ | Already landed — listed only as context for follow-ups |
| ◐ | In progress |
| □ | Queued, not started |
| ◯ | Parked — explicitly deferred until a stated trigger condition |

## Guiding principle

Ongrid's moat is the `geminio` two-way tunnel + an AI agent that can take
action on customer hosts. The three pillars (metrics / logs / traces) are
the entry ticket, not the differentiator. Every roadmap item is judged
against one question: **does this let the agent act, or surface a richer
action in the incident timeline?** Pure-display features defer.

Ordering inside each section is rough priority within the section, not
across sections.

---

## A. Causal RCA depth (HLD-013 Phase 3)

A1. ✓ Phase 1 — investigator prompt rewritten as a causal traversal loop;
    `max_turns` 25 → 40; structured `根因/因果链/现象/置信度` output.

A2. ✓ Phase 2 — `query_change_events` BaseTool reads HLD-010 audit log as
    a "patient zero" candidate set; wired through `Registry.SetAuditLister`.

A3. □ **Edge-side change watcher** — audit log only sees changes made
    through Ongrid. Subscribe to `journald` + `dockerd events` + apt/dnf
    history on the edge so external SSH / out-of-band deploys / container
    churn show up as candidate root causes.

A4. □ **Directed dependency edges + baselines** — topology graph today is
    undirected; RCA can't always tell "upstream of" from "downstream of".
    Add direction + per-metric baseline so prompts can reason about
    anomaly position in the dep tree.

A5. □ **Cross-session similar-incident retrieval** —
    `incident_resolutions` table + embedding + `query_similar_incidents`
    tool + resolve-modal in incident UI. Closes the
    "we've seen this before" loop.

---

## B. Remote diagnostic action tools (Roadmap Step 3)

B1. □ **Remote packet capture → object storage + UI render** ★
    Killer feature: `capture_pcap(iface, bpf, duration)` BaseTool, tarball
    uploaded to built-in MinIO/S3, incident timeline links to an embedded
    pcap viewer (web wireshark or a simplified flow table). Nobody else
    in the SaaS space ships this because they lack the secure two-way
    tunnel.

B2. □ **Network probes** — `probe_tcp` / `probe_http` / `probe_dns` /
    `traceroute` / `mtr` as BaseTools. Currently doable via `host_bash`
    but probes deserve first-class output schemas for incident timeline.

B3. □ **File + log + kernel diagnostics** — `tail_file` / `grep_file` /
    `strace` / `lsof` / `dmesg` / `sosreport` BaseTools.

B4. ◐ **Network Layer-1 cmdpolicy expansion** — OVS / nft / conntrack /
    ipset / ethtool / bpftool / `ip netns` read-side already shipped
    (9 binaries, see `internal/edgeagent/cmdpolicy/policy.go`).

B5. □ **Network Layer-2/3 skills** — `host_ovs_show`,
    `host_netfilter_dump`, `host_conntrack_summary`, eBPF preset library
    (preset IDs only — never raw `bpftrace -e <body>`).

---

## C. Natural-language querying

C1. □ **LLM-generated PromQL / LogQL / TraceQL** — `chat_to_query`
    BaseTool, schema-augmented context (label set + metric metadata),
    dry-run preview before execution, frequently-hit patterns auto-saved
    as templates. Lowers the floor for operators who don't speak query
    DSLs yet.

C2. □ **Chat → Grafana panel** — natural-language description picks a
    panel + range + variable values and either deep-links or embeds the
    panel inline in chat.

---

## D. Agent kernel quality

D1. □ **Specialist sub-agents** — `specialist-network`, `specialist-disk`,
    `specialist-process` personas; coordinator picks one by incident type
    and constrains its tool bag. Activates the sub-agent framework that
    currently sits idle.

D2. □ **Critic loop** — primary ReAct finishes → critic LLM checks for
    unevidenced claims, missed tool calls, broken causal chains → up to
    2 corrective rounds. Industry reports +10–30% accuracy. Doubles
    tokens, gate at `severity ≥ critical`.

D3. □ **Eval / replay framework** — 5–10 golden incidents annotated with
    expected tool set + answer keywords; `cmd/ongrid-eval` CLI; CI hook
    on prompt / persona / tool-description changes.

---

## E. User-facing assistant UX

E1. □ **Global Side Panel assistant** — floating button + `Cmd+K`,
    available on every page, auto-seeds prompt with current page context.
    Single biggest UX uplift; landing dock for everything else here.

E2. □ **Multi-assistant / user-defined assistants** — each carries its
    own prompt + tool subset + default scope. Side Panel switches
    between them.

E3. □ **Quick Action cards** — dynamic slot above the chat input;
    page-context-aware "one-click ask" templates.

E4. □ **Knowledge bookmarking** — pin useful agent answers to an
    `agent_knowledge` table; detail pages get a "related knowledge"
    panel; list pages get search.

E5. □ **Reasoning timeline visualisation** — chat toggle that renders
    `user → thought → tool_calls → tool_results → final answer` as a
    tree, not flat text.

---

## F. Data and resource integrations

### F1. Kubernetes

F1.1. □ **K8s edge plugin** — installable as a node-level binary or a
    single in-cluster pod with a service-account-driven kubectl; RBAC
    manifests shipped.

F1.2. □ **K8s BaseTool suite** — `kube_get_pods`, `kube_describe`,
    `kube_logs` (streaming), `kube_events`, `kube_top`, `kube_exec`
    (gated as dangerous).

F1.3. □ **Operator / CRD deployment** — `OngridEdge` CRD, DaemonSet
    mode, aligned with ADR-024 bundle upgrade.

F1.4. □ **K8s topology** — Pod / Deployment / Service / Ingress as
    topology nodes; blast-radius BFS spans K8s resources too.

### F2. Cloud resources

F2.1. □ **Read-only inventory tools** — AWS / GCP / Aliyun / Tencent
    Cloud: EC2 / CVM list, VPCs and routes, security groups, RDS, LB,
    object storage usage.

F2.2. □ **Cloud-monitoring read-through** — CloudWatch / Stackdriver /
    Aliyun CMS as RCA evidence sources; not a Prometheus replacement,
    just fills the cloud-native resource gap.

F2.3. □ **Cloud cost** — `cloud_cost_breakdown` BaseTool tied into
    incidents so the expensive ones surface first.

F2.4. □ **Per-org cloud credentials** — credential vault with rotation,
    modelled on ADR-023 (Git credentials dual rail).

### F3. LLM providers

F3.1. □ **Ollama support** — Custom (OpenAI-compatible) provider hint
    pre-fills `http://localhost:11434/v1`, auto-pulls the model list.
    Marketing angle: zero outbound data.

F3.2. □ **vLLM / SGLang / LMDeploy** preset support for self-hosted
    GPU customers.

F3.3. □ **Bedrock / Vertex** — enterprise-cloud customers; defer until
    asked.

### F4. IM / collaboration

F4.1. ✓ Slack / Telegram / Lark two-way already shipped (ADR-021 /
    ADR-031).

F4.2. □ **DingTalk** — completes the CN big-three.

F4.3. □ **Microsoft Teams** — required for overseas enterprise.

F4.4. □ **WeCom (企业微信) two-way** — webhook-only today; bot mode
    needed for chat-driven actions.

### F5. Logs and traces (Roadmap Step 5)

F5.1. ✓ Loki path already shipped (ADR-012).

F5.2. □ **Tempo traces** — edge OTel collector reverse-proxied through
    manager ingress, same pattern as the metrics path. Hold until a
    real customer asks; storage + UX is a black hole otherwise.

F5.3. □ **eBPF auto-tracing** (Pixie-inspired) — only after F5.2 is
    live and stable.

---

## G. Engineering and operations

### G1. Install / first-boot

G1.1. □ **Port-conflict preflight** — `ss -tlnp | grep :443` first,
    auto-bump to 8443 / 8080 on collision and propagate to
    `ONGRID_PUBLIC_URL`.

G1.2. □ **Co-tenant vs sole-tenant prompt** — preflight asks whether
    this box runs other web services; the answer drives port and
    public-URL choice.

G1.3. □ **Internal vs public IP confirmation** — single-box self-test
    users usually want the internal IP, not the cloud-metadata public IP.

G1.4. □ **Mirror health check** — after writing `daemon.json` and
    restarting docker, `docker pull hello-world` as a probe; roll back
    or switch the mirror list on failure.

G1.5. □ **`read -s` for admin password** — current prompt leaks into
    `.bash_history`.

G1.6. □ **First-boot guided wizard** — after install, the first UI
    visit walks through admin reset → LLM provider → IM → first edge
    install as one flow (PRD-001).

G1.7. ✓ Uninstall script wholesale-purges plugin binaries and work
    directory; stops units unconditionally with a `pkill -9` fallback
    (PR #46, PR #47).

### G2. Edge lifecycle

G2.1. ✓ ADR-024 one-touch bundle upgrade with `.previous` rollback.

G2.2. □ **Channels and canary** — stable / beta / canary, gated by
    edge tags.

G2.3. □ **Health-aware rollout** — auto-rollback if the post-upgrade
    heartbeat doesn't come back green within 30s.

G2.4. □ **Offline upgrade bundle** — internal-network customers,
    manager acts as the mirror.

### G3. Prometheus production hardening (parked)

G3.1. ◯ nginx `/prometheus/` `auth_request` to close the remote_write
    endpoint from the open internet.

G3.2. ◯ `promwrite.Ingester` ring buffer + worker pool + bbolt DLQ.

G3.3. ◯ VictoriaMetrics drop-in compose profile.

G3.4. ◯ `install.sh` "built-in Prom / VictoriaMetrics / external TSDB"
    selector.

G3.5. ◯ Label whitelist to bound cardinality blow-ups.

Trigger to unpark: > 100 edges per customer, an HA / SLA request,
or `promwrite` write-timeout accumulation in manager logs.

### G4. Self-observability (ADR-026)

G4.1. ✓ `/metrics`, 6 self-alerts, dashboard already shipped.

G4.2. □ **SLO board** — availability, tool success rate, RCA accuracy
    (fed by D3 eval framework).

G4.3. □ **Self-diagnostic agent** — periodic self-RCA against the
    manager's own metrics.

### G5. Security / sandbox

G5.1. ◯ microsandbox upgrade path — only when a customer demands kernel
    isolation; until then `host_bash` + `cmdpolicy` covers 90% of
    diagnostic surface.

G5.2. □ **WebSSH (ADR-019)** — geminio stream + xterm.js, exposed as
    a fallback inside SOP execution.

G5.3. □ **Per-tool RBAC + audit replay** — ADR-022 gates by viewer
    role; granularity needs to drop to per-tool.

### G6. Credentials / knowledge

G6.1. ✓ ADR-023 SSH key table + Git credentials dual rail.

G6.2. □ **ADR-018 RepoFetcher** — per-repo auth without re-introducing
    the token leakage concerns ADR-018 (revised) tabled.

G6.3. □ **Offline vault bundle** — for customers without outbound
    internet; built-in vault + offline snapshot tarball.

---

## H. Ecosystem / late-stage

H1. ◯ **Skill marketplace public listing** (ADR-017) — wait until skill
    count crosses ~30.

H2. □ **HLD-009 coordinator e2e evaluation** — currently a design;
    becomes runnable once D3 eval framework is live.

H3. □ **HLD-012 code-aware analysis** — combine a code repo with
    incidents; PR diff → impact analysis.

H4. ◯ **Open ecosystem** — plugin SDK docs and third-party BaseTool
    registration; defer until the open-core split (ADR-030) has been
    out long enough to attract contributors.

---

## I. SOP / Runbook execution loop

The most ambitious chunk on the roadmap. Listed last on purpose:
sections A–H either unblock SOP or have to be solid before SOP can
ship safely. **Do not start until the diagnostic + action tools (B)
and the agent kernel quality work (D) are in a reliable place** —
otherwise the executable path becomes an outage generator instead of
a moat.

I1. □ **Tool.Class three-tier** — `safe` (read-only) / `mutating`
    (state change) / `dangerous` (irreversible). Replaces today's
    binary read/write split.

I2. □ **SOP DSL** — YAML runbooks: `triggers / steps / approvals /
    rollback`.

I3. □ **Two-signature execution chain** — manager RSA signs, edge
    verifies, both ends audit-log every step.

I4. □ **`review_gate` sub-agent** — mutating step spawns a reviewer
    worker → writes a `mutating_proposal` row → UI surfaces an action
    card → operator signs → edge executes → timeline gets a
    `mutating_action_executed` entry.

I5. □ **Initial playbook library** — opening four:
    `host_restart_service`, `disk_cleanup`, `log_rotate`,
    `certificate_renew`.

---

## J. Periodic agent jobs

Built on the same scheduler primitive; sized for "agent watches over
time" workloads rather than synchronous chat.

J1. □ **Inspection / 巡检** — scheduled (daily / weekly) sweeps of
    every edge run a lightweight RCA: `top_load_anomaly`,
    `dep_health_check`, `cert_expiry_check`; matches auto-create
    incidents.

J2. □ **Weekly / monthly digest** — cross-edge aggregate of alerts,
    incidents, executed actions; LLM-written summary; IM-pushed.

J3. □ **Watch tasks / proactive notification** — `create_watch
    (condition, expire_at)` BaseTool; when the condition fires, push
    back into the original session over SSE.
