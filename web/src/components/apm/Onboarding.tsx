import { useState } from 'react';
import { useI18n } from '@/i18n/locale';
import { Button, Card } from '@/components/ui';

export function Onboarding() {
  const { tr } = useI18n();
  const [language, setLanguage] = useState('java');
  const [endpoint, setEndpoint] = useState('http://127.0.0.1:4318/v1/traces');
  const [service, setService] = useState('order-api');
  const [namespace, setNamespace] = useState('trade');
  const [environment, setEnvironment] = useState('production');
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState('');
  const input = 'rounded-md border border-zinc-800 bg-zinc-950 p-2 text-xs';
  const valid =
    /^https?:\/\/[^\s]+$/.test(endpoint) &&
    [service, namespace, environment].every((v) => /^[a-zA-Z0-9_.-]{1,128}$/.test(v));
  const quote = (value: string) => "'" + value.replace(/'/g, "'\\''") + "'";
  const commands: Record<string, string> = {
    java: 'java -javaagent:/path/to/opentelemetry-javaagent.jar -jar app.jar',
    node: 'node --require @opentelemetry/auto-instrumentations-node/register app.js',
    python: 'opentelemetry-instrument python app.py',
    go: '# Initialize the official OTel Go SDK and HTTP/gRPC instrumentation.\n# See docs/runbooks/apm-service-performance.md for the runnable example.',
  };
  const config = [
    `export OTEL_SERVICE_NAME=${quote(service)}`,
    `export OTEL_RESOURCE_ATTRIBUTES=${quote(`service.namespace=${namespace},deployment.environment.name=${environment}`)}`,
    `export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL='http/protobuf'`,
    `export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=${quote(endpoint)}`,
    `export OTEL_PROPAGATORS='tracecontext,baggage'`,
    `export OTEL_METRICS_EXPORTER='none'`,
    `export OTEL_LOGS_EXPORTER='none'`,
    commands[language],
  ].join('\n');
  return (
    <Card className="space-y-4">
      <h2 className="text-sm font-semibold">{tr('应用接入', 'Instrument an application')}</h2>
      <p className="text-xs text-zinc-500">
        {tr(
          '使用官方 SDK 或自动埋点包。主机选择已启用的本机 Edge 接收端；Docker 使用容器可达地址；Kubernetes 使用已部署 Telemetry Gateway 的 Service 地址。',
          'Use the official SDK or instrumentation package. On hosts, use the enabled local Edge receiver; in Docker, use a container-reachable address; in Kubernetes, use the deployed Telemetry Gateway Service.',
        )}
      </p>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <label className="flex flex-col gap-1 text-xs">
          {tr('语言', 'Language')}
          <select className={input} value={language} onChange={(e) => setLanguage(e.target.value)}>
            {['java', 'node', 'python', 'go'].map((v) => (
              <option key={v}>{v}</option>
            ))}
          </select>
        </label>
        {[
          [tr('Trace 接收地址', 'Trace endpoint'), endpoint, setEndpoint],
          [tr('服务名', 'Service name'), service, setService],
          [tr('业务命名空间', 'Service namespace'), namespace, setNamespace],
          [tr('环境', 'Environment'), environment, setEnvironment],
        ].map(([label, value, setter]) => (
          <label className="flex flex-col gap-1 text-xs" key={String(label)}>
            {String(label)}
            <input
              className={input}
              value={String(value)}
              onChange={(e) => (setter as (s: string) => void)(e.target.value)}
            />
          </label>
        ))}
      </div>
      {!valid && (
        <p role="alert" className="text-xs text-amber-500">
          {tr(
            '使用 HTTP(S) 地址；身份字段仅支持字母、数字、点、横线和下划线。',
            'Use an HTTP(S) endpoint and identity fields containing letters, numbers, dots, dashes or underscores.',
          )}
        </p>
      )}
      <pre className="overflow-auto rounded-lg bg-zinc-950 p-4 text-xs">{config}</pre>
      <Button
        disabled={!valid}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(config);
            setCopied(true);
            setError('');
          } catch {
            setError(tr('复制失败，请手动选择文本。', 'Copy failed; select the text manually.'));
          }
        }}
      >
        {copied ? tr('已复制', 'Copied') : tr('复制配置', 'Copy configuration')}
      </Button>
      {error && <p role="alert">{error}</p>}
      <p className="text-xs text-zinc-500">
        {tr(
          '先安装对应的官方埋点组件，再启动应用并发送真实请求。配置生成不代表接入成功：返回服务列表，进入“接入诊断”检查 Trace、指标和日志关联。采样覆盖率需要单独核验。',
          'Install the official instrumentation first, then start the application and send real requests. Configuration alone does not confirm ingestion: use service diagnostics to inspect traces, metrics and log correlation. Verify sampling coverage separately.',
        )}
      </p>
      <div className="flex flex-wrap gap-4 text-xs underline">
        {[
          ['Java', 'https://opentelemetry.io/docs/zero-code/java/agent/'],
          ['Node.js', 'https://opentelemetry.io/docs/zero-code/js/'],
          ['Python', 'https://opentelemetry.io/docs/zero-code/python/'],
          ['Go', 'https://opentelemetry.io/docs/languages/go/instrumentation/'],
        ].map(([name, url]) => (
          <a key={name} href={url} target="_blank" rel="noreferrer">
            {name}
          </a>
        ))}
      </div>
    </Card>
  );
}
