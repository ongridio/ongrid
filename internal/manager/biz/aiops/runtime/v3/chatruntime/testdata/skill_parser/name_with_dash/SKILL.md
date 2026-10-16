---
name: hello-world
description: Skill whose name uses a dash, expected to be normalized.
---

# Hello World Dashed

This skill name is `hello-world` which is not snake_case. The parser
should normalize it to `hello_world` and emit a warning.
