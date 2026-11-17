# Saved-result analysis fixture

This read-only MCP server returns 25,000 synthetic projects in native JSON, legacy JSON-in-text, and quoted CSV. A second 25,000-row snapshot supports joins. It makes no external calls and contains no real business data.

```bash
python3 scripts/result-analysis/fixture.py --self-test
python3 scripts/result-analysis/verify.py --self-test
python3 scripts/result-analysis/fixture.py
```

The server listens on port 8000 at `/mcp`; run it only in an isolated test network. Register it as `analysis_demo` in the test app and enable trusted calls only for this read-only fixture. Enable result capture on the Linux Manager as described in `docs/install/tool-results.md`.

Send `cases.json` questions through the normal chat UI. Cases with the same `session` continue in one conversation; use a new conversation for the other groups. The questions request data and answer fields but do not name the analyzer, supply expressions, or reveal expected answers. Preserve all attempts and errors. Record the model, reasoning level, result IDs, source/analysis call counts, coverage and final answers. Confirm that follow-up questions do not repeat source calls.

Export final assistant text into a JSON object keyed by case ID, then run:

```bash
python3 scripts/result-analysis/verify.py answers.json
```

The verifier compares exact answers against independent SQLite queries over the deterministic fixtures, including every matching row and the ordered top five. Python integers and SQLite INTEGER preserve IDs above 2^53; costs are integer cents. The tests cover compound filtering, grouping with nulls, tied rankings, missing values, duplicate keys, absence, quoted multiline CSV, totals, and snapshot joins. The self-test deliberately changes an ID to ensure the comparator detects a mismatch.

Do not execute expressions copied from a conversation outside the bounded analyzer. Inspect saved analysis provenance through the result viewer. All fixtures together fit the existing 16 MiB per-MCP-response limit; each snapshot has 25,000 records. These cases check this synthetic workload, not general semantic accuracy or production capacity.

To check the actual Linux binary, including bounded failure cases:

```bash
python3 scripts/result-analysis/check_worker.py -- docker exec -i -e HOME=/tmp -e GOMAXPROCS=1 -e GOTRACEBACK=none ongrid /ongrid
```

`memory_check.py` is an assert-based benchmark intended to run inside the existing Linux test Manager container. It uses the Python already present in the test image only as a measurement harness; production analysis uses Go. It sends 25,000 and 250,000 synthetic records, checks exact counts/sums and complete coverage, and samples `/proc/<worker-pid>/status` after exec every millisecond. It excludes the fixture generator and parent process. Short-lived peaks between samples may be missed; this is evidence for these queries, not a memory guarantee for arbitrary expressions.

```bash
docker exec -i ongrid python3 - < scripts/result-analysis/memory_check.py
```
