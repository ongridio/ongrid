#!/usr/bin/env python3
"""Extract the CMDB icon font from a nexus-cantor checkout.

WHY THIS IS A SCRIPT AND NOT A COPY-PASTE: the glyphs live inside two 3 MB
`iconfont.js` sprites (iconfont.cn's `_iconfont_svg_string_*` payload), and the
console only ever offers a curated subset of them. Copying the sprites wholesale
would ship ~1400 symbols of which most are unrelated to a CMDB (oneterm, opsmgt,
service desk...). This pulls exactly the ids the cantor icon picker offers.

USAGE
    python3 web/scripts/extract-cmdb-icons.py /path/to/nexus-cantor
    → writes web/src/cmdb/assets/cmdb-icons.json

SOURCE OF TRUTH, in the cantor checkout:
    cmdb-ui/src/components/custom-icon-select/constants.ts   the curated id list
    cmdb-ui/public/iconfont/iconfont.js                      the sprites
    cmdb-ui/public/iconfont/iconfont-extend.js

The output lands in web/public/ and is fetched once, lazily, by the console — so
it costs the JS bundle nothing and a page that draws no model icon never asks for
it. See web/src/cmdb/components/CMDBIcon.tsx.

This script is not run by CI: it reads a different repository, so it is a
documented regeneration step, not a build step. Re-run it when cantor's icon set
changes.
"""
import json
import os
import re
import sys

ID_RE = re.compile(r"value: '([^']+)'")
SYMBOL_RE = re.compile(
    r'<symbol id="([^"]+)" viewBox="([^"]*)">(.*?)</symbol>', re.S
)


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    root = os.path.join(sys.argv[1], "cmdb-ui")
    constants = os.path.join(root, "src/components/custom-icon-select/constants.ts")
    sprites = [
        os.path.join(root, "public/iconfont/iconfont.js"),
        os.path.join(root, "public/iconfont/iconfont-extend.js"),
    ]
    for path in [constants, *sprites]:
        if not os.path.isfile(path):
            print(f"missing: {path}", file=sys.stderr)
            return 1

    # DEDUPLICATED: a `value:` field is also used for the picker's CATEGORY and
    # tab keys, which are not icon ids — dedupe keeps the report about icons.
    wanted = sorted(set(ID_RE.findall(open(constants, encoding="utf-8").read())))

    symbols = {}
    for path in sprites:
        raw = open(path, encoding="utf-8", errors="ignore").read()
        for name, view_box, content in SYMBOL_RE.findall(raw):
            symbols[name] = {"v": view_box or "0 0 1024 1024", "c": content.strip()}

    picked = {name: symbols[name] for name in wanted if name in symbols}
    missing = [name for name in wanted if name not in symbols]

    out = os.path.join(
        os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
        "public/cmdb/cmdb-icons.json",
    )
    with open(out, "w", encoding="utf-8") as handle:
        json.dump(picked, handle, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
        handle.write("\n")

    size = os.path.getsize(out)
    print(f"wrote {len(picked)} of {len(wanted)} icons to {out} ({size / 1024:.0f} KiB)")
    if missing:
        # REPORTED, NOT SILENT. These ids are offered by cantor's picker and have
        # no glyph in the sprites, so they fall back to the default icon on both
        # sides — worth knowing rather than discovering from a blank square.
        print(f"{len(missing)} ids have no glyph: {', '.join(sorted(missing))}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
