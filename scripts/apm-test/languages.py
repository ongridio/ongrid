"""Exercise installed official agents against the isolated Collector/Tempo/Prometheus."""
from http.client import HTTPConnection
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor

import grpc
import health_pb2
import health_pb2_grpc

ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "examples/apm-languages"
CACHE = Path(os.environ["APM_LANGUAGE_CACHE"])
OUTPUT = Path(os.environ["APM_ACCEPTANCE_OUTPUT"])
PROM = "http://127.0.0.1:19090/prometheus"
TEMPO = "http://127.0.0.1:13200"
HTTP_PORT = int(os.environ.get("HTTP_PORT", "18080"))
RPC_PORT = int(os.environ.get("RPC_PORT", "18081"))


def get_json(url):
    with urllib.request.urlopen(url, timeout=15) as response:
        return json.load(response)


def metrics(service, instance):
    query = '{service_name="%s",service_instance_id="%s",__name__=~"(http|rpc)_server_.*_count"}' % (service, instance)
    return get_json(PROM + "/api/v1/query?" + urllib.parse.urlencode({"query": query}))["data"]["result"]


def wait_ready(process):
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"application exited {process.returncode}")
        try:
            for port in (HTTP_PORT, RPC_PORT):
                with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                    pass
            return
        except OSError:
            time.sleep(0.2)
    raise TimeoutError("application did not listen")


def exercise(language, sampled, *, measure=False, instrumented=True):
    started = int(time.time()) - 5
    service = "apm-" + language + ("-overhead" if measure else "")
    instance = "sampled" if sampled else "unsampled"
    env = {**os.environ, "OTEL_JAVAAGENT_EXTENSIONS": str(CACHE / "java/target/apm-java-1.0.jar"), "OTEL_SERVICE_NAME": service,
           "OTEL_RESOURCE_ATTRIBUTES": f"service.namespace=trade,deployment.environment.name=acceptance,service.instance.id={instance}",
           "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:14319",
           "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf", "OTEL_METRICS_EXPORTER": "otlp",
           "OTEL_TRACES_EXPORTER": "otlp", "OTEL_LOGS_EXPORTER": "none",
           "OTEL_TRACES_SAMPLER": "always_on" if sampled else "always_off",
           "OTEL_METRIC_EXPORT_INTERVAL": "1000", "OTEL_BSP_SCHEDULE_DELAY": "1000",
           "OTEL_SEMCONV_STABILITY_OPT_IN": "http,rpc",
           "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "cumulative",
           "OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION": "explicit_bucket_histogram",
           "NODE_PATH": str(CACHE / "node/node_modules")}
    commands = {
        "python": [str(CACHE / "python/bin/opentelemetry-instrument"), str(CACHE / "python/bin/python"), str(SOURCE / "app.py")],
        "node": ["node", "--require", "@opentelemetry/auto-instrumentations-node/register", str(SOURCE / "app.cjs")],
        "java": ["java", "-javaagent:" + str(CACHE / "opentelemetry-javaagent.jar"), "-cp",
                 str(CACHE / "java/target/classes") + os.pathsep + (CACHE / "java/classpath.txt").read_text().strip(), "App"],
    }
    command = commands[language]
    if not instrumented:
        env["OTEL_SDK_DISABLED"] = "true"
        command = {"python": command[1:], "node": ["node", str(SOURCE / "app.cjs")],
                   "java": [command[0], *command[2:]]}[language]
    logfile = OUTPUT / f"{language}-{instance}{'-overhead' if measure else ''}.log"
    with logfile.open("w") as log:
        process = subprocess.Popen(command, env=env, stdout=log, stderr=log)
        try:
            wait_ready(process)
            if measure:
                def call(connection):
                    before = time.perf_counter()
                    connection.request("GET", "/orders/42")
                    response = connection.getresponse()
                    assert response.status == 200
                    response.read()
                    return (time.perf_counter() - before) * 1000
                warmup = HTTPConnection("127.0.0.1", HTTP_PORT, timeout=5)
                try:
                    for _ in range(200):
                        call(warmup)
                finally:
                    warmup.close()
                with ThreadPoolExecutor(max_workers=4) as pool:
                    before = time.perf_counter()
                    deadline = before + 10
                    def worker(_):
                        timings = []
                        connection = HTTPConnection("127.0.0.1", HTTP_PORT, timeout=5)
                        try:
                            while time.perf_counter() < deadline:
                                started_request = time.perf_counter()
                                timings.append(call(connection))
                                # Same bounded load in all modes; not a saturation benchmark.
                                time.sleep(max(0, 0.02 - (time.perf_counter() - started_request)))
                        finally:
                            connection.close()
                        return timings
                    timings = sorted(value for worker_timings in pool.map(worker, range(4)) for value in worker_timings)
                    elapsed = time.perf_counter() - before
                rss = int(subprocess.check_output(["ps", "-o", "rss=", "-p", str(process.pid)], text=True).strip())
                result = {"language": language, "instrumented": instrumented, "sampled": sampled,
                          "requests": len(timings), "concurrency": 4, "target_rps": 200, "duration_seconds": elapsed, "rps": len(timings) / elapsed,
                          "p50_ms": timings[len(timings) // 2], "p95_ms": timings[int(len(timings) * 0.95)], "rss_kib": rss}
                print(json.dumps(result), flush=True)
                return result
            with grpc.insecure_channel(f"127.0.0.1:{RPC_PORT}") as channel:
                client = health_pb2_grpc.HealthStub(channel)
                for batch in range(3):
                    for i in range(4):
                        suffix = "?fail=1" if i == 0 else "?slow=1" if i == 1 else ""
                        try:
                            with urllib.request.urlopen(f"http://127.0.0.1:{HTTP_PORT}/orders/42" + suffix, timeout=5) as response:
                                assert response.status == 200 and i != 0
                        except urllib.error.HTTPError as error:
                            assert i == 0 and error.code == 500
                        try:
                            client.Check(health_pb2.HealthCheckRequest(service="missing" if i == 0 else "slow" if i == 1 else ""), timeout=5)
                            assert i != 0
                        except grpc.RpcError as error:
                            assert i == 0 and error.code() == grpc.StatusCode.NOT_FOUND
                    time.sleep(7)  # The production Collector batches exports for five seconds.
            points = metrics(service, instance)
            counts = {}
            errors = {}
            routes = set()
            for point in points:
                labels, value = point["metric"], float(point["value"][1])
                name = labels["__name__"]
                counts[name] = counts.get(name, 0) + value
                status = labels.get("http_response_status_code", labels.get("http_status_code", "0"))
                failed = labels.get("error_type") or int(status) >= 500 or labels.get("rpc_grpc_status_code", "0") != "0" or labels.get("rpc_response_status_code", "OK") != "OK"
                errors[name] = errors.get(name, 0) + (value if failed else 0)
                if name.startswith("http_"):
                    routes.add(labels.get("http_route", ""))
            http = "http_server_request_duration_seconds_count"
            assert counts.get(http) == 12, (language, "HTTP request count", counts)
            assert errors[http] == 3, (language, "HTTP errors", errors)
            assert len(routes) == 1 and "42" not in next(iter(routes)) and "" not in routes, (language, "route cardinality", routes)
            rpc = "rpc_server_call_duration_seconds_count"
            assert counts.get(rpc) == 12 and errors[rpc] == 3, (language, "RPC metrics", counts, errors)
            query = '{ resource.service.name = "%s" && resource.service.instance.id = "%s" && kind = server }' % (service, instance)
            deadline = time.monotonic() + 60
            while True:
                traces = get_json(TEMPO + "/api/search?" + urllib.parse.urlencode({"q": query, "limit": 100, "start": started, "end": int(time.time()) + 1})).get("traces", [])
                if not sampled or len(traces) == 24 or time.monotonic() >= deadline:
                    break
                time.sleep(1)
            assert len(traces) == (24 if sampled else 0), (language, instance, "trace count", len(traces))
            records = []
            for line in logfile.read_text().splitlines():
                if line.startswith('{"message"'):
                    records.append(json.loads(line))
            assert len(records) == 24, (language, "application logs", len(records))
            if sampled:
                trace_ids = {entry["traceID"].lower().zfill(32) for entry in traces}
                assert all(record["trace_id"] in trace_ids for record in records), (language, "log/trace correlation", {record["trace_id"] for record in records} - trace_ids)
                (OUTPUT / f"{language}-requests.jsonl").write_text("".join(json.dumps(record) + "\n" for record in records))
            result = {"language": language, "instance": instance, "requests_per_protocol": 12, "metrics": counts,
                      "errors": errors, "routes": sorted(routes), "server_traces": len(traces), "logs": len(records),
                      "rpc_metrics_supported": True}
            print(json.dumps(result), flush=True)
            return result
        finally:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


if __name__ == "__main__":
    OUTPUT.mkdir(parents=True, exist_ok=True)
    results = []
    if os.environ.get("APM_OVERHEAD_ONLY") != "1":
        for language in sys.argv[1:] or ["java", "node", "python"]:
            for sampled in [True, False]:
                results.append(exercise(language, sampled))
        (OUTPUT / "languages.json").write_text(json.dumps(results, indent=2) + "\n")
    if os.environ.get("APM_TEST_OVERHEAD") == "1":
        measurements = []
        for language in sys.argv[1:] or ["java", "node", "python"]:
            for trial in range(3):
                for instrumented, sampled in [(False, False), (True, False), (True, True)]:
                    record = exercise(language, sampled, measure=True, instrumented=instrumented)
                    measurements.append({"trial": trial + 1, **record})
        (OUTPUT / "overhead.json").write_text(json.dumps(measurements, indent=2) + "\n")
