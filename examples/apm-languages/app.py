"""Official auto-instrumentation plus the gRPC OpenTelemetry metrics plugin."""
import json
import os
import time
from concurrent.futures import ThreadPoolExecutor

import grpc
import grpc_observability
from flask import Flask, request
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.metrics.view import ExplicitBucketHistogramAggregation, View
from opentelemetry.sdk.resources import Resource
import health_pb2
import health_pb2_grpc


def log_request(protocol):
    ctx = trace.get_current_span().get_span_context()
    print(json.dumps({"message": "request completed", "protocol": protocol,
                      "service.name": os.environ["OTEL_SERVICE_NAME"],
                      "service.namespace": os.environ.get("SERVICE_NAMESPACE", "trade"), "deployment.environment.name": os.environ.get("DEPLOYMENT_ENVIRONMENT", "acceptance"),
                      "service.instance.id": os.environ.get("SERVICE_INSTANCE_ID", ""), "service.version": os.environ.get("SERVICE_VERSION", ""),
                      "trace_id": f"{ctx.trace_id:032x}", "span_id": f"{ctx.span_id:016x}"}), flush=True)


class Health(health_pb2_grpc.HealthServicer):
    def Check(self, req, context):
        if req.service == "slow":
            time.sleep(0.08)
        log_request("rpc")
        if req.service == "missing":
            context.abort(grpc.StatusCode.NOT_FOUND, "missing")
        return health_pb2.HealthCheckResponse(status=health_pb2.HealthCheckResponse.SERVING)


app = Flask(__name__)


@app.get("/orders/<id>")
def orders(id):
    if request.args.get("slow"):
        time.sleep(0.08)
    log_request("http")
    return {"id": id}, 500 if request.args.get("fail") else 200


if __name__ == "__main__":
    # The plugin's seconds histogram needs explicit latency buckets. The global
    # auto-configured provider cannot accept Views after initialization, so this
    # provider owns only gRPC plugin metrics; HTTP stays with auto-instrumentation.
    grpc_provider = MeterProvider(
        resource=Resource.create({}),
        metric_readers=[PeriodicExportingMetricReader(OTLPMetricExporter())],
        views=[View(instrument_name="grpc.server.call.duration", aggregation=ExplicitBucketHistogramAggregation(
            boundaries=(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10)))],
    )
    grpc_metrics = grpc_observability.OpenTelemetryPlugin(meter_provider=grpc_provider)
    grpc_metrics.register_global()
    rpc = grpc.server(ThreadPoolExecutor(max_workers=4))
    health_pb2_grpc.add_HealthServicer_to_server(Health(), rpc)
    rpc.add_insecure_port("127.0.0.1:" + os.environ.get("RPC_PORT", "18081"))
    rpc.start()
    try:
        app.run(host="127.0.0.1", port=int(os.environ.get("HTTP_PORT", "18080")), use_reloader=False)
    finally:
        rpc.stop(0).wait()
        grpc_metrics.deregister_global()
        grpc_provider.shutdown()
