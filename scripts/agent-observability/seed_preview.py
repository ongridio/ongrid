"""Seed synthetic UI scenarios derived from real SDK exports into test Tempo."""
import base64
import copy
import hashlib
import json
import time
import urllib.request
from pathlib import Path

from google.protobuf.json_format import ParseDict
from opentelemetry.proto.collector.trace.v1.trace_service_pb2 import ExportTraceServiceRequest, ExportTraceServiceResponse

ROOT = Path(__file__).resolve().parents[2]
SDK_NAMES = ["langsmith", "openinference", "openllmetry", "langfuse"]
questions = [
    ("退货政策查询", "这笔订单还可以申请退货吗？", "可以。订单在 30 天退货期内，请保留收据并提交退货申请。"),
    ("产品资料检索", "整理新产品的核心能力和适用场景。", "已检索 3 份产品资料，核心能力包括自动诊断、知识检索和工具调用。"),
    ("代码变更检查", "检查这次修改是否影响请求超时处理。", "已检查调用链。超时仍由请求上下文控制，取消信号可以传到下游。"),
]
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
now = time.time_ns()
previous = None
manifest = []


def put(attrs, key, value):
    for attr in attrs:
        if attr["key"] == key:
            attr["value"] = {"stringValue": value}
            return
    attrs.append({"key": key, "value": {"stringValue": value}})


for index in range(24):
    sdk = SDK_NAMES[index % 4]
    body = copy.deepcopy(json.loads((ROOT / f"internal/manager/biz/agenttrace/testdata/sdk/{sdk}.json").read_text()))
    trace_bytes = hashlib.sha256(f"ongrid-agent-preview-{now}-{index}".encode()).digest()[:16]
    trace_id = base64.b64encode(trace_bytes).decode()
    title, question, answer = questions[index % 3]
    resources = body["resourceSpans"]
    spans = [span for resource in resources for scope in resource["scopeSpans"] for span in scope["spans"]]
    root = next(span for span in spans if not span.get("parentSpanId"))
    original_start = min(int(span["startTimeUnixNano"]) for span in spans)
    original_end = max(int(span["endTimeUnixNano"]) for span in spans)
    start = now - (index + 1) * 45_000_000_000
    target_duration = 780_000_000 + (index % 9) * 310_000_000
    scale = target_duration / max(1, original_end - original_start)
    for resource in resources:
        attrs = resource["resource"]["attributes"]
        put(attrs, "service.name", ["support-agent", "research-assistant", "code-review-agent"][index % 3])
        put(attrs, "service.namespace", "agent-preview")
        put(attrs, "deployment.environment.name", "synthetic-demo")
        for scope in resource["scopeSpans"]:
            for span in scope["spans"]:
                span["traceId"] = trace_id
                span["startTimeUnixNano"] = str(start + int((int(span["startTimeUnixNano"]) - original_start) * scale))
                span["endTimeUnixNano"] = str(start + int((int(span["endTimeUnixNano"]) - original_start) * scale))
                attrs = span.setdefault("attributes", [])
                for attr in attrs:
                    if attr["key"] in ["session.id", "langsmith.metadata.thread_id", "traceloop.association.properties.session_id", "traceloop.association.properties.thread_id"]:
                        attr["value"] = {"stringValue": f"conversation-{index % 6 + 1042}"}
                if any(attr["key"] in ["llm.token_count.prompt", "gen_ai.usage.input_tokens", "langfuse.observation.usage_details"] for attr in attrs):
                    put(attrs, "gen_ai.response.model", "fixture-model")
                if "search_knowledge" in span["name"] and index % 6 == 0:
                    span["status"] = {"code": "STATUS_CODE_ERROR", "message": "Synthetic retry example"}
                else:
                    span["status"] = {"code": "STATUS_CODE_OK"}
    root["name"] = f"{title} · #{10824 + index}"
    if index == 23:
        root["name"] += " / a-very-long-application-run-name-for-responsive-layout-verification-2026"
    put(root["attributes"], "input.value", question)
    put(root["attributes"], "output.value", answer)
    if index % 7 == 0:
        root["status"] = {"code": "STATUS_CODE_ERROR", "message": "Synthetic validation error"}
        put(root["attributes"], "output.value", "检索服务暂时不可用。本条记录为合成错误样例。")
    if previous and index % 3 == 1:
        root["links"] = [{"traceId": previous[0], "spanId": previous[1], "attributes": [{"key": "relation", "value": {"stringValue": "synthetic-follow-up"}}]}]
    previous = (trace_id, root["spanId"])
    payload = ParseDict(body, ExportTraceServiceRequest()).SerializeToString()
    request = urllib.request.Request("http://127.0.0.1:14318/v1/traces", data=payload, headers={"Content-Type": "application/x-protobuf"})
    with opener.open(request, timeout=10) as response:
        assert response.status == 200
        result = ExportTraceServiceResponse.FromString(response.read())
        assert result.partial_success.rejected_spans == 0, result.partial_success.error_message
    manifest.append({"sdk": sdk, "trace_id": trace_bytes.hex(), "name": root["name"]})

Path("/private/tmp/ongrid-agent-preview-manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2))
print(f"Seeded {len(manifest)} synthetic traces into isolated Tempo on port 14318")
