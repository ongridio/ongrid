#!/usr/bin/env python3
"""Regressions for lightweight CI validation and the aggregate web check."""

import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile

script = Path(__file__).with_name("ci-docs-only.py").resolve()
docs_only = runpy.run_path(str(script))["docs_only"]
assert docs_only(["AGENTS.md", "README_zh.md", "docs/design/guide.md"])
assert docs_only(["CHANGELOG.md", "ROADMAP.zh-CN.md", "LICENSE", "docs/guide.rst"])
assert docs_only(["web/README.md", "deploy/install/README.md", "api/README.md", "web/e2e/README.md"])
assert docs_only(["docs/assets/demo.gif", "docs/assets/integrations/openai.svg", "docs/assets/guide.pdf"])
assert docs_only([".github/pull_request_template.md", ".github/PULL_REQUEST_TEMPLATE/feature.md"])
assert docs_only([".github/ISSUE_TEMPLATE/legacy.md"])
assert docs_only([".github/ISSUE_TEMPLATE/config.yml", ".github/ISSUE_TEMPLATE/bug.yaml"])
for paths in [[], ["main.go"], ["Makefile"], [".github/workflows/ci.yml"],
              ["internal/manager/biz/knowledge/builtin_vault/guide.md"],
              ["internal/manager/biz/aiops/chatruntime/testdata/agent_registry/multi/README.md"],
              ["docs/guides/apm-configuration-files.md"], ["agents/reviewer.md"], ["skills/bash/SKILL.md"],
              ["web/src/guide.md"], ["web/public/logo.svg"], ["new-package/README.md"],
              ["docs/config.yml"], ["docs/assets/app.js"], ["docs/example.go"],
              ["go.sum"], ["web/package-lock.json"], ["deploy/install/install.sh"], ["VERSION"],
              ["AGENTS.md", "main.go"], ["docs/assets/demo.gif", "web/src/App.tsx"],
              [".github/ISSUE_TEMPLATE/script.py"], [".github/ISSUE_TEMPLATE/nested/config.yml"],
              [".github/ISSUE_TEMPLATE/config.yml", ".github/workflows/ci.yml"]]:
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
    (root / "docs" / "assets").mkdir()
    (root / "docs" / "assets" / "demo.gif").write_bytes(b"GIF89a")
    (root / "CHANGELOG.md").write_text("Release notes\n")
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

    templates = root / ".github" / "ISSUE_TEMPLATE"
    templates.mkdir(parents=True)
    template = templates / "bug.yml"
    valid = "name: Bug report\ndescription: Report a bug\nbody:\n  - type: input\n    id: summary\n    attributes:\n      label: Summary\n"
    template.write_text(valid)
    template_base = commit()
    check(deleted, True)
    for invalid in ["body: [", "name: Bug report\ndescription: Report a bug\nbody: []\n",
                    valid.replace("type: input", "type: unknown"),
                    valid + "  - type: input\n    id: summary\n    attributes:\n      label: Duplicate\n",
                    valid.replace("type: input", "type: dropdown") + "      options: [true]\n"]:
        template.write_text(invalid)
        commit()
        check(template_base, True, success=False)
        check("", False, success=False)  # Main pushes must validate templates too.
    template.write_text(valid)
    commit()
    check(template_base, False)
    chooser = templates / "config.yml"
    chooser.write_text("blank_issues_enabled: true\ncontact_links:\n  - name: Help\n    url: file:///invalid\n    about: Help\n")
    commit()
    check(template_base, True, success=False)
    chooser.unlink()
    commit()

    (root / "docs" / "bad.md").write_text("Trailing whitespace \n")
    commit()
    check(deleted, True, success=False)

# A failed, cancelled, or skipped matrix must never satisfy the old required check.
workflow = script.parent.parent / ".github" / "workflows" / "ci.yml"
jobs = json.loads(subprocess.check_output([
    "ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV[0]))", str(workflow)
]))["jobs"]
gate = jobs["web-test"]
assert gate["name"] == "web test + build"
assert gate["needs"] == "web-checks" and gate["if"] == "always()"
for result in ["success", "failure", "cancelled", "skipped", ""]:
    status = subprocess.run(["bash", "-c", gate["steps"][0]["run"]],
                            env={**os.environ, "WEB_RESULT": result}).returncode
    assert (status == 0) == (result == "success"), result

print("CI routing, template validation, and web gate checks passed")
