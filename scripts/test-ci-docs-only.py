#!/usr/bin/env python3
"""Regression check for the docs fast path, using real Git diffs."""

import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile

script = Path(__file__).with_name("ci-docs-only.py").resolve()
docs_only = runpy.run_path(str(script))["docs_only"]
assert docs_only(["AGENTS.md", "README_zh.md", "docs/design/guide.md"])
for paths in [[], ["main.go"], ["Makefile"], [".github/workflows/ci.yml"],
              ["internal/manager/biz/knowledge/builtin_vault/guide.md"],
              ["docs/config.yml"], ["web/README.md"], ["AGENTS.md", "main.go"]]:
    assert not docs_only(paths), paths

with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)

    def git(*args):
        return subprocess.check_output(["git", *args], cwd=root, stderr=subprocess.DEVNULL).decode().strip()

    def commit():
        git("add", "-A")
        git("-c", "user.name=CI", "-c", "user.email=ci@example.invalid",
            "-c", "commit.gpgsign=false", "commit", "-qm", "test")
        return git("rev-parse", "HEAD")

    def check(base, expected, success=True):
        output = root / ".git" / "outputs"
        output.write_text("")
        result = subprocess.run([sys.executable, str(script)], cwd=root, capture_output=True,
                                env={**os.environ, "CI_BASE_SHA": base, "GITHUB_OUTPUT": str(output)})
        assert (result.returncode == 0) == success, result.stderr.decode()
        assert output.read_text() == (f"docs_only={str(expected).lower()}\n" if success else "")

    git("init", "-q")
    (root / "docs").mkdir()
    (root / "docs" / "guide.md").write_text("Guide\n")
    (root / "main.go").write_text("package main\n")
    base = commit()
    check(base, False)  # Empty diff.
    (root / "docs" / "guide.md").write_text("Updated guide\n")
    commit()
    check(base, True)
    for missing in ["", "--help", "0" * 40]:
        check(missing, False)
    git("mv", "main.go", "docs/code.md")
    renamed = commit()
    check(base, False)  # A rename must not hide the removed code path.
    git("rm", "docs/guide.md")
    deleted = commit()
    check(renamed, True)
    (root / "docs" / "space and\nnewline.md").write_text("Guide\n")
    commit()
    check(deleted, True)  # NUL-separated paths, including whitespace in names.
    (root / "docs" / "bad.md").write_text("Trailing whitespace \n")
    commit()
    check(deleted, True, success=False)

print("CI documentation routing checks passed")
