# Ongrid Product Showcase

This document is website-ready product copy for the Ongrid homepage, launch pages, and screenshot sections.

## Positioning

Ongrid is an AI-native SRE workspace for self-hosted infrastructure teams.

It connects observability, topology, host access, runbooks, source code, workflows, and approval gates so operators can move from alert to evidence, root cause, remediation proposal, and audited execution in one loop.

## First-screen Message

**Headline**

Investigate incidents, explain blast radius, and move approved fixes through governed operations workflows.

**Supporting copy**

Ongrid gives infrastructure teams an AI-assisted control plane for alerts, metrics, logs, traces, topology, edge-host inspection, knowledge retrieval, and safe execution. The agent gathers evidence before making claims, proposes changes before running them, and records every tool call for review.

**Primary proof points**

- Evidence-first RCA across metrics, logs, traces, topology, host state, changes, runbooks, and code.
- Outbound edge tunnels for host inspection without inbound SSH exposure.
- Approval gates for mutating actions, with dry-run output and audit history.
- Visual workflows that turn repeated incident handling into reusable automation.
- Private-by-default artifacts for reports, pages, and investigation summaries.

## Product Pillars

### 1. Incident Room

The Incident Room turns an alert into an investigation record.

It brings together the alert, current symptoms, affected devices, topology neighbors, recent changes, logs, traces, query evidence, related incidents, and the agent's root-cause narrative. Operators can see what the agent checked, what it ruled out, and where confidence is still low.

**Screenshot focus**

- Incident timeline
- RCA summary
- Evidence cards
- Related logs/traces
- Proposed action card

**Best caption**

"From alert to evidence-backed root cause, with the blast radius and next action in the same record."

### 2. Workflow Studio

Workflow Studio turns successful operational patterns into reusable flows.

Teams can compose triggers, tool nodes, agent nodes, HTTP requests, conditions, notifications, and artifacts. Workflows can run manually, on a schedule, or when alerts fire. AI generation helps bootstrap the graph, but every node remains visible and editable.

**Screenshot focus**

- Visual workflow editor
- Tool palette
- Agent node configuration
- Variable picker
- Run history

**Best caption**

"Reusable incident and inspection workflows instead of scattered scripts and one-off prompts."

### 3. Approval Gate

Approval Gate keeps automation useful without hiding production changes.

When an agent wants to restart a service, change a config, or execute a risky command, it creates a proposal. The proposal can include scope, target edges, risk class, dry-run output, rollback notes, and reviewer context. Execution is separate from reasoning.

**Screenshot focus**

- Proposed command
- Risk label
- Affected resources
- Approve/reject controls
- Audit trail

**Best caption**

"Agents can propose changes; operators decide what runs."

### 4. Fleet Control

Fleet Control gives operators a live map of edges, devices, health, and host access.

Edge agents dial out to the manager, so hosts do not need public SSH, HTTP, or jumpbox exposure. Operators can inspect host load, processes, logs, and service state through audited tools and browser shell access.

**Screenshot focus**

- Edge list
- Device health
- Topology neighbors
- Browser shell
- Host process and load views

**Best caption**

"Inspect hosts through outbound tunnels, without opening inbound ports."

### 5. Knowledge Vault

Knowledge Vault gives the agent and the operator the same source of truth.

Ongrid can retrieve runbooks, diagnostic playbooks, incident history, architecture notes, and source code while investigating. The result is an answer with context and citations instead of a generic model response.

**Screenshot focus**

- Knowledge repository list
- Search results
- Source snippets
- Diagnostic playbooks
- Related incident summaries

**Best caption**

"Bring runbooks, code, and incident memory into every investigation."

### 6. Skills and MCP

Skills and MCP make Ongrid extensible without hiding what the agent can do.

The Skills catalog shows built-in diagnostics, observability queries, hosted-page generation, IM delivery, risk class, runtime location, and parameter schema. MCP servers add external tools from Grafana, Kubernetes, PagerDuty, GitHub, databases, Terraform, or internal platforms. The same inventory is available in chat and workflow nodes.

**Screenshot focus**

- Skills catalog
- Risk class badges
- Device vs manager runtime
- MCP server list
- Cached tool list
- Trusted vs approval-gated servers

**Best caption**

"Every tool the agent can call is visible, typed, and governed."

### 7. Artifacts Center

Artifacts Center makes investigation output durable.

Reports and generated pages are private by default. Operators can share explicit links with TTL, keep incident writeups for review, and reuse output in handoff, retrospectives, and customer updates.

**Screenshot focus**

- Artifacts grid
- Report/page preview
- Share controls
- TTL settings
- Task output history

**Best caption**

"Turn agent output into durable, reviewable operational artifacts."

## Homepage Section Map

Use this structure for a GitHub README or website landing page.

1. **Hero** - one sentence promise, one dense product screenshot, badges, install CTA.
2. **Why Ongrid** - explain the investigation loop and approval boundary.
3. **Product Tour** - Agent workspace, Workflow Studio, Approval Gate, Artifacts.
4. **In Action** - incident response, daily operations brief, remote diagnostics.
5. **Capabilities** - Incident Room, Fleet Control, Knowledge Vault, Skills and MCP, Artifacts.
6. **Integrations** - observability, chat, LLM providers, MCP servers.
7. **Install** - one command or release tarball, then docs links.

## Screenshot Set

Suggested screenshot inventory for GitHub and website use.

| Screenshot | Purpose | Notes |
|---|---|---|
| Home / Agent workspace | First impression | Show status chips, prompt box, suggested actions, and recent sessions. |
| Workflow Studio | Automation depth | Prefer a workflow graph or a dense workflow list with real names. |
| Skills catalog | Tool visibility | Show safe/mutating classes, device/cloud runtime, and varied skills. |
| MCP servers | Extensibility | Show multiple servers and cached tools, not an empty configuration page. |
| Knowledge Vault | Context/RAG | Show folders, docs, tags, and search results. |
| Knowledge repos | Source-of-truth setup | Show git repos, branches, file counts, and sync status. |
| Approval Gate | Governance | Show approve/reject controls and proposal summaries. |
| Artifacts Center | Durable output | Show multiple generated pages/reports with different operational scenarios. |
| Generated artifact detail | Output quality | Show a readable RCA/report page, not only a grid thumbnail. |

## Example Scenarios

### Alert to Root Cause

```text
Alert: checkout-api p99 latency is above SLO.
Ongrid checks metrics, logs, traces, topology, recent changes, and host state.
It finds read-replica IO saturation after a backup job.
It returns root cause, causal chain, blast radius, query evidence, and mitigation options.
```

### Natural-language Observability

```text
Ask: Did checkout errors start before or after the deploy?
Ongrid generates PromQL and LogQL, runs the queries, and links the Grafana panel.
The operator gets a short answer backed by the exact query evidence.
```

### Remote Diagnostics without Inbound SSH

```text
Ask: Inspect nginx memory, open files, and recent kernel messages on edge-03.
Ongrid runs approved read-only tools through the outbound edge tunnel.
The output is attached to the browser session and incident timeline.
```

### Controlled Remediation

```text
Ask: Restart unhealthy workers in us-east only.
Ongrid identifies candidate services, builds a proposal, estimates blast radius, and waits.
After approval, execution output and rollback notes are attached to the same incident.
```

### Daily Operations Brief

```text
Trigger: every weekday at 09:00.
Workflow: collect service health, active alerts, slow traces, noisy hosts, and pending approvals.
Output: a private report page with explicit share controls and TTL.
```

## Screenshot Checklist

Use this list when preparing GitHub or website screenshots.

- The page is in English.
- The screenshot shows real product surface, not placeholder marketing UI.
- The frame includes enough context for a viewer to understand the workflow.
- Sensitive values, hostnames, tokens, and user data are not visible.
- The caption explains the operator outcome, not only the feature name.
- Dark mode and light mode are both checked when the screenshot comes from the app UI.

## Short Copy Blocks

**One-liner**

AI-native incident investigation and governed remediation for self-hosted infrastructure teams.

**Two-line version**

Ongrid connects observability, topology, host tools, runbooks, workflows, and approval gates.
Agents gather evidence, explain root cause, and propose fixes without bypassing human review.

**Longer description**

Ongrid is an open-source AIOps workspace for teams that operate real infrastructure. It turns alerts and operator questions into evidence-backed investigations by querying metrics, logs, traces, topology, host state, change history, knowledge bases, and source code. When a fix is needed, Ongrid separates reasoning from execution through explicit proposals, approvals, and audit trails.
