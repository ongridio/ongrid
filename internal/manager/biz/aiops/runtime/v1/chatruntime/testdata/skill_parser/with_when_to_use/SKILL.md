---
name: log_search
description: Search Loki logs.
when_to_use: |
  When the user asks about log content, error patterns, or log volume per device.
  NOT for: file-system search (use file_inspector), metric queries (use metric_query).
tools:
  - name: query_logql
    impl: builtin:loki.Query
    class: read
    description: Run LogQL.
---

# Log Search

Use LogQL to search distributed logs.
