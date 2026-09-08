"""Real official-SDK HTTP acceptance; run via run-more-languages.sh."""
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
OUTPUT = Path(os.environ.get('APM_ACCEPTANCE_OUTPUT', ROOT / 'output/apm-acceptance'))
OUTPUT.mkdir(parents=True, exist_ok=True)
LOGS = Path(os.environ['APM_MORE_LOGS'])
NETWORK = 'container:' + os.environ['APM_MORE_COLLECTOR']
LANGUAGES = os.environ.get('APM_MORE_LANGUAGES', 'dotnet php cpp rust ruby').split()
PROM = 'http://127.0.0.1:19090/prometheus'
TEMPO = 'http://127.0.0.1:13200'


def get(url):
    with urllib.request.urlopen(url, timeout=15) as response:
        return json.load(response)


def query(expression):
    return get(PROM + '/api/v1/query?' + urllib.parse.urlencode({'query': expression}))['data']['result']


containers = []
results = []
try:
    for n, language in enumerate(LANGUAGES):
        for sampled in (True, False):
            instance = 'sampled' if sampled else 'unsampled'
            name = f'apm-{language}-{instance}-{os.getpid()}'
            port = 18300 + n * 2 + int(sampled)
            env = {
                'APP_LANGUAGE': f'{language}-{instance}', 'HTTP_PORT': str(port),
                'SERVICE_NAMESPACE': 'trade', 'DEPLOYMENT_ENVIRONMENT': 'acceptance',
                'OTEL_SERVICE_NAME': 'apm-' + language,
                'OTEL_RESOURCE_ATTRIBUTES': f'service.namespace=trade,deployment.environment.name=acceptance,service.instance.id={instance}',
                'OTEL_EXPORTER_OTLP_ENDPOINT': 'http://127.0.0.1:4318',
                'OTEL_EXPORTER_OTLP_PROTOCOL': 'http/protobuf',
                'OTEL_TRACES_EXPORTER': 'otlp', 'OTEL_METRICS_EXPORTER': 'none' if language == 'ruby' else 'otlp',
                'OTEL_LOGS_EXPORTER': 'none', 'OTEL_TRACES_SAMPLER': 'always_on' if sampled else 'always_off',
                'OTEL_METRIC_EXPORT_INTERVAL': '1000', 'OTEL_BSP_SCHEDULE_DELAY': '1000',
                'OTEL_SEMCONV_STABILITY_OPT_IN': 'http,rpc',
                'OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE': 'cumulative',
                'OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION': 'explicit_bucket_histogram',
            }
            if language == 'cpp':
                env['OTEL_METRIC_EXPORT_TIMEOUT'] = '500'
            command = ['docker', 'run', '-d', '--name', name, '--network', NETWORK, '-v', f'{LOGS}:/logs']
            for key, value in env.items():
                command += ['-e', key + '=' + value]
            command += [f'ongrid-apm-demo-{language}:local']
            subprocess.run(command, check=True, stdout=subprocess.DEVNULL)
            containers.append(name)
            results.append({'language': language, 'instance': instance, 'port': port})
    # Same network namespace as applications; no public application ports.
    driver = '''
import http.client, time, json
from concurrent.futures import ThreadPoolExecutor
jobs = JOBS

def run(job):
    deadline = time.monotonic() + 60
    while True:
        try:
            c = http.client.HTTPConnection('127.0.0.1', job['port'], timeout=5)
            c.request('GET', '/healthz'); r = c.getresponse(); r.read(); c.close()
            assert r.status == 200
            break
        except (OSError, AssertionError):
            if time.monotonic() > deadline: raise
            time.sleep(0.5)
    for _ in range(10):
        for suffix, status in [('',200),('?fail=1',500),('?slow=1',200),('',200)]:
            c = http.client.HTTPConnection('127.0.0.1', job['port'], timeout=5)
            before = time.monotonic()
            c.request('GET', '/orders/42' + suffix); r = c.getresponse(); r.read(); c.close()
            assert r.status == status, (job, suffix, r.status)
            if 'slow' in suffix: assert time.monotonic() - before >= 0.075
        time.sleep(3)
with ThreadPoolExecutor(max_workers=len(jobs)) as pool: list(pool.map(run,jobs))
'''.replace('JOBS', repr(results))
    subprocess.run(['docker', 'run', '--rm', '--network', NETWORK, '--entrypoint', 'python',
                    'ongrid-apm-demo-python:local', '-c', driver], check=True)
    deadline = time.monotonic() + 90
    while True:
        try:
            for result in results:
                language, instance = result['language'], result['instance']
                selector = f'{{service_name="apm-{language}",service_instance_id="{instance}",http_route!="",http_route!="/healthz",http_route!="/readyz"}}'
                counts = query('http_server_request_duration_seconds_count' + selector)
                if language == 'ruby':
                    assert not counts, 'Ruby must not fabricate request metrics'
                else:
                    total = sum(float(row['value'][1]) for row in counts)
                    errors = sum(float(row['value'][1]) for row in counts if row['metric'].get('http_response_status_code') == '500')
                    assert total == 40 and errors == 10, (language, instance, total, errors, counts)
                    assert query('http_server_request_duration_seconds_bucket' + selector), language
                    result.update(requests=total, errors=errors, metric_languages=sorted({row['metric'].get('telemetry_sdk_language') for row in counts}))
                traceql = f'{{resource.service.name="apm-{language}" && resource.service.instance.id="{instance}"}}'
                traces = get(TEMPO + '/api/search?' + urllib.parse.urlencode({'q': traceql, 'limit': 50}))['traces']
                if instance == 'sampled':
                    assert len(traces) >= 40, (language, 'waiting for all sampled requests', len(traces))
                    if language == 'ruby':
                        assert query('traces_spanmetrics_calls_total{service="apm-ruby",span_kind="SPAN_KIND_SERVER",telemetry_sdk_language="ruby"}'), 'waiting for Tempo service discovery metrics'
                        assert query('sum(rate(traces_spanmetrics_calls_total{service="apm-ruby",span_kind="SPAN_KIND_SERVER",http_method!=""}[1m]) or rate(traces_spanmetrics_calls_total{service="apm-ruby",span_kind="SPAN_KIND_SERVER",http_request_method!=""}[1m])) > 0'), 'waiting for sampled HTTP rates'
                    logs = []
                    for line in (LOGS / f'{language}-{instance}.log').read_text().splitlines():
                        try: logs.append(json.loads(line))
                        except json.JSONDecodeError: pass
                    ids = {line.get('trace_id') for line in logs}
                    assert any(trace['traceID'] in ids for trace in traces), (language, 'no trace/log correlation')
                else:
                    assert not traces, (language, 'sampling disabled but traces exported')
                result['traces'] = len(traces)
            break
        except (AssertionError, KeyError) as error:
            if time.monotonic() > deadline: raise
            time.sleep(3)
    (OUTPUT / 'more-languages.json').write_text(json.dumps(results, indent=2) + '\n')
    print(json.dumps(results, indent=2), flush=True)
finally:
    for name in containers:
        subprocess.run(['docker', 'rm', '-f', name], check=False, stdout=subprocess.DEVNULL)
