#!/usr/bin/env python3
"""Validate the documented properties with the cached real Java agent/OTLP API.
Run with /tmp/ongrid-apm-languages/python/bin/python after run-languages.sh.
"""
import os
from pathlib import Path
import re
import subprocess
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from tempfile import TemporaryDirectory
from threading import Thread
from opentelemetry.proto.collector.trace.v1.trace_service_pb2 import ExportTraceServiceRequest

root = Path(__file__).resolve().parents[2]
cache = Path(os.environ.get('APM_LANGUAGE_CACHE', '/tmp/ongrid-apm-languages'))
doc = (root / 'docs/guides/apm-configuration-files.md').read_text()
properties = re.search(r'```properties\n(.*?)\n```', doc, re.S).group(1)
resources = []

class Receiver(BaseHTTPRequestHandler):
    def do_POST(self):
        assert self.path == '/v1/traces', self.path
        request = ExportTraceServiceRequest.FromString(self.rfile.read(int(self.headers['Content-Length'])))
        resources.extend({a.key: a.value.string_value for a in item.resource.attributes} for item in request.resource_spans)
        self.send_response(200)
        self.send_header('Content-Type', 'application/x-protobuf')
        self.end_headers()
    def log_message(self, *_):
        pass

with TemporaryDirectory(prefix='ongrid-java-properties-') as directory, ThreadingHTTPServer(('127.0.0.1', 0), Receiver) as receiver:
    worker = Thread(target=receiver.serve_forever, daemon=True)
    worker.start()
    try:
        tmp = Path(directory)
        (tmp / 'ConfigProbe.java').write_text('''import io.opentelemetry.api.GlobalOpenTelemetry;
public class ConfigProbe { public static void main(String[] args) throws Exception {
GlobalOpenTelemetry.getTracer("config-check").spanBuilder("probe").startSpan().end(); Thread.sleep(1500);
}}''')
        classpath = (cache / 'java/classpath.txt').read_text().strip()
        subprocess.run(['javac', '-cp', classpath, str(tmp / 'ConfigProbe.java')], check=True)
        config = tmp / 'otel.properties'
        config.write_text(properties.replace('http://127.0.0.1:4318', f'http://127.0.0.1:{receiver.server_port}'))
        clean = {k: v for k, v in os.environ.items() if not k.startswith('OTEL_') and k not in ('JAVA_TOOL_OPTIONS', 'JDK_JAVA_OPTIONS', '_JAVA_OPTIONS')}
        for env_name, jvm_name, expected in [(None, None, 'order-api'), ('env-service', None, 'env-service'), ('env-service', 'jvm-service', 'jvm-service')]:
            resources.clear()
            env = {**clean, **({'OTEL_SERVICE_NAME': env_name} if env_name else {})}
            command = ['java', '-javaagent:' + str(cache / 'opentelemetry-javaagent.jar'), '-Dotel.javaagent.configuration-file=' + str(config), '-Dotel.metrics.exporter=none', '-Dotel.bsp.schedule.delay=100']
            if jvm_name:
                command.append('-Dotel.service.name=' + jvm_name)
            result = subprocess.run(command + ['-cp', str(tmp) + os.pathsep + classpath, 'ConfigProbe'], env=env, capture_output=True, text=True, timeout=30)
            assert result.returncode == 0, result.stderr
            assert resources, result.stderr
            assert all(r.get('service.name') == expected and r.get('service.namespace') == 'trade' and r.get('deployment.environment.name') == 'production' and r.get('service.version') == '1.2.3' and r.get('service.instance.id') == 'order-api-01' for r in resources), resources
            print(f'PASS: actual OTLP resource service.name={expected}; file identity preserved')
    finally:
        receiver.shutdown()
        worker.join()
