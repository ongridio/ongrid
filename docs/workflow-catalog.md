# Ongrid Workflow Catalog

This catalog describes workflow capabilities that are ready to present on the website and GitHub README. The examples are written as product-facing copy, not internal test notes.

## What Workflows Do

Ongrid workflows connect **triggers -> nodes -> artifacts**.

A workflow can start manually, on a schedule, from an alert, or from an agent-generated draft. It can call tools, dispatch specialist agents, branch on conditions, transform fields, send notifications, and produce durable artifacts such as reports or pages.

The goal is simple: once a team finds a repeatable operations pattern, Ongrid turns it into a visible, reviewable, and reusable flow.

## Node Types

| Node | Purpose | Example |
|---|---|---|
| **Trigger** | Starts a workflow manually, on a schedule, or from an alert. | Run a daily fleet health check at 09:00. |
| **Tool** | Calls a structured capability with typed inputs and outputs. | Query Prometheus, inspect host load, list incidents. |
| **Agent** | Dispatches a specialist worker that can reason and call tools. | Ask a network specialist to inspect packet loss. |
| **Condition** | Splits the flow based on an expression. | Continue only if error rate is above threshold. |
| **Transform** | Shapes upstream output for the next node. | Extract the top three affected edges. |
| **Notify** | Sends results to chat or webhooks. | Post an incident summary to Slack or Telegram. |
| **Artifact** | Stores a report, page, or investigation summary. | Create a private incident report with a share TTL. |

## Ready-to-show Workflow Examples

| Workflow | Node chain | Operator outcome |
|---|---|---|
| **Platform Snapshot** | `query_promql(up)` -> `query_devices` -> `get_topology` -> artifact | One page showing platform reachability, device state, and topology context. |
| **Kubernetes Triage** | `namespaces_list` -> `pods_list` -> `events_list` -> notify | A fast read-only view of cluster health and recent scheduling events. |
| **Host Health Check** | `get_host_load` -> `get_host_processes` -> `get_edge_summary` -> artifact | CPU, memory, process, and edge-health evidence collected in one run. |
| **Alert and Change Audit** | `query_alert_rules` -> `query_incidents` -> `query_change_events` | A compact view of active alerts and suspicious changes around the same window. |
| **Incident Correlation** | `query_incidents` -> `correlate_incident` -> agent summary | Multi-signal evidence attached to a single incident summary. |
| **Approval-backed Restart** | `get_edge_summary` -> `host_restart_service` proposal -> approval -> notify | A service restart can be proposed, reviewed, executed, and audited. |
| **Daily Operations Brief** | schedule -> health queries -> active incidents -> generated page | A private morning report for on-call handoff. |

## Tool Groups

### Observability

| Tool | What it does | Workflow use |
|---|---|---|
| `query_promql` | Query Prometheus metrics. | SLO checks, resource pressure, availability snapshots. |
| `query_logql` | Query Loki logs. | Error bursts, deploy correlation, suspicious messages. |
| `query_traceql` | Query Tempo traces. | Slow paths, failing spans, downstream latency. |
| `list_metric_catalog` | List collected metrics and labels. | Help the agent choose valid PromQL. |
| `list_database_sources` | List discovered database metric sources. | Build database health workflows. |
| `analyze_database_status` | Inspect database health from metrics. | Produce DB-specific triage summaries. |

### Fleet and Hosts

| Tool | What it does | Workflow use |
|---|---|---|
| `query_devices` | List devices and edge inventory. | Fleet snapshots and targeting. |
| `get_edge_summary` | Aggregate edge health. | Pick affected edges for follow-up. |
| `rank_edges` | Rank edges by CPU, memory, or disk. | Find overloaded hosts. |
| `find_outlier_edges` | Detect resource outliers. | Surface unusual hosts before alerts fire. |
| `get_host_load` | Read host CPU, memory, and load. | Fast host health check. |
| `get_host_processes` | Read top processes. | Identify noisy or stuck processes. |
| `host_bash` | Run approved read-only commands. | Deep inspection through the edge tunnel. |
| `host_restart_service` | Propose a service restart. | Controlled remediation with approval. |

### Topology and Incidents

| Tool | What it does | Workflow use |
|---|---|---|
| `get_topology` | Fetch topology overview. | Understand upstream and downstream impact. |
| `expand_topology` | Expand from a node. | Estimate blast radius. |
| `find_topology_node` | Search topology by name. | Anchor an investigation. |
| `query_incidents` | List incidents. | Build incident dashboards and briefs. |
| `correlate_incident` | Correlate metrics, logs, and traces around an incident. | Generate evidence-backed summaries. |
| `query_alert_rules` | List alert rules. | Audit alert coverage. |
| `query_change_events` | Read nearby change events. | Find likely patient-zero changes. |

### Knowledge and Code

| Tool | What it does | Workflow use |
|---|---|---|
| `query_knowledge` | Search runbooks and diagnostic knowledge. | Attach likely playbooks to incidents. |
| `list_repo_sources` | List connected code repositories. | Scope source-code search. |
| `grep_source` | Search source code. | Find ownership, configs, and suspicious changes. |
| `read_source` | Read a source file. | Cite relevant code in a diagnosis. |

### External Tools through MCP

| Tool family | What it enables | Workflow use |
|---|---|---|
| Kubernetes MCP tools | Namespaces, pods, events, resources, logs, and node stats. | Cluster triage and read-only inspection. |
| Custom MCP servers | Team-specific systems exposed as structured tools. | Bring deploy, CMDB, ticketing, or cloud inventory into the same graph. |

## Website Narrative

Use this structure for a website section:

1. **Start with the operator problem.**
   - Incidents repeat.
   - Scripts drift.
   - Runbooks are separate from evidence.
   - AI output is hard to trust when it is not connected to tools and approval.

2. **Show the workflow answer.**
   - Trigger from alert, schedule, manual run, or AI draft.
   - Collect evidence with typed tools.
   - Dispatch a specialist agent when reasoning is needed.
   - Branch based on risk or result.
   - Produce a report, page, chat message, or approval proposal.

3. **Close with governance.**
   - Read-only steps can run automatically.
   - Mutating steps require approval.
   - Every run has a history and artifacts.

## Screenshot Ideas

| Screenshot | What to frame | Caption |
|---|---|---|
| Workflow editor | Trigger, agent node, tool nodes, condition, artifact output. | "Build incident workflows as visible graphs." |
| Run history | Node states, durations, outputs, and errors. | "Every automation run is inspectable." |
| AI-generated workflow | Prompt -> editable nodes. | "Start from natural language, then review the graph." |
| Approval-backed action | Proposal node and approval result. | "Separate reasoning from execution." |
| Artifacts output | Generated page/report from a run. | "Turn workflow output into durable handoff material." |

## Guardrails

- Read-only workflows can be automated freely.
- Mutating tools must produce proposals and wait for approval.
- Tool schemas should be explicit enough that agent calls are predictable.
- Workflow output should prefer structured artifacts over long chat-only text.
- Screenshots should avoid secrets, real hostnames, tokens, customer data, and production identifiers.
