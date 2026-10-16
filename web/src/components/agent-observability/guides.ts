export const sdkPackages: Record<string, string> = {
  langsmith:
    'langsmith==0.14.8 opentelemetry-sdk==1.45.1 opentelemetry-exporter-otlp-proto-http==1.45.1',
  openinference:
    'openinference-instrumentation-langchain==0.1.79 langchain-core==1.6.10 opentelemetry-sdk==1.45.1 opentelemetry-exporter-otlp-proto-http==1.45.1',
  openllmetry:
    'traceloop-sdk==0.62.4 opentelemetry-exporter-otlp-proto-http==1.45.1',
  langfuse:
    'langfuse==4.17.0 opentelemetry-sdk==1.45.1 opentelemetry-exporter-otlp-proto-http==1.45.1',
};

export function environmentScript(environment: Record<string, string>) {
  return Object.entries(environment)
    .map(([key, value]) => `export ${key}='${value.replace(/'/g, `'\\''`)}'`)
    .join('\n');
}

export function pythonExample(sdk: string) {
  const exporter = `import os
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

exporter = OTLPSpanExporter(
    endpoint=os.environ["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"]
)
`;
  const provider = `from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

provider = TracerProvider(resource=Resource.create())
`;
  switch (sdk) {
    case 'langsmith':
      return `${exporter}${provider}provider.add_span_processor(BatchSpanProcessor(exporter))

from langsmith import Client, traceable, tracing_context
client = Client(tracing_mode="otel", otel_tracer_provider=provider)

@traceable(client=client, run_type="chain")
def support_agent(question):
    return {"answer": "Hello from Ongrid"}

with tracing_context(enabled=True, client=client,
                     metadata={"thread_id": "ongrid-smoke"}):
    support_agent("Hello Ongrid")

client.flush()
provider.force_flush()
`;
    case 'openinference':
      return `${exporter}${provider}provider.add_span_processor(BatchSpanProcessor(exporter))

from openinference.instrumentation.langchain import LangChainInstrumentor
from openinference.instrumentation import using_attributes
LangChainInstrumentor().instrument(tracer_provider=provider)

from langchain_core.runnables import RunnableLambda
agent = RunnableLambda(lambda value: {"answer": "Hello from Ongrid"})
with using_attributes(session_id="ongrid-smoke"):
    agent.invoke({"question": "Hello Ongrid"})

provider.force_flush()
`;
    case 'openllmetry':
      return `${exporter}
from traceloop.sdk import Traceloop
from traceloop.sdk.decorators import workflow
from opentelemetry import trace

Traceloop.init(app_name=os.environ["OTEL_SERVICE_NAME"],
              exporter=exporter, telemetry_enabled=False)
Traceloop.set_association_properties({"session_id": "ongrid-smoke"})

@workflow(name="support-agent")
def support_agent(question):
    return {"answer": "Hello from Ongrid"}

support_agent("Hello Ongrid")
trace.get_tracer_provider().force_flush()
`;
    case 'langfuse':
      return `${exporter}${provider}
from langfuse import Langfuse, propagate_attributes

# Local placeholders are only for this trace-export smoke test.
# Keep your existing Langfuse credentials for other Langfuse APIs.
client = Langfuse(
    public_key="pk-lf-ongrid-smoke", secret_key="sk-lf-local-unused",
    base_url="http://127.0.0.1:1",
    tracer_provider=provider, span_exporter=exporter,
    should_export_span=lambda span: True,
)
with propagate_attributes(session_id="ongrid-smoke"):
    with client.start_as_current_observation(
        name="support-agent", as_type="agent", input="Hello Ongrid"
    ) as run:
        run.update(output="Hello from Ongrid")

client.flush()
client.shutdown()
`;
    default:
      return '';
  }
}
