---
name: file_inspector
version: 1.0.0
description: Inspect files on the host filesystem.
metadata:
  os: ["darwin", "linux"]
  requires:
    bins: [find, du]
    config: [accountsPath]
tools:
  - name: find_files
    impl: builtin:filetools.Find
    class: read
    description: Search files by name pattern.
    when_to_use: When the user asks to find files.
  - name: du_summary
    impl: builtin:filetools.DuSummary
    class: read
    description: Disk usage summary.
---

# File Inspector

Use this skill to find files on disk and report disk usage.

## Workflow

1. Use `find_files` to locate candidate files.
2. Use `du_summary` to size up the matches.
