#!/usr/bin/env python3
"""Check a captured Tempo trace against an explicit end-to-end expectation.

Usage: trace-continuity.py TRACE.json EXPECTATION.json
EXPECTATION: {"trace_id": "32 hex", "root_parent_ids": ["16 hex"],
  "expected": [{"service": "orders", "kind": "SERVER", "count": 1}],
  "spans": [{"span_id": "16 hex", "parent_span_id": "16 hex"}],
  "max_span_duration_ms": 60000,
  "wire": [{"traceparents": ["00-...-...-01"], "expected": "00-...-...-01"}]}
Use --self-test to check the verifier without a live backend.
"""

import base64
import json
import re
import sys
import unittest
from collections import Counter


def identifier(value, size):
    if not value:
        return "0" * (size * 2)
    if re.fullmatch(r"[0-9a-fA-F]{%d}" % (size * 2), value):
        return value.lower()
    raw = base64.b64decode(value, validate=True)
    if len(raw) != size:
        raise ValueError("invalid trace/span ID length")
    return raw.hex()


def span_kind(value):
    if isinstance(value, int):
        return {0: "UNSPECIFIED", 1: "INTERNAL", 2: "SERVER", 3: "CLIENT",
                4: "PRODUCER", 5: "CONSUMER"}[value]
    return value.removeprefix("SPAN_KIND_")


def verify(document, expectation):
    trace_id = identifier(expectation["trace_id"], 16)
    external = {identifier(value, 8) for value in expectation.get("root_parent_ids", [])}
    spans = []
    for batch in document.get("batches", document.get("resourceSpans", [])):
        attrs = {a["key"]: a["value"].get("stringValue")
                 for a in batch.get("resource", {}).get("attributes", [])}
        for scope in batch.get("scopeSpans", []):
            for span in scope.get("spans", []):
                spans.append({"id": identifier(span["spanId"], 8),
                              "parent": identifier(span.get("parentSpanId"), 8),
                              "trace": identifier(span["traceId"], 16),
                              "service": attrs.get("service.name", ""),
                              "kind": span_kind(span.get("kind", 0)),
                              "name": span.get("name", ""),
                              "start": int(span.get("startTimeUnixNano", 0)),
                              "end": int(span.get("endTimeUnixNano", 0))})
    errors = []
    if not spans:
        errors.append("trace is empty")
    ids = Counter(s["id"] for s in spans)
    for value, count in ids.items():
        if count != 1 or value == "0" * 16:
            errors.append(f"invalid or repeated Span ID: {value} ({count})")
    parents = {s["id"]: s["parent"] for s in spans}
    for span in spans:
        max_duration = expectation.get("max_span_duration_ms")
        if max_duration is not None and (span["start"] <= 0 or span["end"] < span["start"]
                or span["end"] - span["start"] > max_duration * 1_000_000):
            errors.append(f"invalid span timing: {span['id']}")
        if span["trace"] != trace_id:
            errors.append(f"Trace ID changed: {span['id']}")
        if span["parent"] not in ids and span["parent"] not in external and span["parent"] != "0" * 16:
            errors.append(f"missing parent: {span['id']} -> {span['parent']}")
        seen, cursor = set(), span["id"]
        while cursor in parents:
            if cursor in seen:
                errors.append(f"parent cycle: {span['id']}")
                break
            seen.add(cursor)
            cursor = parents[cursor]
    for expected in expectation["expected"]:
        count = sum(all(span[key] == expected[key] for key in ("service", "kind", "name") if key in expected)
                    for span in spans)
        if count != expected["count"]:
            errors.append(f"operation count {count}, expected {expected}")
    for expected in expectation.get("spans", []):
        value = identifier(expected["span_id"], 8)
        if value not in parents or parents[value] != identifier(expected.get("parent_span_id"), 8):
            errors.append(f"SDK Span ID or parent changed: {value}")
    for wire in expectation.get("wire", []):
        if wire["traceparents"] != [wire["expected"]]:
            errors.append(f"wire context changed or duplicated: {wire['traceparents']}")
    return {"status": "FAIL" if errors else "PASS", "trace_id": trace_id,
            "span_count": len(spans), "errors": errors}


class VerifierTest(unittest.TestCase):
    def setUp(self):
        self.trace_id = "11" * 16
        self.parent, self.child = "22" * 8, "33" * 8
        self.spans = [{"traceId": self.trace_id, "spanId": self.parent, "kind": 2},
                      {"traceId": self.trace_id, "spanId": self.child,
                       "parentSpanId": self.parent, "kind": "SPAN_KIND_INTERNAL", "name": "custom"}]
        self.document = {"batches": [{"resource": {"attributes": [
            {"key": "service.name", "value": {"stringValue": "app"}}]},
            "scopeSpans": [{"spans": self.spans}]}]}
        self.expected = {"trace_id": self.trace_id, "expected": [
            {"service": "app", "kind": "SERVER", "count": 1},
            {"service": "app", "name": "custom", "count": 1}]}

    def test_valid_trace_and_base64_ids(self):
        self.spans[0]["spanId"] = base64.b64encode(bytes.fromhex(self.parent)).decode()
        self.assertEqual("PASS", verify(self.document, self.expected)["status"])

    def test_missing_parent(self):
        self.spans[1]["parentSpanId"] = "44" * 8
        self.assertTrue(any("missing parent" in e for e in verify(self.document, self.expected)["errors"]))

    def test_duplicate_operation_with_different_ids(self):
        self.spans.append({"traceId": self.trace_id, "spanId": "44" * 8, "kind": 2})
        self.assertEqual("FAIL", verify(self.document, self.expected)["status"])

    def test_missing_custom_span(self):
        self.spans.pop()
        self.assertEqual("FAIL", verify(self.document, self.expected)["status"])

    def test_cycle(self):
        self.spans[0]["parentSpanId"] = self.child
        self.assertTrue(any("parent cycle" in e for e in verify(self.document, self.expected)["errors"]))

    def test_span_timing(self):
        self.expected["max_span_duration_ms"] = 1000
        for span in self.spans:
            span.update(startTimeUnixNano="1000000000", endTimeUnixNano="1500000000")
        self.assertEqual("PASS", verify(self.document, self.expected)["status"])
        for start, end in [(0, 1), (20, 10), (1, 2_000_000_000)]:
            with self.subTest(start=start, end=end):
                self.spans[0].update(startTimeUnixNano=str(start), endTimeUnixNano=str(end))
                self.assertTrue(any("invalid span timing" in error
                                    for error in verify(self.document, self.expected)["errors"]))

    def test_duplicate_wire_context(self):
        self.expected["wire"] = [{"traceparents": ["sdk", "obi"], "expected": "sdk"}]
        self.assertEqual("FAIL", verify(self.document, self.expected)["status"])


if __name__ == "__main__":
    if sys.argv[1:] == ["--self-test"]:
        unittest.main(argv=[sys.argv[0]])
    elif len(sys.argv) == 3:
        with open(sys.argv[1]) as trace_file, open(sys.argv[2]) as expectation_file:
            result = verify(json.load(trace_file), json.load(expectation_file))
        print(json.dumps(result, indent=2))
        sys.exit(result["status"] != "PASS")
    else:
        sys.exit(__doc__)
