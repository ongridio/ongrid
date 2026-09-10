import configurationGuide from '../../../../docs/guides/apm-configuration-files.md?raw';
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Label, Input } from '@/components/ui';
import { Select } from '@/components/ui/Select';
import { useState } from 'react';
import { useI18n } from '@/i18n/locale';
import { Button, Card } from '@/components/ui';

export function Onboarding() {
  const { tr } = useI18n();
  const [language, setLanguage] = useState('java');
  const [endpoint, setEndpoint] = useState('http://127.0.0.1:4318');
  const [service, setService] = useState('order-api');
  const [namespace, setNamespace] = useState('trade');
  const [environment, setEnvironment] = useState('production');
  const [copied, setCopied] = useState(false);
  const [showGuide, setShowGuide] = useState(false);
  const [error, setError] = useState('');
  const input = "";
  const valid =
    /^https?:\/\/[^\s]+$/.test(endpoint) &&
    [service, namespace, environment].every((v) => /^[a-zA-Z0-9_.-]{1,128}$/.test(v));
  const quote = (value: string) => "'" + value.replace(/'/g, "'\\''") + "'";
  const commands: Record<string, string> = {
    java: '# For precise RPC buckets with Agent 2.31.1, build the SDK View extension\n# in examples/apm-languages/java and add -Dotel.javaagent.extensions=/path/to/apm-java-1.0.jar.\njava -javaagent:/path/to/opentelemetry-javaagent.jar -jar app.jar',
    node: '# For gRPC metrics, register grpcMetricsInterceptor on grpc.Server.\n# See examples/apm-languages/grpc-metrics.cjs.\nnode --require @opentelemetry/auto-instrumentations-node/register app.js',
    python: '# Install grpcio-observability matching grpcio, then register OpenTelemetryPlugin\n# with a MeterProvider and seconds-based histogram View before creating gRPC servers. See examples/apm-languages/app.py.\nopentelemetry-instrument python app.py',
    go: '# Initialize the official OTel Go SDK and HTTP/gRPC instrumentation.\n# See docs/runbooks/apm-service-performance.md for the runnable example.',
    dotnet: '# Register the official OTel SDK, ASP.NET Core instrumentation and OTLP exporters in Program.cs.\ndotnet App.dll',
    php: 'export OTEL_PHP_AUTOLOAD_ENABLED=true\n# Install the OTel extension, SDK, OTLP exporter and framework instrumentation.\nphp app.php',
    cpp: '# Initialize official opentelemetry-cpp providers and instrument application requests.\nexport OTEL_TRACES_SAMPLER=always_on\n./app',
    rust: '# Initialize official OTel Rust providers and instrument application requests.\n./target/release/ongrid-apm-rust-example',
    ruby: '# Configure the official OTel Ruby SDK and framework instrumentation before starting.\nexport OTEL_METRICS_EXPORTER=none\nbundle exec ruby app.rb',
  };
  const config = [
    `export OTEL_SERVICE_NAME=${quote(service)}`,
    `export OTEL_RESOURCE_ATTRIBUTES=${quote(`service.namespace=${namespace},deployment.environment.name=${environment}`)}`,
    `export OTEL_EXPORTER_OTLP_PROTOCOL='http/protobuf'`,
    `export OTEL_EXPORTER_OTLP_ENDPOINT=${quote(endpoint.replace(/\/$/, ''))}`,
    `export OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE='cumulative'`,
    `export OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION='explicit_bucket_histogram'`,
    `export OTEL_SEMCONV_STABILITY_OPT_IN='http,rpc'`,
    `export OTEL_TRACES_EXPORTER='otlp'`,
    `export OTEL_PROPAGATORS='tracecontext,baggage'`,
    `export OTEL_METRICS_EXPORTER='otlp'`,
    `export OTEL_LOGS_EXPORTER='none'`,
    commands[language],
  ].join('\n');
  return (
    <Card className="space-y-4">
      <h2 className="text-sm font-semibold">{tr('应用接入', 'Instrument an application')}</h2>
      <p className="text-xs text-zinc-500">
        {tr(
          '启用官方 HTTP / RPC 埋点和 Metrics 导出。主机需启用 Edge 的 traces 与 metrics 插件；Docker 使用容器可达地址；Kubernetes 使用 Telemetry Gateway Service 地址。',
          'Enable official HTTP / RPC instrumentation and Metrics export. Hosts require the Edge traces and metrics plugins; Docker needs a container-reachable address; Kubernetes uses the Telemetry Gateway Service.',
        )}
      </p>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Label className="flex flex-col gap-1 text-xs">
          {tr('语言', 'Language')}
          <Select aria-label={tr('语言', 'Language')} className="w-full" value={language} onValueChange={(selectedValue) => setLanguage(selectedValue)}>
            {Object.entries({ java: 'Java', node: 'Node.js', python: 'Python', go: 'Go', dotnet: 'C# / .NET', php: 'PHP', cpp: 'C++', rust: 'Rust', ruby: 'Ruby' }).map(([value, label]) => (
              <option key={value} value={value}>{label}</option>
            ))}
          </Select>
        </Label>
        {[
          [tr('OTLP 基础地址', 'OTLP base endpoint'), endpoint, setEndpoint],
          [tr('服务名', 'Service name'), service, setService],
          [tr('业务命名空间', 'Service namespace'), namespace, setNamespace],
          [tr('环境', 'Environment'), environment, setEnvironment],
        ].map(([label, value, setter]) => (
          <Label className="flex flex-col gap-1 text-xs" key={String(label)}>
            {String(label)}
            <Input
              className={input}
              value={String(value)}
              onChange={(e) => (setter as (s: string) => void)(e.target.value)}
            />
          </Label>
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
      <p className="text-xs text-zinc-500">
        {tr(
          'HTTP 使用路由模板，RPC 使用服务/方法名，避免将请求 ID 放进指标。具体支持取决于语言和埋点版本。Go 还需在代码中初始化 MeterProvider。',
          'Use HTTP route templates and RPC service/method names, never request IDs. Support depends on language and instrumentation version. Go also requires a MeterProvider in code.',
        )}
      </p>
      {language === 'java' && <p className="text-xs text-zinc-500">{tr(
        'Java Agent 2.31.1 的默认 RPC 桶较粗。需要毫秒级延迟分位数时，按仓库 examples/apm-languages/java 配置官方 SDK View 扩展；示例镜像已启用。',
        'Java Agent 2.31.1 uses coarse default RPC buckets. For millisecond latency quantiles, configure the official SDK View extension in examples/apm-languages/java; the example image enables it.',
      )}</p>}
      {language === 'python' && (
        <p className="text-xs text-zinc-500">
          {tr(
            'HTTP 指标和 gRPC 链路使用官方自动埋点；gRPC 请求指标需额外注册官方 grpcio-observability 插件，并配置秒级延迟直方图桶。参考仓库 examples/apm-languages/app.py，指标不受 Trace 采样影响。',
            'Official auto-instrumentation provides HTTP metrics and gRPC traces. For gRPC request metrics, also register the official grpcio-observability plugin with seconds-based latency histogram buckets. See examples/apm-languages/app.py; metrics are independent of trace sampling.',
          )}
        </p>
      )}
      {language === 'node' && (
        <p className="text-xs text-zinc-500">{tr(
          'HTTP 指标和 gRPC 链路使用官方自动埋点；gRPC 请求指标需注册示例中的服务端拦截器，通过官方 Metrics API 记录真实请求。参考仓库 examples/apm-languages/grpc-metrics.cjs，仅配置环境变量不会启用该拦截器。',
          'Official auto-instrumentation provides HTTP metrics and gRPC traces. For gRPC request metrics, register the example server interceptor, which measures real calls through the official Metrics API. See examples/apm-languages/grpc-metrics.cjs; environment variables alone do not enable it.',
        )}</p>
      )}
      {language === 'dotnet' && (
        <p className="text-xs text-zinc-500">{tr(
          '示例使用官方 ASP.NET Core 埋点生成 HTTP 指标和 Trace。先安装 OpenTelemetry.Extensions.Hosting、OpenTelemetry.Instrumentation.AspNetCore 和 OpenTelemetry.Exporter.OpenTelemetryProtocol，并注册 TracerProvider 与 MeterProvider；仅配置环境变量不会启用埋点。',
          'The example uses official ASP.NET Core instrumentation for HTTP metrics and traces. Install OpenTelemetry.Extensions.Hosting, OpenTelemetry.Instrumentation.AspNetCore and OpenTelemetry.Exporter.OpenTelemetryProtocol, then register TracerProvider and MeterProvider; environment variables alone do not enable instrumentation.',
        )}</p>
      )}
      {['php', 'cpp', 'rust'].includes(language) && (
        <p className="text-xs text-zinc-500">{tr(
          '示例通过官方 Metrics API 记录真实请求耗时；接收端不会自动为业务代码补齐请求指标。PHP 示例使用长驻 worker 保持计数连续，Slim 自动埋点负责 Trace；C++ / Rust 需要在应用中接入 SDK。Rust SDK 当前为 Beta。请参考仓库 examples/apm-languages 中对应语言示例。',
          'Examples measure real requests through the official Metrics API; the receiver does not add missing instrumentation. PHP uses a long-lived worker for cumulative counters and Slim auto-instrumentation for traces. C++ / Rust require SDK integration in the application. The Rust SDK is currently Beta. See the language examples in examples/apm-languages.',
        )}</p>
      )}
      {language === 'ruby' && (
        <p className="text-xs text-zinc-500">{tr(
          'Ruby 示例使用官方 Sinatra 自动埋点，提供 Trace 与 JSON 日志关联。官方指标 SDK 尚未稳定，不提供全量请求指标；APM 使用服务端 HTTP Trace 补充样本速率、错误率和延迟，并标注“Trace 样本”；请求级告警需要先接入独立的请求指标。',
          'The Ruby example uses official Sinatra instrumentation for traces and correlated JSON logs. Its metrics SDK is not yet stable, so full request metrics are unavailable. APM fills sampled HTTP rates, errors and latency from server traces and labels them Trace samples; request-level alerts require independent request metrics first.',
        )}</p>
      )}
      <p className="text-xs text-zinc-500">
        {tr('也可使用配置文件：Java Agent properties、Spring Boot YAML、.NET appsettings.json；其他语言通过应用配置初始化 SDK。', 'Configuration files are also supported: Java Agent properties, Spring Boot YAML, .NET appsettings.json, or application configuration passed to the SDK.')}{' '}

      </p>
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
      <Button variant="subtle" aria-expanded={showGuide} aria-controls="apm-configuration-guide" onClick={() => setShowGuide(!showGuide)}>
        {showGuide ? tr('收起配置文件接入指南', 'Hide configuration file guide') : tr('查看配置文件接入指南', 'Read configuration file guide (Chinese)')}
      </Button>
      {showGuide && (
        <div id="apm-configuration-guide" className="md-body min-w-0 overflow-x-auto text-sm text-zinc-200">
          <ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={(url) => {
            const safeUrl = defaultUrlTransform(url);
            return safeUrl ? new URL(safeUrl, 'https://github.com/ongridio/ongrid/blob/main/docs/guides/').href : '';
          }} components={{ a: ({ children, ...props }) => <a {...props} target="_blank" rel="noreferrer">{children}</a> }}>
            {configurationGuide}
          </ReactMarkdown>
        </div>
      )}
      <p className="text-xs text-zinc-500">
        {tr(
          '先安装对应的官方埋点组件，再启动应用并发送真实请求。配置生成不代表接入成功：从服务列表打开服务，检查请求指标、链路和日志关联。采样覆盖率需要单独核验。',
          'Install the official instrumentation first, then start the application and send real requests. Configuration alone does not confirm ingestion: open a service to inspect request metrics, traces and log correlation. Verify sampling coverage separately.',
        )}
      </p>
      <div className="flex flex-wrap gap-4 text-xs underline">
        {[
          ['Java', 'https://opentelemetry.io/docs/zero-code/java/agent/'],
          ['Node.js', 'https://opentelemetry.io/docs/zero-code/js/'],
          ['Python', 'https://opentelemetry.io/docs/zero-code/python/'],
          ['Go', 'https://opentelemetry.io/docs/languages/go/instrumentation/'],
          ['C# / .NET', 'https://opentelemetry.io/docs/languages/dotnet/instrumentation/'],
          ['PHP', 'https://opentelemetry.io/docs/zero-code/php/'],
          ['C++', 'https://opentelemetry.io/docs/languages/cpp/instrumentation/'],
          ['Rust', 'https://opentelemetry.io/docs/languages/rust/getting-started/'],
          ['Ruby', 'https://opentelemetry.io/docs/languages/ruby/getting-started/'],
        ].map(([name, url]) => (
          <a key={name} href={url} target="_blank" rel="noreferrer">
            {name}
          </a>
        ))}
      </div>
    </Card>
  );
}
