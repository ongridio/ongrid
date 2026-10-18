#!/usr/bin/env python3
"""Staging-only Host/Edge acceptance. Run beside the deployment with its tunnel probe.
Credentials are read from the existing Manager container, never printed or persisted.
Only uniquely named test resources are mutated, and cleanup runs even on failure.
"""
import argparse
import json
import os
import pathlib
import select
import ssl
import subprocess
import time
import urllib.error
import urllib.request
import uuid

parser = argparse.ArgumentParser()
parser.add_argument('--url', required=True)
parser.add_argument('--probe', required=True)
parser.add_argument('--tunnel', default='127.0.0.1:40012')
parser.add_argument('--ca', default='')
parser.add_argument('--server-name', default='')
parser.add_argument('--report', required=True)
parser.add_argument('--restart-manager', action='store_true')
args = parser.parse_args()
ctx = ssl._create_unverified_context()
suffix = 'e2e-host-identity-' + uuid.uuid4().hex[:12]
results, cleanup_errors, edges, hosts, probes = [], [], [], set(), []
flow_id = None
token = ''


def api(method, route, body=None, expected=(200, 201, 202, 204)):
    payload = None if body is None else json.dumps(body).encode()
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(args.url.rstrip('/') + '/api/v1' + route,
                                 data=payload, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(req, context=ctx, timeout=70)
    except urllib.error.HTTPError as exc:
        response = exc
    data = response.read()
    obj = json.loads(data) if data else {}
    if response.code not in expected:
        raise AssertionError(f'{method} {route}: HTTP {response.code}, {str(obj)[:500]}')
    if isinstance(obj, dict) and 'code' in obj and 'data' in obj:
        obj = obj['data']
    return obj


def record(name, fn):
    started = time.monotonic()
    try:
        evidence = fn()
        results.append({'name': name, 'status': 'passed', 'evidence': evidence,
                        'seconds': round(time.monotonic() - started, 2)})
        print('PASS ' + name, flush=True)
        return evidence
    except Exception as exc:
        results.append({'name': name, 'status': 'failed', 'error': str(exc)[:1000]})
        print('FAIL ' + name + ': ' + str(exc)[:500], flush=True)
        raise


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def until(fn, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = fn()
        if value:
            return value
        time.sleep(.3)
    raise AssertionError('condition did not converge')


class Probe:
    def __init__(self, edge, marker):
        self.edge = edge
        self.process = subprocess.Popen([args.probe], stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                        text=True, bufsize=1)
        probes.append(self)
        config = {'address': args.tunnel, 'access_key': edge['access_key_id'],
                  'secret_key': edge['secret_key'], 'marker': marker,
                  'ca': args.ca, 'server_name': args.server_name}
        self.send(config)
        check(self.read().get('ready'), 'probe not ready')

    def send(self, value):
        self.process.stdin.write(json.dumps(value) + '\n')
        self.process.stdin.flush()

    def read(self):
        ready, _, _ = select.select([self.process.stdout], [], [], 30)
        check(bool(ready), 'probe response timeout')
        line = self.process.stdout.readline()
        check(bool(line), 'probe exited')
        return json.loads(line)

    def call(self, method, body=None, allow_error=False):
        self.send({'method': method, 'body': body or {}})
        value = self.read()
        if not allow_error:
            check(not value.get('error'), 'tunnel ' + method + ': ' + str(value.get('error')))
        return value

    def register(self, hostname):
        return self.call('register_edge', {
            'access_key': self.edge['access_key_id'], 'secret_key': self.edge['secret_key'],
            'agent_version': 'e2e-host-identity-probe',
            'host_info': {'hostname': hostname, 'hardware_fingerprint': suffix,
                          'fingerprint': suffix, 'os': 'linux', 'arch': 'amd64',
                          'cpu_count': 2, 'mem_total_bytes': 1073741824}})

    def close(self):
        if self.process.poll() is not None:
            return
        self.process.stdin.close()
        try:
            self.process.wait(timeout=8)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            self.process.wait(timeout=5)


def new_edge(label):
    edge = api('POST', '/edges', {'name': suffix + '-' + label})
    edges.append(edge['id'])
    return edge


def device(host_id):
    return api('GET', '/devices/' + str(host_id))


def tool(name, params):
    result = api('POST', f'/flows/{flow_id}/test-node', {
        'node_type': 'tool', 'config': {'tool': name, 'args': params}})
    check(not result.get('error'), str(result.get('error')))
    output = result['output']
    if isinstance(output, dict) and 'result' in output:
        output = output['result']
    return json.loads(output) if isinstance(output, str) else output


def summary(host_id, name='get_host_summary'):
    value = tool(name, {'device_ids': [host_id]})
    check(value.get('success_count') == 1, 'summary failed: ' + str(value)[:500])
    return value['results'][0]['summary']


def skill(body):
    return api('POST', '/skills/host_probe_ping/execute', dict(body, params={'host': '127.0.0.1'}))


try:
    container = json.loads(subprocess.check_output(['docker', 'inspect', 'ongrid']))[0]
    env = dict(item.split('=', 1) for item in container['Config']['Env'])
    login = api('POST', '/auth/login', {'email': env['ONGRID_ADMIN_EMAIL'],
                                      'password': env['ONGRID_ADMIN_PASSWORD']})
    token = login.get('access_token') or login.get('token')
    check(bool(token), 'login did not return a token')
    graph = {'nodes': [{'id': 'start', 'type': 'trigger.manual', 'name': 'start', 'config': {}}],
             'edges': []}
    flow_id = api('POST', '/flows', {'name': suffix, 'graph': graph})['id']
    # An unregistered spare Edge must never manufacture a Host during migration.
    spare = new_edge('unregistered')
    first = new_edge('installation-one')
    one = Probe(first, 'installation-one')
    one.register(suffix)
    linked = api('GET', '/edges/' + str(first['id']))
    host_id = linked['device_id']
    hosts.add(host_id)
    record('Host and Edge IDs are independently resolved', lambda: (
        check(host_id != first['id'], 'fixture must use different Host and Edge IDs'),
        check(device(host_id)['current_edge_id'] == first['id'], 'wrong initial binding'),
        {'host_id': host_id, 'edge_id': first['id'], 'different_ids': host_id != first['id']})[-1])
    record('Plugin configuration carries explicit Host identity', lambda: (
        check(one.call('get_plugin_configs')['result']['device_id'] == host_id, 'plugin identity mismatch'),
        {'host_id': host_id})[-1])
    record('Host summary dispatches current tunnel', lambda: (
        check(summary(host_id)['host_load']['cpu_pct'] == 23, 'load did not use probe'),
        {'host_id': host_id})[-1])
    record('Skill execution accepts Host identity', lambda: (
        check(skill({'device_id': host_id})['result']['installation'] == 'installation-one', 'wrong skill route'),
        {'installation': 'installation-one'})[-1])
    second = new_edge('installation-two')
    two = Probe(second, 'installation-two')
    two.register(suffix)
    record('Same fingerprint reinstall preserves Host and advances binding', lambda: (
        check(api('GET', '/edges/' + str(second['id']))['device_id'] == host_id, 'Host changed'),
        check(device(host_id)['current_edge_id'] == second['id'], 'new instance not current'),
        {'host_id': host_id, 'old_edge': first['id'], 'current_edge': second['id']})[-1])
    record('Skill follows new installation', lambda: (
        check(skill({'device_id': host_id})['result']['installation'] == 'installation-two', 'stale route'),
        {'installation': 'installation-two'})[-1])
    record('Old explicit Edge skill target is rejected', lambda: api(
        'POST', '/skills/host_probe_ping/execute', {'edge_id': first['id'], 'params': {'host': '127.0.0.1'}},
        expected=(400, 404, 409)))
    record('Old registration cannot steal Host binding', lambda: (
        check(bool(one.call('register_edge', {'access_key': first['access_key_id'],
            'secret_key': first['secret_key'], 'host_info': {'hostname': suffix,
            'hardware_fingerprint': suffix, 'fingerprint': suffix}}, allow_error=True).get('error')),
              'retired registration unexpectedly accepted'),
        check(device(host_id)['current_edge_id'] == second['id'], 'old registration stole binding'),
        {'current_edge': second['id']})[-1])
    two.close()
    until(lambda: not device(host_id)['online'], timeout=35)
    one.call('heartbeat', {'edge_id': first['id'], 'ts': int(time.time())})
    record('Old heartbeat cannot revive Host after current instance disconnects', lambda: (
        check(not device(host_id)['online'], 'retired heartbeat revived Host'), {'online': False})[-1])
    two = Probe(second, 'installation-two')
    two.register(suffix)
    one.close()
    record('Old disconnect preserves current Host presence', lambda: (
        time.sleep(2), check(device(host_id)['online'], 'old disconnect marked Host offline'),
        {'online': True})[-1])
    record('Saved legacy tool name resolves to Host summary', lambda: (
        check(summary(host_id, 'get_edge_summary')['current_edge']['edge_id'] == second['id'], 'legacy tool wrong route'),
        {'alias': 'get_edge_summary'})[-1])
    for name in ['rank_hosts', 'rank_edges', 'find_outlier_hosts', 'find_outlier_edges']:
        record('Workflow tool ' + name, lambda name=name: tool(name, {'by': 'mem', 'metric': 'mem'}))
    record('Topology distinguishes Hosts and Edge installations', lambda: (
        check('host_count' in tool('get_topology', {}), 'host count absent'),
        {'separate_counts': True})[-1])
    two.close()
    until(lambda: not device(host_id)['online'], timeout=35)
    record('Offline summary retains Host with unavailable execution', lambda: (
        check(not summary(host_id)['execution_available'], 'offline execution advertised'),
        {'host_id': host_id})[-1])
    api('DELETE', '/edges/' + str(second['id']))
    api('DELETE', '/edges/' + str(first['id']))
    record('Deleting all installations preserves Host and summary', lambda: (
        check(device(host_id)['id'] == host_id, 'Host deleted with Edge'),
        check(summary(host_id)['host']['device_id'] == host_id, 'Host history inaccessible'),
        {'host_id': host_id})[-1])
    if args.restart_manager:
        subprocess.run(['docker', 'restart', 'ongrid'], stdout=subprocess.DEVNULL, check=True)
        def healthy():
            try:
                with urllib.request.urlopen(args.url.rstrip('/') + '/readyz', context=ctx, timeout=3) as response:
                    return response.status == 200
            except Exception:
                return False
        until(healthy, timeout=60)
        record('Manager restart preserves Host without live installations', lambda: (
            check(device(host_id)['id'] == host_id, 'restart removed Host'),
            check(summary(host_id)['host']['device_id'] == host_id, 'offline history lost after restart'),
            {'host_id': host_id})[-1])
        record('Migration never creates Host from unregistered same-number Edge', lambda: (
            check(not api('GET', '/edges/' + str(spare['id'])).get('device_id'), 'migration invented Host link'),
            {'unregistered_edge': spare['id']})[-1])
finally:
    for probe in probes:
        try:
            probe.close()
        except Exception as exc:
            cleanup_errors.append('probe: ' + str(exc))
    for edge_id in edges:
        try:
            api('DELETE', '/edges/' + str(edge_id), expected=(200, 204, 404))
        except Exception as exc:
            cleanup_errors.append('edge: ' + str(exc))
    # Collect Host IDs even if an assertion failed immediately after registration.
    if token:
        try:
            for item in api('GET', '/devices?name=' + suffix).get('items', []):
                if item.get('name', '').startswith(suffix):
                    hosts.add(item['id'])
        except Exception as exc:
            cleanup_errors.append('host lookup: ' + str(exc))
    for host_id in hosts:
        try:
            api('DELETE', '/devices/' + str(host_id), expected=(200, 204, 404))
        except Exception as exc:
            cleanup_errors.append('host: ' + str(exc))
    if flow_id is not None:
        try:
            api('DELETE', '/flows/' + str(flow_id), expected=(200, 204, 404))
        except Exception as exc:
            cleanup_errors.append('flow: ' + str(exc))
    report = {'run': suffix, 'results': results, 'cleanup_errors': cleanup_errors,
              'scope': 'real Manager API and authenticated tunnel; synthetic Edge handlers, no shell execution'}
    pathlib.Path(args.report).write_text(json.dumps(report, ensure_ascii=False, indent=2))
    print(json.dumps({'passed': sum(r['status'] == 'passed' for r in results),
                      'failed': sum(r['status'] == 'failed' for r in results),
                      'cleanup_errors': cleanup_errors}), flush=True)
    if cleanup_errors:
        raise SystemExit(1)
