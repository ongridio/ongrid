"""Run with opentelemetry-instrument; no custom tracer or request metrics."""
import json
import os
import time
from concurrent.futures import ThreadPoolExecutor

import grpc
from flask import Flask, request
from opentelemetry import trace
import health_pb2
import health_pb2_grpc


def log_request(protocol):
    ctx = trace.get_current_span().get_span_context()
    print(json.dumps({"message": "request completed", "protocol": protocol,
                      "service.name": os.environ["OTEL_SERVICE_NAME"],
                      "service.namespace": os.environ.get("SERVICE_NAMESPACE", "trade"), "deployment.environment.name": os.environ.get("DEPLOYMENT_ENVIRONMENT", "acceptance"),
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
    rpc = grpc.server(ThreadPoolExecutor(max_workers=4))
    health_pb2_grpc.add_HealthServicer_to_server(Health(), rpc)
    rpc.add_insecure_port("127.0.0.1:" + os.environ.get("RPC_PORT", "18081"))
    rpc.start()
    try:
        app.run(host="127.0.0.1", port=int(os.environ.get("HTTP_PORT", "18080")), use_reloader=False)
    finally:
        rpc.stop(0).wait()
