"""Capture real SDK OTLP exports without a model key or external collector.

Run each SDK in a fresh process using requirements.lock. Payloads contain only
the synthetic support question below. No production environment is read.
"""
import json
import os
import sys
from pathlib import Path
from typing import TypedDict

from google.protobuf.json_format import MessageToDict
from opentelemetry.exporter.otlp.proto.common.trace_encoder import encode_spans
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor, SpanExporter, SpanExportResult


class Capture(SpanExporter):
    def __init__(self):
        self.spans = []

    def export(self, spans):
        self.spans.extend(spans)
        return SpanExportResult.SUCCESS

    def shutdown(self):
        pass


SDK = sys.argv[1]
resources = {
    "service.name": "support-agent",
    "service.namespace": "agent-sdk-fixtures",
    "deployment.environment.name": "test",
}
# Explicit test settings prevent an inherited SDK configuration from exporting.
for key in list(os.environ):
    if key.lower().endswith("_proxy") or key.startswith(("LANGSMITH_", "LANGCHAIN_", "LANGFUSE_", "TRACELOOP_", "OTEL_")):
        del os.environ[key]
os.environ["OTEL_SERVICE_NAME"] = resources["service.name"]
os.environ["OTEL_RESOURCE_ATTRIBUTES"] = ",".join(f"{k}={v}" for k, v in resources.items())
capture = Capture()
provider = TracerProvider(resource=Resource.create(resources))
if SDK != "langfuse":
    provider.add_span_processor(SimpleSpanProcessor(capture))


def graph_run(callbacks=None):
    from langchain_core.language_models.fake_chat_models import FakeMessagesListChatModel
    from langchain_core.messages import AIMessage, HumanMessage
    from langchain_core.tools import tool
    from langgraph.graph import END, START, StateGraph

    class State(TypedDict):
        question: str
        context: str
        answer: str

    @tool
    def search_knowledge(question: str) -> str:
        """Find the synthetic returns policy."""
        return "Returns are accepted within 30 days with a receipt."

    model = FakeMessagesListChatModel(responses=[AIMessage(
        content="You can return the order within 30 days with your receipt.",
        usage_metadata={"input_tokens": 42, "output_tokens": 16, "total_tokens": 58},
        response_metadata={"model_name": "fixture-model"},
    )])

    def retrieve(state):
        return {"context": search_knowledge.invoke({"question": state["question"]})}

    def answer(state):
        result = model.invoke([HumanMessage(content=state["question"] + "\n" + state["context"])])
        return {"answer": result.content}

    builder = StateGraph(State)
    builder.add_node("retrieve", retrieve)
    builder.add_node("answer", answer)
    builder.add_edge(START, "retrieve")
    builder.add_edge("retrieve", "answer")
    builder.add_edge("answer", END)
    return builder.compile().invoke(
        {"question": "Can I return an order?", "context": "", "answer": ""},
        config={"callbacks": callbacks or [], "metadata": {"thread_id": "fixture-session"}},
    )


if SDK == "langsmith":
    from langsmith import Client, tracing_context
    from langchain_core.tracers.langchain import LangChainTracer

    client = Client(tracing_mode="otel", otel_tracer_provider=provider)
    with tracing_context(enabled=True, client=client, metadata={"thread_id": "fixture-session"}):
        graph_run([LangChainTracer(client=client)])
    client.flush()
elif SDK == "openinference":
    from openinference.instrumentation import using_attributes
    from openinference.instrumentation.langchain import LangChainInstrumentor

    LangChainInstrumentor().instrument(tracer_provider=provider)
    with using_attributes(session_id="fixture-session"):
        graph_run()
elif SDK == "openllmetry":
    from traceloop.sdk import Traceloop
    from traceloop.sdk.instruments import Instruments

    Traceloop.init(app_name=resources["service.name"], exporter=capture,
                  resource_attributes=resources, telemetry_enabled=False,
                  disable_batch=True, instruments={Instruments.LANGCHAIN})
    Traceloop.set_association_properties({"session_id": "fixture-session"})
    graph_run()
elif SDK == "langfuse":
    from langfuse import Langfuse, observe, propagate_attributes

    os.environ["LANGFUSE_PUBLIC_KEY"] = "pk-lf-local-fixture"
    os.environ["LANGFUSE_SECRET_KEY"] = "sk-lf-local-fixture"
    client = Langfuse(base_url="http://127.0.0.1:1", tracer_provider=provider,
                      span_exporter=capture, should_export_span=lambda span: True,
                      environment="test")

    @observe(name="support-agent", as_type="agent")
    def run(question):
        with client.start_as_current_observation(as_type="tool", name="search_knowledge", input=question) as tool:
            tool.update(output="Returns are accepted within 30 days.")
        with client.start_as_current_observation(as_type="generation", name="answer", model="fixture-model", input=question) as generation:
            generation.update(output="You can return your order within 30 days.",
                              usage_details={"input": 42, "output": 16, "total": 58},
                              cost_details={"total": 0.0001})
        return "You can return your order within 30 days."

    with propagate_attributes(session_id="fixture-session"):
        run("Can I return an order?")
    client.flush()
    client.shutdown()
else:
    raise ValueError(f"Unknown SDK: {SDK}")

provider.force_flush()
assert len(capture.spans) >= 3, f"{SDK} did not export a complete sample"
body = MessageToDict(encode_spans(capture.spans))
target = Path(__file__).resolve().parents[2] / "internal/manager/biz/agenttrace/testdata/sdk" / f"{SDK}.json"
target.write_text(json.dumps(body, ensure_ascii=False, indent=2) + "\n")
print(f"{SDK}: captured {len(capture.spans)} spans -> {target.name}")
provider.shutdown()
