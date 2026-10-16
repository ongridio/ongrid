---
name: keyword_skill
description: Activated by the word "logs".
activation:
  mode: keyword
  keywords: [logs, 日志]
tools:
  - name: query_logs
    impl: builtin:loki.Query
    class: read
    description: Search logs.
  - name: delete_logs
    impl: builtin:loki.Delete
    class: destructive
    description: Wipe a log range.
---

# Keyword Skill

Only loaded when the user mentions logs / 日志.
