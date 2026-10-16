---
name: edge_disk_audit
description: Audit disk usage on edge devices.
metadata:
  os: [linux]
  requires:
    bins: [find, du]
  ongrid:
    scope: edge
    edge_runtime: subprocess
    edge_capabilities:
      - filesystem.read.path: ["/data", "/var/log"]
      - process.exec: [find, du]
    activation:
      mode: keyword
      keywords: [disk, 磁盘, du]
    min_ongrid_version: ">=0.7.30"
tools:
  - name: edge_du
    impl: edge:disk.DuSummary
    class: read
    description: Disk usage on edge.
---

# Edge Disk Audit

Inspect disk consumption on connected edges via the plugin runtime.
