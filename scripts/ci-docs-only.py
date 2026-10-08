#!/usr/bin/env python3
"""Keep heavy checks for changes outside known non-runtime documentation."""

import os
from pathlib import Path
import re
import subprocess


def docs_only(paths):
    prose = {".md", ".markdown", ".rst"}
    assets = {".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico", ".avif", ".pdf"}
    readme_dirs = {"api", "db", "deploy", "deploy/install", "dist", "examples/apm-languages",
                   "tests/e2e", "web", "web/e2e"}
    # Onboarding.tsx imports this guide as raw text; it changes the shipped UI.
    runtime_docs = {"docs/guides/apm-configuration-files.md"}
    # ponytail: known documentation locations only; add newly audited locations here.
    return bool(paths) and all(
        path not in runtime_docs and (
            ("/" not in path and (Path(path).suffix in prose or path == "LICENSE"))
            or (path.startswith("docs/") and Path(path).suffix in prose)
            or (path.startswith("docs/assets/") and Path(path).suffix in assets)
            or (str(Path(path).parent) in readme_dirs and re.fullmatch(r"README(?:[._-][^/]*)?\.md", Path(path).name))
            or re.fullmatch(r"\.github/(?:[^/]+\.md|(?:ISSUE_TEMPLATE|PULL_REQUEST_TEMPLATE)/[^/]+\.md|ISSUE_TEMPLATE/[^/]+\.ya?ml)", path)
        )
        for path in paths
    )


def changed_paths(base):
    # An absent base, new branch, or unavailable history means full CI.
    if not re.fullmatch(r"[0-9a-f]{40}", base):
        return []
    try:
        diff = subprocess.check_output(
            ["git", "diff", "--name-only", "--no-renames", "-z", base, "HEAD", "--"],
            stderr=subprocess.DEVNULL,
        )
    except subprocess.CalledProcessError:
        return []
    # Disable rename detection so moving code into docs still reports the old path.
    return [os.fsdecode(path) for path in diff.split(b"\0") if path]


def main():
    # Validate metadata even when the PR qualifies for the lightweight path.
    subprocess.run(["ruby", str(Path(__file__).with_name("check-issue-templates.rb"))], check=True)
    base = os.environ.get("CI_BASE_SHA", "")
    only_docs = docs_only(changed_paths(base))
    if only_docs:
        subprocess.run(["git", "diff", "--check", base, "HEAD", "--"], check=True)
    result = f"docs_only={str(only_docs).lower()}\n"
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        output.write(result)
    print("Documentation/template-only changes: metadata and whitespace checked; skipping heavy checks."
          if only_docs else "Running full CI: changes are not documentation-only or base is unavailable.")


if __name__ == "__main__":
    main()
