import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { serviceParams, type ApmIngestion, type ApmSummary } from '@/api/apm';
import { server } from '@/test/msw-server';
import { selectOption } from '@/test/select-option';
import { ServiceSetup } from './ServiceSetup';

const service: ApmSummary = {
  identity: { service_name: 'billing-api', service_namespace: 'payments', environment: 'staging' },
  languages: ['go'], rps: 1, error_rate: 0, requests: 60,
  p50_ms: 10, p95_ms: 20, p99_ms: 30, data_status: 'observed',
};
const params = serviceParams(new URLSearchParams('start=2026-10-10T00:00:00Z&end=2026-10-10T01:00:00Z&device_id=42'), service.identity, 'http');
const target: ApmIngestion['targets'][number] = {
  instance: { instance_id: 'billing-worker-3', device_id: '42', cluster_id: '7', k8s_cluster_id: '3', pod: 'billing-worker-3', namespace: 'business', version: '2026.10.10' },
  endpoint: 'http://ongrid-edge-telemetry-gateway.observability.svc:18418', location: 'kubernetes', metrics: true,
  version_field_path: "metadata.labels['app.kubernetes.io/version']",
  target_id: 'cfd72932-deb4-4550-86e0-59c036e37d4a',
};
let latest: URLSearchParams;
const respond = (targets: ApmIngestion['targets']) => server.use(http.get('/api/v1/apm/ingestion', ({ request }) => {
  latest = new URL(request.url).searchParams;
  return HttpResponse.json({ data: { identity: service.identity, targets } });
}));

describe('Service ingestion configuration', () => {
  beforeEach(() => { localStorage.setItem('ongrid-locale', 'zh-CN'); respond([target]); });

  it('automatically generates complete variables with the resolved receiver and exact resource scope', async () => {
    respond([{ ...target, location: 'host', endpoint: 'http://127.0.0.1:4318' }]);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    render(<ServiceSetup service={service} params={params} onClose={vi.fn()} />);
    const config = (await screen.findByLabelText('生成的接入配置')).textContent!;
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: '配置格式' })).not.toBeInTheDocument();
    expect(config).not.toMatch(/env:|fieldRef:|ONGRID_POD_UID/);
    expect(latest.get('service_name')).toBe('billing-api');
    expect(latest.get('service_namespace')).toBe('payments');
    expect(latest.get('environment')).toBe('staging');
    expect(latest.get('device_id')).toBe('42');
    expect(latest.get('start')).toBe('2026-10-10T00:00:00Z');
    expect(config).toContain("export OTEL_SERVICE_NAME='billing-api'");
    expect(config).toContain('service.namespace=payments,deployment.environment.name=staging,ongrid.target.id=' + target.target_id);
    expect(config).toContain('cat /proc/sys/kernel/random/uuid');
    expect(config).toContain(`service.instance.id='"$ONGRID_SERVICE_INSTANCE_ID"`);
    expect(config).not.toContain('billing-worker-3');
    expect(config).not.toContain('service.version=');
    expect(config).toContain("export OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:4318'");
    expect(config.split('\n').slice(1).map(line => line.split('=')[0])).toEqual(['export OTEL_SERVICE_NAME', 'export OTEL_RESOURCE_ATTRIBUTES', 'export OTEL_EXPORTER_OTLP_ENDPOINT', 'export OTEL_EXPORTER_OTLP_PROTOCOL']);
    expect(config).toContain("export OTEL_EXPORTER_OTLP_PROTOCOL='http/protobuf'");
    expect(config).not.toMatch(/order-api|production/);
    expect(screen.getByRole('button', { name: '复制配置' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: '复制配置' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(config));
    expect(await screen.findByRole('button', { name: '已复制' })).toBeInTheDocument();
  });

  it('injects each Pod version from metadata without pinning either observed version', async () => {
    respond([target, { ...target, instance: { ...target.instance, instance_id: 'worker-4', pod: 'worker-4', version: '2026.10.11' }, endpoint: 'http://ongrid-edge-telemetry-gateway.ops.svc:24318', metrics: false }]);
    render(<ServiceSetup service={{ ...service, identity: { ...service.identity, service_name: 'billing-$(REGION)' } }} params={params} onClose={vi.fn()} />);
    await screen.findByLabelText('生成的接入配置');
    expect(screen.queryByRole('combobox', { name: '配置格式' })).not.toBeInTheDocument();
    let config = screen.getByLabelText('生成的接入配置').textContent!;
    expect(config).toContain('fieldPath: metadata.uid');
    expect(config).toContain('service.instance.id=$(ONGRID_POD_UID)');
    expect(config).toContain('value: "billing-$$(REGION)"');
    expect(config).toContain("fieldPath: metadata.labels['app.kubernetes.io/version']");
    expect(config).toContain('service.version=$(ONGRID_SERVICE_VERSION)');
    expect(config).not.toMatch(/2026\.10\.10|2026\.10\.11/);
    expect(config).not.toMatch(/OTEL_(TRACES_EXPORTER|METRICS_EXPORTER|LOGS_EXPORTER|PROPAGATORS|SEMCONV_STABILITY_OPT_IN)/);
    expect(config).not.toMatch(/TEMPORALITY_PREFERENCE|DEFAULT_HISTOGRAM_AGGREGATION/);
    expect(config).toContain('name: OTEL_EXPORTER_OTLP_PROTOCOL\n    value: "http/protobuf"');
    await selectOption(screen.getByRole('combobox', { name: '接入实例' }), 'worker-4 · #3');
    config = screen.getByLabelText('生成的接入配置').textContent!;
    expect(config).toContain('http://ongrid-edge-telemetry-gateway.ops.svc:24318');
    expect(config).not.toContain('OTEL_METRICS_EXPORTER');
    expect(config).toContain('service.version=$(ONGRID_SERVICE_VERSION)');
  });

  it('keeps Docker metadata and generates a fresh opaque identity on each start', async () => {
    respond([{ ...target, instance: { ...target.instance, instance_id: 'device:123', container_name: 'app-blue' }, location: 'docker', endpoint: 'http://172.23.0.1:4318' }]);
    render(<ServiceSetup service={service} params={params} onClose={vi.fn()} />);
    const config = (await screen.findByLabelText('生成的接入配置')).textContent!;
    expect(config).toContain('container.name=app-blue');
    expect(config).not.toContain('device:123');
    expect(config).toContain('cat /proc/sys/kernel/random/uuid');
    expect(config).not.toContain('%3A');
    expect(config).toContain("export OTEL_EXPORTER_OTLP_ENDPOINT='http://172.23.0.1:4318'");
  });

  it('preserves application version configuration when the Pod has no version metadata', async () => {
    respond([{ ...target, version_field_path: undefined }]);
    render(<ServiceSetup service={service} params={params} onClose={vi.fn()} />);
    const config = (await screen.findByLabelText('生成的接入配置')).textContent!;
    expect(config).toContain('fieldPath: metadata.uid');
    expect(config).not.toMatch(/service.version=|ONGRID_SERVICE_VERSION|2026\.10\.10/);
    expect(screen.getByText(/当前 Pod 未提供可读取的/)).toBeInTheDocument();
  });

  it('does not invent an address when discovery has no linked receiver, and retries API errors', async () => {
    server.use(http.get('/api/v1/apm/ingestion', () => HttpResponse.json({ message: 'Unavailable' }, { status: 503 })));
    render(<ServiceSetup service={service} params={params} onClose={vi.fn()} />);
    expect(await screen.findByText('接入配置加载失败')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '复制配置' })).toBeDisabled();
    respond([]);
    fireEvent.click(screen.getByRole('button', { name: '重试' }));
    expect(await screen.findByText('无法定位接收端')).toBeInTheDocument();
    expect(screen.queryByLabelText('生成的接入配置')).not.toBeInTheDocument();
  });

  it('preserves missing identity fields and reports clipboard failures without losing the generated config', async () => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } });
    respond([{ ...target, instance: { ...target.instance, instance_id: 'worker=3,region=east', version: '' }, location: 'docker', metrics: false, endpoint: 'http://172.23.0.1:4318' }]);
    render(<ServiceSetup service={{ ...service, identity: { service_name: "billing'api", environment: '', service_namespace: '' } }} params={params} onClose={vi.fn()} />);
    const config = (await screen.findByLabelText('生成的接入配置')).textContent!;
    expect(config).toContain(`export OTEL_SERVICE_NAME='billing'"'"'api'`);
    expect(config).not.toContain('worker%3D3%2Cregion%3Deast');
    expect(config).toContain('cat /proc/sys/kernel/random/uuid');
    expect(config).not.toMatch(/service.namespace=|deployment.environment.name=|service.version=/);
    fireEvent.click(screen.getByRole('button', { name: '复制配置' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('复制失败');
    expect(screen.getByLabelText('生成的接入配置')).toHaveTextContent('http://172.23.0.1:4318');
  });
});
