#!/usr/bin/env python3
"""Seed an English, dark-theme showcase environment for README screenshots.

The script owns only presentation/demo data that is visible in product screenshots:
hosted pages, workflows, chat sessions, MCP servers, topology, and report rows.
It deliberately avoids built-in documentation or product i18n resources.
"""

from __future__ import annotations

import argparse
import datetime as dt
import html
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import uuid
from pathlib import Path
from urllib.parse import unquote
import re


SEED = "showcase_en_dark_v1"
BASE_TIME = dt.datetime(2026, 7, 7, 9, 0, 0, tzinfo=dt.timezone.utc)
OWNER_ID = 1

WORKFLOW_TITLES = [
    "Checkout Latency RCA",
    "Payment Authorization Failure RCA",
    "Database Replication Lag Triage",
    "Kafka Consumer Lag Recovery",
    "Kubernetes Image Pull Backoff Triage",
    "Canary Release Guardrail",
    "Post Deploy Health Verification",
    "Daily Operations Brief",
    "Weekly Capacity Review",
    "Security Patch Rollout Check",
    "TLS Certificate Expiry Sweep",
    "CDN Origin Fallback Investigation",
    "API Gateway Error Budget Review",
    "Redis Memory Pressure Triage",
    "Disk IO Saturation RCA",
    "JVM Heap Pressure Investigation",
    "Node Pool Drain Safety Check",
    "Prometheus Scrape Failure Triage",
    "Loki Ingestion Backpressure Review",
    "Tempo Missing Spans Investigation",
    "PagerDuty Escalation Summary",
    "Incident Customer Impact Summary",
    "Cost Anomaly Egress Review",
    "SLO Burn Rate Review",
    "Backup Window Collision Check",
    "Queue Dead Letter Spike Triage",
    "Feature Flag Blast Radius Review",
    "Dependency Timeout RCA",
    "Synthetic Check Failure Triage",
    "Load Balancer 5xx Investigation",
    "DNS Resolution Latency Review",
    "NFS Stale Handle Triage",
    "Certificate Rotation Readiness",
    "Database Slow Query Review",
    "Index Bloat Capacity Review",
    "Host Kernel Error Sweep",
    "Container Restart Loop RCA",
    "Multi Region Failover Readiness",
    "Edge Fleet Upgrade Plan",
    "Webhook Delivery Failure Triage",
    "IM Notification Delivery Review",
    "Alert Noise Deduplication Review",
    "Incident Retrospective Draft",
    "Runbook Freshness Review",
    "Terraform Drift Summary",
    "GitHub Deployment Risk Review",
    "Secrets Rotation Readiness",
    "Vulnerability Advisory Impact Review",
    "License Expiry Sweep",
    "Monthly Executive Ops Review",
]

SESSION_TITLES = [
    "Investigate checkout latency after canary",
    "Explain payment authorization failures",
    "Review database replication lag",
    "Draft safe restart for checkout workers",
    "Build daily operations brief workflow",
    "Find Kubernetes image pull backoff cause",
    "Summarize APAC egress cost anomaly",
    "Prepare customer impact update",
    "Check fleet upgrade readiness",
    "Review release guardrail health",
]

MCP_SERVERS = [
    ("grafana-prod", "https://grafana.internal.example/mcp", True, ["query_dashboard", "list_alert_rules", "render_panel"]),
    ("prometheus-prod", "https://prometheus.internal.example/mcp", True, ["query_range", "instant_query", "list_targets"]),
    ("loki-prod", "https://loki.internal.example/mcp", True, ["logql_query", "tail_stream", "label_values"]),
    ("tempo-prod", "https://tempo.internal.example/mcp", True, ["trace_lookup", "service_graph", "span_search"]),
    ("kubernetes-prod", "https://k8s-control.internal.example/mcp", False, ["get_pods", "describe_deployment", "rollout_status"]),
    ("pagerduty-prod", "https://pagerduty.internal.example/mcp", False, ["list_incidents", "escalate_incident", "acknowledge_incident"]),
    ("github-org", "https://github-mcp.internal.example/mcp", False, ["search_code", "list_deployments", "open_pull_request"]),
    ("gitlab-internal", "https://gitlab-mcp.internal.example/mcp", False, ["list_pipelines", "compare_refs", "create_issue"]),
    ("postgres-readonly", "https://postgres-ro.internal.example/mcp", True, ["explain_query", "table_stats", "replication_lag"]),
    ("mysql-readonly", "https://mysql-ro.internal.example/mcp", True, ["slow_queries", "innodb_status", "replica_health"]),
    ("redis-inspector", "https://redis.internal.example/mcp", True, ["keyspace_stats", "memory_report", "hot_keys"]),
    ("kafka-admin", "https://kafka.internal.example/mcp", False, ["consumer_lag", "topic_config", "rebalance_status"]),
    ("terraform-cloud", "https://terraform.internal.example/mcp", False, ["plan_summary", "workspace_drift", "state_outputs"]),
    ("vault-secrets", "https://vault.internal.example/mcp", False, ["lease_status", "rotation_plan", "policy_lookup"]),
    ("cloudwatch-prod", "https://cloudwatch.internal.example/mcp", True, ["metric_search", "alarm_state", "log_insights"]),
    ("datadog-prod", "https://datadog.internal.example/mcp", True, ["query_timeseries", "service_map", "monitor_status"]),
    ("sentry-prod", "https://sentry.internal.example/mcp", True, ["issue_search", "release_health", "suspect_commits"]),
    ("jira-service-desk", "https://jira.internal.example/mcp", False, ["create_ticket", "search_issues", "transition_issue"]),
    ("slack-ops", "https://slack.internal.example/mcp", False, ["send_message", "create_channel", "thread_summary"]),
    ("statuspage-public", "https://statuspage.internal.example/mcp", False, ["component_status", "draft_update", "publish_update"]),
]

TOPOLOGY_NODES = [
    ("app", "Commerce Platform", {"owner_team": "commerce", "tier": "business"}),
    ("app", "Payments Platform", {"owner_team": "payments", "tier": "business"}),
    ("app", "Operator Console", {"owner_team": "sre", "tier": "business"}),
    ("service", "edge-gateway", {"slo": "99.95", "language": "go"}),
    ("service", "checkout-api", {"slo": "99.9", "language": "go"}),
    ("service", "checkout-worker", {"slo": "99.5", "language": "go"}),
    ("service", "payment-api", {"slo": "99.9", "language": "java"}),
    ("service", "order-api", {"slo": "99.9", "language": "go"}),
    ("service", "inventory-api", {"slo": "99.5", "language": "node"}),
    ("service", "notification-service", {"slo": "99.5", "language": "python"}),
    ("cluster", "k8s-prod-us-east", {"provider": "kubernetes", "region": "us-east"}),
    ("cluster", "mysql-commerce-primary", {"engine": "mysql", "role": "primary"}),
    ("cluster", "mysql-commerce-replica", {"engine": "mysql", "role": "replica"}),
    ("cluster", "redis-checkout-cache", {"engine": "redis", "role": "cache"}),
    ("cluster", "kafka-events", {"engine": "kafka", "partitions": 48}),
    ("cluster", "observability-stack", {"stack": "prometheus-loki-tempo-grafana"}),
    ("device", "edge-us-east-01", {"region": "us-east", "zone": "a", "status": "online"}),
    ("device", "edge-us-east-02", {"region": "us-east", "zone": "b", "status": "online"}),
    ("device", "edge-eu-west-01", {"region": "eu-west", "zone": "a", "status": "online"}),
    ("device", "edge-apac-cache-01", {"region": "apac", "zone": "cache", "status": "online"}),
    ("device", "db-primary-1", {"region": "us-east", "zone": "a", "status": "online"}),
    ("device", "db-read-1", {"region": "us-east", "zone": "b", "status": "online"}),
    ("device", "k8s-node-a", {"region": "us-east", "zone": "a", "status": "online"}),
    ("device", "k8s-node-b", {"region": "us-east", "zone": "b", "status": "online"}),
    ("device", "k8s-node-c", {"region": "us-east", "zone": "c", "status": "online"}),
    ("rack", "us-east-a", {"facility": "iad-01"}),
    ("rack", "us-east-b", {"facility": "iad-02"}),
    ("rack", "eu-west-a", {"facility": "dub-01"}),
    ("rack", "apac-cache-zone", {"facility": "sin-01"}),
]

TOPOLOGY_RELATIONS = [
    ("Commerce Platform", "edge-gateway", "routes_to"),
    ("Commerce Platform", "checkout-api", "depends_on"),
    ("Commerce Platform", "order-api", "depends_on"),
    ("Payments Platform", "payment-api", "depends_on"),
    ("Operator Console", "observability-stack", "depends_on"),
    ("edge-gateway", "checkout-api", "routes_to"),
    ("edge-gateway", "payment-api", "routes_to"),
    ("checkout-api", "checkout-worker", "depends_on"),
    ("checkout-api", "payment-api", "depends_on"),
    ("checkout-api", "order-api", "depends_on"),
    ("checkout-api", "inventory-api", "depends_on"),
    ("checkout-api", "redis-checkout-cache", "depends_on"),
    ("checkout-worker", "kafka-events", "depends_on"),
    ("order-api", "mysql-commerce-primary", "depends_on"),
    ("payment-api", "mysql-commerce-primary", "depends_on"),
    ("inventory-api", "mysql-commerce-replica", "depends_on"),
    ("notification-service", "kafka-events", "depends_on"),
    ("mysql-commerce-primary", "mysql-commerce-replica", "replicates_to"),
    ("mysql-commerce-primary", "db-primary-1", "deployed_on"),
    ("mysql-commerce-replica", "db-read-1", "deployed_on"),
    ("edge-gateway", "edge-us-east-01", "deployed_on"),
    ("edge-gateway", "edge-us-east-02", "deployed_on"),
    ("edge-gateway", "edge-eu-west-01", "deployed_on"),
    ("checkout-api", "k8s-prod-us-east", "deployed_on"),
    ("checkout-worker", "k8s-prod-us-east", "deployed_on"),
    ("payment-api", "k8s-prod-us-east", "deployed_on"),
    ("order-api", "k8s-prod-us-east", "deployed_on"),
    ("inventory-api", "k8s-prod-us-east", "deployed_on"),
    ("notification-service", "k8s-prod-us-east", "deployed_on"),
    ("k8s-prod-us-east", "k8s-node-a", "member_of"),
    ("k8s-prod-us-east", "k8s-node-b", "member_of"),
    ("k8s-prod-us-east", "k8s-node-c", "member_of"),
    ("observability-stack", "checkout-api", "monitors"),
    ("observability-stack", "payment-api", "monitors"),
    ("observability-stack", "mysql-commerce-primary", "monitors"),
    ("observability-stack", "kafka-events", "monitors"),
    ("edge-us-east-01", "us-east-a", "member_of"),
    ("edge-us-east-02", "us-east-b", "member_of"),
    ("edge-eu-west-01", "eu-west-a", "member_of"),
    ("edge-apac-cache-01", "apac-cache-zone", "member_of"),
    ("db-primary-1", "us-east-a", "member_of"),
    ("db-read-1", "us-east-b", "member_of"),
    ("k8s-node-a", "us-east-a", "member_of"),
    ("k8s-node-b", "us-east-b", "member_of"),
    ("k8s-node-c", "us-east-b", "member_of"),
]


def ts(minutes_ago: int = 0) -> str:
    return (BASE_TIME - dt.timedelta(minutes=minutes_ago)).strftime("%Y-%m-%d %H:%M:%S")


def rfc3339(minutes_ago: int = 0) -> str:
    return (BASE_TIME - dt.timedelta(minutes=minutes_ago)).strftime("%Y-%m-%dT%H:%M:%SZ")


def q(value: object) -> str:
    if value is None:
        return "NULL"
    if isinstance(value, bool):
        return "1" if value else "0"
    if isinstance(value, (int, float)):
        return str(value)
    s = str(value).replace("\\", "\\\\").replace("'", "''")
    return "'" + s + "'"


def dumps(value: object) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"))


def stable_uuid(name: str) -> str:
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f"ongrid/{SEED}/{name}"))


def table_exists(conn: sqlite3.Connection, table: str) -> bool:
    cur = conn.execute("SELECT name FROM sqlite_master WHERE type='table' AND name=?", (table,))
    return cur.fetchone() is not None


def sqlite_exec(db_path: str, statements: list[str]) -> None:
    conn = sqlite3.connect(db_path)
    try:
        cur = conn.cursor()
        cur.execute("PRAGMA foreign_keys = OFF")
        for statement in statements:
            stripped = statement.strip()
            if stripped:
                cur.execute(stripped)
        conn.commit()
    finally:
        conn.close()


def parse_mysql_dsn(dsn: str) -> dict[str, str]:
    m = re.match(r"(?P<user>[^:]+):(?P<password>.*?)@tcp\((?P<host>[^:)]+):(?P<port>\d+)\)/(?P<db>[^?]+)", dsn)
    if not m:
        raise ValueError("expected DSN like user:pass@tcp(host:3306)/database?...")
    out = m.groupdict()
    out["password"] = unquote(out["password"])
    out["db"] = out["db"].split("?")[0]
    return out


def mysql_exec(dsn: str, statements: list[str]) -> None:
    cfg = parse_mysql_dsn(dsn)
    sql = "SET FOREIGN_KEY_CHECKS=0;\n" + "\n".join(s.rstrip(";") + ";" for s in statements) + "\nSET FOREIGN_KEY_CHECKS=1;\n"
    cmd = [
        "mysql",
        "--protocol=TCP",
        "-h",
        cfg["host"],
        "-P",
        cfg["port"],
        "-u",
        cfg["user"],
        f"-p{cfg['password']}",
        cfg["db"],
    ]
    subprocess.run(cmd, input=sql, text=True, check=True)


def workflow_graph(index: int, title: str) -> dict[str, object]:
    trigger_type = "trigger.cron" if index % 3 == 0 else ("trigger.alert_fired" if index % 3 == 1 else "trigger.manual")
    nodes = [
        {"id": "trigger", "type": trigger_type, "name": "Trigger", "config": {"schedule": "0 9 * * *"} if trigger_type == "trigger.cron" else {}, "position": {"x": 0, "y": 120}},
        {"id": "collect", "type": "tool", "name": "Collect evidence", "config": {"tool": "prometheus_query", "args": {"query": "sum(rate(http_requests_total[5m])) by (service)"}}, "position": {"x": 260, "y": 120}},
        {"id": "context", "type": "agent", "name": "Retrieve context", "config": {"agent": "sre", "prompt": f"Find runbooks, recent changes, and topology for {title}."}, "position": {"x": 520, "y": 120}},
        {"id": "analyze", "type": "llm", "name": "Explain impact", "config": {"system": "You are an SRE assistant. Return concise RCA, blast radius, confidence, and next actions in English.", "prompt": "{{nodes.collect.output.result}}\n{{nodes.context.output.answer}}"}, "position": {"x": 780, "y": 120}},
        {"id": "gate", "type": "condition", "name": "Risk gate", "config": {"expr": "severity >= warning"}, "position": {"x": 1040, "y": 120}},
        {"id": "page", "type": "tool", "name": "Publish dark report", "config": {"tool": "serve_page", "args": {"title": title, "html": "{{nodes.analyze.output.answer}}"}}, "position": {"x": 1300, "y": 80}},
        {"id": "notify", "type": "notify", "name": "Notify owners", "config": {"channel": "slack-ops", "message": f"{title} is ready for review."}, "position": {"x": 1300, "y": 200}},
    ]
    edges = [
        {"id": "e1", "source": "trigger", "target": "collect"},
        {"id": "e2", "source": "collect", "target": "context"},
        {"id": "e3", "source": "context", "target": "analyze"},
        {"id": "e4", "source": "analyze", "target": "gate"},
        {"id": "e5", "source": "gate", "sourcePort": "true", "target": "page"},
        {"id": "e6", "source": "gate", "sourcePort": "true", "target": "notify"},
    ]
    return {"nodes": nodes, "edges": edges, "meta": {"seed": SEED, "locale": "en-US", "dark_theme": True}}


def page_html(index: int, title: str) -> str:
    score = 98 - (index % 17)
    severity = ["nominal", "watch", "degraded"][index % 3]
    accent = ["#7c3aed", "#10b981", "#38bdf8", "#f59e0b"][index % 4]
    rows = [
        ("Primary signal", f"{title} shows {severity} status with stable traffic."),
        ("Blast radius", "Commerce, payments, and operator console dependencies reviewed."),
        ("Evidence", "Metrics, logs, traces, topology, runbooks, recent changes, and source links correlated."),
        ("Action", "Keep read-only investigation active; require approval before any production change."),
    ]
    cards = [
        ("Health score", f"{score}/100"),
        ("Open risks", str(index % 5)),
        ("Evidence items", str(18 + index % 11)),
        ("Confidence", f"{82 + index % 15}%"),
    ]
    return f"""<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>{html.escape(title)}</title>
  <style>
    :root {{ color-scheme: dark; --accent: {accent}; --bg: #070b14; --panel: #101827; --line: #253044; --text: #e5edf8; --muted: #94a3b8; }}
    * {{ box-sizing: border-box; }}
    body {{ margin: 0; min-height: 100vh; background: var(--bg); color: var(--text); font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }}
    main {{ width: min(1180px, calc(100vw - 48px)); margin: 0 auto; padding: 34px 0 48px; }}
    header {{ border: 1px solid var(--line); background: #0c1220; border-radius: 8px; padding: 26px; }}
    .eyebrow {{ color: var(--accent); font-size: 12px; font-weight: 700; text-transform: uppercase; letter-spacing: .08em; }}
    h1 {{ margin: 10px 0 8px; font-size: 32px; line-height: 1.1; letter-spacing: 0; }}
    p {{ color: var(--muted); line-height: 1.6; margin: 0; }}
    .grid {{ display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; margin: 18px 0; }}
    .card {{ border: 1px solid var(--line); background: var(--panel); border-radius: 8px; padding: 18px; min-height: 96px; }}
    .label {{ color: var(--muted); font-size: 13px; }}
    .value {{ margin-top: 10px; font-size: 26px; font-weight: 750; }}
    .section {{ display: grid; grid-template-columns: 1.2fr .8fr; gap: 18px; margin-top: 18px; }}
    table {{ width: 100%; border-collapse: collapse; }}
    th, td {{ text-align: left; padding: 13px 0; border-bottom: 1px solid var(--line); vertical-align: top; }}
    th {{ color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .06em; }}
    td:first-child {{ width: 170px; color: var(--text); font-weight: 650; }}
    .timeline {{ display: grid; gap: 12px; }}
    .step {{ display: grid; grid-template-columns: 28px 1fr; gap: 10px; align-items: start; }}
    .dot {{ width: 10px; height: 10px; margin-top: 6px; border-radius: 50%; background: var(--accent); box-shadow: 0 0 0 5px rgba(124, 58, 237, .12); }}
    @media (max-width: 860px) {{ .grid {{ grid-template-columns: repeat(2, minmax(0, 1fr)); }} .section {{ grid-template-columns: 1fr; }} }}
  </style>
</head>
<body>
  <main>
    <header>
      <div class="eyebrow">Ongrid operational artifact</div>
      <h1>{html.escape(title)}</h1>
      <p>Dark-theme executive view generated for an SRE handoff. It summarizes signals, context, risk, and the next governed step without exposing mutable actions.</p>
    </header>
    <section class="grid">
      {''.join(f'<div class="card"><div class="label">{html.escape(k)}</div><div class="value">{html.escape(v)}</div></div>' for k, v in cards)}
    </section>
    <section class="section">
      <div class="card">
        <table>
          <thead><tr><th>Area</th><th>Summary</th></tr></thead>
          <tbody>{''.join(f'<tr><td>{html.escape(k)}</td><td>{html.escape(v)}</td></tr>' for k, v in rows)}</tbody>
        </table>
      </div>
      <div class="card">
        <div class="label">Investigation path</div>
        <div class="timeline">
          <div class="step"><span class="dot"></span><p>Collect metrics, logs, traces, host state, topology, and recent deployment changes.</p></div>
          <div class="step"><span class="dot"></span><p>Retrieve runbooks and incident history before drafting remediation.</p></div>
          <div class="step"><span class="dot"></span><p>Publish this artifact and require human approval for risky tools.</p></div>
        </div>
      </div>
    </section>
  </main>
</body>
</html>
"""


def write_pages(pages_dir: Path, wipe: bool) -> int:
    pages_dir.mkdir(parents=True, exist_ok=True)
    if wipe:
        for child in pages_dir.iterdir():
            if child.is_dir():
                shutil.rmtree(child)
            elif child.suffix == ".html":
                child.unlink()
    count = 0
    for i, title in enumerate(WORKFLOW_TITLES, start=1):
        page_id = f"0{i:023x}"[-24:]
        page_dir = pages_dir / page_id
        page_dir.mkdir(parents=True, exist_ok=True)
        body = page_html(i, title)
        (page_dir / "index.html").write_text(body, encoding="utf-8")
        meta = {
            "id": page_id,
            "title": title,
            "created_at": rfc3339(i * 7),
            "url": f"/api/pages/{page_id}",
            "size_bytes": len(body.encode("utf-8")),
            "source": "workflow" if i % 2 else "chat",
        }
        (page_dir / "meta.json").write_text(json.dumps(meta, ensure_ascii=True, separators=(",", ":")), encoding="utf-8")
        count += 1
    return count


def insert_statement(table: str, row: dict[str, object]) -> str:
    cols = ", ".join(row.keys())
    vals = ", ".join(q(v) for v in row.values())
    return f"INSERT INTO {table} ({cols}) VALUES ({vals});"


def replace_statement(table: str, row: dict[str, object]) -> str:
    cols = ", ".join(row.keys())
    vals = ", ".join(q(v) for v in row.values())
    return f"REPLACE INTO {table} ({cols}) VALUES ({vals});"


def delete_statement(table: str, where: str) -> str:
    return f"DELETE FROM {table} WHERE {where};"


def build_sql() -> list[str]:
    statements: list[str] = []
    han = "[一-龥]"
    demo_like = "%demo%"
    mcp_names = ",".join(q(name) for name, _, _, _ in MCP_SERVERS)

    statements.extend(
        [
            delete_statement("chat_tool_calls", f"message_id IN (SELECT id FROM chat_messages WHERE session_id IN (SELECT id FROM chat_sessions WHERE title REGEXP {q(han)} OR LOWER(title) LIKE {q(demo_like)} OR scope_json LIKE {q('%' + SEED + '%')}))"),
            delete_statement("chat_messages", f"session_id IN (SELECT id FROM chat_sessions WHERE title REGEXP {q(han)} OR LOWER(title) LIKE {q(demo_like)} OR scope_json LIKE {q('%' + SEED + '%')})"),
            delete_statement("chat_sessions", f"title REGEXP {q(han)} OR LOWER(title) LIKE {q(demo_like)} OR scope_json LIKE {q('%' + SEED + '%')}"),
            delete_statement("flow_run_nodes", "run_id IN (SELECT id FROM flow_runs WHERE flow_id BETWEEN 910001 AND 910050)"),
            delete_statement("flow_runs", "flow_id BETWEEN 910001 AND 910050"),
            delete_statement("flows", f"id BETWEEN 910001 AND 910050 OR name REGEXP {q(han)} OR description REGEXP {q(han)} OR LOWER(name) LIKE {q(demo_like)} OR graph_json LIKE {q('%' + SEED + '%')}"),
            delete_statement("mcp_servers", f"id BETWEEN 930001 AND 930020 OR name IN ({mcp_names}) OR name REGEXP {q(han)} OR LOWER(name) LIKE {q(demo_like)} OR tools_cache_json LIKE {q('%' + SEED + '%')}"),
            delete_statement("reports", f"id IN ({','.join(q(stable_uuid('report-' + str(i))) for i in range(1, 11))}) OR title REGEXP {q(han)} OR content_json LIKE {q('%' + SEED + '%')}"),
            delete_statement("relations", "src_id BETWEEN 920001 AND 920099 OR dst_id BETWEEN 920001 AND 920099"),
            delete_statement("nodes", f"id BETWEEN 920001 AND 920099 OR name REGEXP {q(han)} OR props_jsonb LIKE {q('%' + SEED + '%')}"),
        ]
    )

    for i, title in enumerate(WORKFLOW_TITLES, start=1):
        created = ts(i * 9)
        statements.append(
            insert_statement(
                "flows",
                {
                    "id": 910000 + i,
                    "name": title,
                    "description": f"English showcase workflow for {title.lower()}: collect evidence, retrieve context, publish a dark artifact, and notify owners.",
                    "graph_json": dumps(workflow_graph(i, title)),
                    "enabled": True,
                    "version": 1,
                    "created_by": OWNER_ID,
                    "created_at": created,
                    "updated_at": created,
                    "deleted_at": None,
                },
            )
        )
        run_id = stable_uuid(f"flow-run-{i}")
        statements.append(
            insert_statement(
                "flow_runs",
                {
                    "id": run_id,
                    "flow_id": 910000 + i,
                    "flow_version": 1,
                    "status": "succeeded",
                    "trigger_type": "manual",
                    "trigger_json": dumps({"seed": SEED, "locale": "en-US"}),
                    "error": "",
                    "created_by": OWNER_ID,
                    "started_at": ts(i * 9 + 2),
                    "finished_at": ts(i * 9),
                    "created_at": ts(i * 9 + 2),
                    "updated_at": ts(i * 9),
                },
            )
        )

    for i, title in enumerate(SESSION_TITLES, start=1):
        sid = stable_uuid(f"session-{i}")
        scope = dumps({"seed": SEED, "locale": "en-US", "view": "screenshot"})
        created = ts(600 + i * 13)
        statements.append(insert_statement("chat_sessions", {"id": sid, "user_id": OWNER_ID, "title": title, "scope_json": scope, "agent_id": None, "parent_session_id": None, "background": False, "related_incident_id": None, "kind": "user", "created_at": created, "updated_at": ts(590 + i * 13), "closed_at": None}))
        turns = [
            ("user", f"Use English and investigate: {title}. Include evidence, blast radius, confidence, and next actions."),
            ("assistant", f"I gathered observability signals, topology neighbors, runbook excerpts, recent deployments, and source references for {title}. The current recommendation is read-only investigation plus approval-gated remediation."),
            ("user", "Prepare a shareable dark artifact for the incident handoff."),
            ("assistant", "Published a dark-theme artifact with root cause, evidence table, risk gate, and owner-ready next steps."),
        ]
        for j, (role, content) in enumerate(turns, start=1):
            statements.append(insert_statement("chat_messages", {"id": stable_uuid(f"session-{i}-msg-{j}"), "session_id": sid, "role": role, "content": content, "tool_call_id": None, "tool_name": None, "model": "showcase", "prompt_tokens": 420 if role == "assistant" else None, "completion_tokens": 180 if role == "assistant" else None, "created_at": ts(600 + i * 13 - j)}))

    for i, (name, endpoint, trusted, tools) in enumerate(MCP_SERVERS, start=1):
        cache = [{"name": tool, "description": f"{tool.replace('_', ' ').title()} for the {name} MCP server.", "inputSchema": {"type": "object", "properties": {"query": {"type": "string"}}}} for tool in tools]
        statements.append(insert_statement("mcp_servers", {"id": 930000 + i, "name": name, "transport": "http", "endpoint": endpoint, "command": "", "args_json": "[]", "credential": f"{name}-credential", "header_template_json": dumps({"Authorization": "Bearer {{token}}"}), "trusted": trusted, "enabled": True, "tools_cache_json": dumps(cache), "status": "ok", "last_error": "", "created_by": OWNER_ID, "created_at": ts(60 + i), "updated_at": ts(i)}))

    node_types = [
        ("app", "App", 0, "Business capability or product surface."),
        ("service", "Service", 1, "Deployable process, container, or repository."),
        ("cluster", "Cluster", 2, "Stateful or orchestrated component group."),
        ("device", "Device", 3, "Host, VM, edge node, or infrastructure unit."),
        ("rack", "Failure Domain", 4, "Physical or logical blast-radius boundary."),
    ]
    for name, display, tier, desc in node_types:
        statements.append(replace_statement("node_types", {"name": name, "display_name": display, "display_name_en": display, "builtin": name in {"app", "service", "cluster", "device", "rack"}, "tier": tier, "description": desc, "created_at": ts(30), "updated_at": ts(30)}))

    relation_types = [
        ("member_of", "Member of", False, "src_to_dst", "aggregation"),
        ("depends_on", "Depends on", True, "dst_to_src", "hard_dep"),
        ("deployed_on", "Deployed on", True, "dst_to_src", "runtime_dep"),
        ("replicates_to", "Replicates to", False, "bidirectional", "redundancy"),
        ("monitors", "Monitors", False, "src_to_dst", "observation"),
        ("routes_to", "Routes to", True, "src_to_dst", "traffic"),
    ]
    for name, display, propagates, direction, semantics in relation_types:
        statements.append(replace_statement("relation_types", {"name": name, "display_name": display, "display_name_en": display, "builtin": True, "propagates_failure": propagates, "direction": direction, "semantics_tag": semantics, "description": f"Showcase relation type: {display.lower()}.", "created_at": ts(30), "updated_at": ts(30)}))

    node_ids: dict[str, int] = {}
    for i, (kind, name, props) in enumerate(TOPOLOGY_NODES, start=1):
        node_id = 920000 + i
        node_ids[name] = node_id
        p = dict(props)
        p.update({"seed": SEED, "locale": "en-US"})
        statements.append(insert_statement("nodes", {"id": node_id, "type": kind, "name": name, "props_jsonb": dumps(p), "created_at": ts(120 + i), "updated_at": ts(20 + i), "deleted_at": None}))

    for i, (src, dst, rel_type) in enumerate(TOPOLOGY_RELATIONS, start=1):
        statements.append(insert_statement("relations", {"id": 921000 + i, "src_id": node_ids[src], "dst_id": node_ids[dst], "type": rel_type, "props_jsonb": dumps({"seed": SEED, "confidence": "high"}), "created_at": ts(90 + i), "updated_at": ts(10 + i), "deleted_at": None, "delete_marker": 0}))

    for i, title in enumerate(WORKFLOW_TITLES[:10], start=1):
        rid = stable_uuid(f"report-{i}")
        content = {
            "seed": SEED,
            "theme": "dark",
            "title": title,
            "summary": f"{title} completed with evidence-backed recommendations.",
            "sections": [
                {"heading": "Signals", "items": ["Metrics stable", "No unresolved critical alerts", "Trace latency reviewed"]},
                {"heading": "Governance", "items": ["Read-only tools completed", "Risky changes require approval", "Audit trail retained"]},
            ],
        }
        statements.append(insert_statement("reports", {"id": rid, "schedule_id": None, "task_id": "", "run_id": "", "created_by": OWNER_ID, "title": title, "kind": "daily", "period_start": ts(1440 + i), "period_end": ts(i), "timezone": "UTC", "locale": "en-US", "scope_json": dumps({"seed": SEED}), "status": "ready", "error_msg": "", "content_json": dumps(content), "content_md": f"# {title}\n\nDark-theme showcase report generated in English.\n", "summary_text": f"{title} with evidence, blast radius, confidence, and next actions.", "generated_at": ts(i), "generated_by_model": "showcase", "prompt_tokens": 1200, "completion_tokens": 620, "audit_session_id": None, "worker_id": None, "share_token": None, "share_expires_at": None, "delivery_json": "[]", "created_at": ts(20 + i), "updated_at": ts(i), "deleted_at": None, "delete_marker": 0}))

    return statements


def mysql_safe_sql(statements: list[str]) -> list[str]:
    return statements


def sqlite_safe_sql(statements: list[str]) -> list[str]:
    out = []
    for s in statements:
        s = re.sub(r"(\w+)\s+REGEXP\s+'[^']+'", r"\1 GLOB '*[一-龥]*'", s)
        out.append(s)
    return out


def write_sql_file(path: Path, statements: list[str], dialect: str) -> None:
    prefix = "SET FOREIGN_KEY_CHECKS=0;\n" if dialect == "mysql" else "PRAGMA foreign_keys = OFF;\n"
    suffix = "SET FOREIGN_KEY_CHECKS=1;\n" if dialect == "mysql" else "PRAGMA foreign_keys = ON;\n"
    path.write_text(prefix + "\n".join(s.rstrip(";") + ";" for s in statements) + "\n" + suffix, encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dialect", choices=["mysql", "sqlite"], default=os.getenv("ONGRID_DB_DIALECT", "mysql"))
    parser.add_argument("--dsn", default=os.getenv("ONGRID_DB_DSN", "ongrid:ongrid@tcp(127.0.0.1:3306)/ongrid?parseTime=true&charset=utf8mb4&loc=Local"))
    parser.add_argument("--sqlite-path", default=os.getenv("ONGRID_DB_PATH", "./data/ongrid.db"))
    parser.add_argument("--pages-dir", default=os.getenv("ONGRID_PAGES_DIR", "/var/lib/ongrid/pages"))
    parser.add_argument("--sql-out", default="", help="Write SQL to this file instead of executing it.")
    parser.add_argument("--yes", action="store_true", help="Apply destructive cleanup and seed data.")
    parser.add_argument("--skip-db", action="store_true", help="Only write hosted pages; do not execute database SQL.")
    parser.add_argument("--keep-existing-pages", action="store_true", help="Do not wipe existing hosted pages before seeding dark pages.")
    args = parser.parse_args()

    statements = build_sql()
    statements = mysql_safe_sql(statements) if args.dialect == "mysql" else sqlite_safe_sql(statements)

    if args.sql_out:
        write_sql_file(Path(args.sql_out), statements, args.dialect)
        print(f"wrote SQL: {args.sql_out}")

    if not args.yes:
        print("dry-run: pass --yes to clean and seed the showcase environment")
        print("planned rows: 50 workflows, 50 dark hosted pages, 10 sessions, 20 MCP servers, 29 topology nodes, 45 topology relations, 10 reports")
        return 0

    page_count = write_pages(Path(args.pages_dir), wipe=not args.keep_existing_pages)
    if not args.skip_db:
        if args.dialect == "sqlite":
            sqlite_exec(args.sqlite_path, statements)
        else:
            mysql_exec(args.dsn, statements)

    print(f"seeded {page_count} dark hosted pages in {args.pages_dir}")
    if args.skip_db:
        print("skipped database seeding")
    else:
        print("seeded database showcase rows for workflows, sessions, MCP servers, topology, and reports")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
