#!/usr/bin/env python3
"""Keep heavy PR checks for everything outside the documentation allowlist."""

import os
from pathlib import Path
import re
import subprocess


def docs_only(paths):
    root_docs = {"AGENTS.md", "README.md", "CONTRIBUTING.md", "CODE_OF_CONDUCT.md", "SECURITY.md"}
    return bool(paths) and all(
        path in root_docs
        or ("/" not in path and path.startswith("README_") and path.endswith(".md"))
        or (path.startswith("docs/") and path.endswith(".md"))
        for path in paths
    )


def changed_paths(base):
    # An absent base (including main pushes) or unavailable history means full CI.
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
    base = os.environ.get("CI_BASE_SHA", "")
    only_docs = docs_only(changed_paths(base))
    if only_docs:
        subprocess.run(["git", "diff", "--check", base, "HEAD", "--"], check=True)
    result = f"docs_only={str(only_docs).lower()}\n"
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        output.write(result)
    print("Documentation-only PR: whitespace checked; skipping Go/web builds and tests."
          if only_docs else "Running full CI: changes are not documentation-only or base is unavailable.")


if __name__ == "__main__":
    main()
