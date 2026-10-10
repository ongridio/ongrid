import { useEffect, useState } from 'react';
import { queryApm, type ApmIngestion, type ApmSummary } from '@/api/apm';
import { Modal } from '@/components/Modal';
import { Button, EmptyState, FilterField, Select } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

const languages = [
  ['java', 'Java', 'https://opentelemetry.io/docs/zero-code/java/agent/'],
  ['go', 'Go', 'https://opentelemetry.io/docs/languages/go/instrumentation/'],
  ['nodejs', 'Node.js', 'https://opentelemetry.io/docs/zero-code/js/'],
  ['python', 'Python', 'https://opentelemetry.io/docs/zero-code/python/'],
  ['dotnet', '.NET', 'https://opentelemetry.io/docs/languages/dotnet/instrumentation/'],
  ['php', 'PHP', 'https://opentelemetry.io/docs/zero-code/php/'],
  ['ruby', 'Ruby', 'https://opentelemetry.io/docs/languages/ruby/getting-started/'],
  ['cpp', 'C++', 'https://opentelemetry.io/docs/languages/cpp/instrumentation/'],
  ['rust', 'Rust', 'https://opentelemetry.io/docs/languages/rust/getting-started/'],
] as const;

export function ServiceSetup({ service, params, onClose }: { service: ApmSummary; params: URLSearchParams; onClose(): void }) {
  const { tr } = useI18n();
  const [result, setResult] = useState<ApmIngestion>();
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [selected, setSelected] = useState('0');
  const [copied, setCopied] = useState('');
  const [copyError, setCopyError] = useState('');
  const query = params.toString();
  useEffect(() => {
    const controller = new AbortController();
    setResult(undefined); setError('');
    void queryApm('ingestion', new URLSearchParams(query), controller.signal).then(value => {
      if (controller.signal.aborted) return;
      setResult(value);
      const index = Math.max(0, value.targets.findIndex(target => !!target.endpoint));
      setSelected(String(index));
    }).catch((e: Error) => { if (!controller.signal.aborted) setError(e.message); });
    return () => controller.abort();
  }, [query, refresh]);
  const target = result?.targets[Number(selected)];
  const kubernetes = target?.location === 'kubernetes';
  const versionField = target?.version_field_path === "metadata.labels['app.kubernetes.io/version']" ? target.version_field_path : '';
  const attributes = [
    ['service.namespace', service.identity.service_namespace],
    ['deployment.environment.name', service.identity.environment],
    ['service.instance.id', kubernetes ? '$(ONGRID_POD_UID)' : ''],
    ['ongrid.target.id', kubernetes ? '' : target?.target_id],
    ['container.name', target?.location === 'docker' ? target.instance.container_name : ''],
    ['service.version', kubernetes && versionField ? '$(ONGRID_SERVICE_VERSION)' : ''],
  ].filter(([, value]) => !!value).map(([key, value]) => `${key}=${kubernetes && (key === 'service.instance.id' || key === 'service.version') ? value : encodeURIComponent(value!).replace(/%3A/g, ':')}`).join(',');
  const values = [
    ['OTEL_SERVICE_NAME', service.identity.service_name],
    ['OTEL_RESOURCE_ATTRIBUTES', attributes],
    ['OTEL_EXPORTER_OTLP_ENDPOINT', target?.endpoint ?? ''],
    ['OTEL_EXPORTER_OTLP_PROTOCOL', 'http/protobuf'],
  ];
  const shellQuote = (value: string) => `'${value.replace(/'/g, `'"'"'`)}'`;
  const config = kubernetes
    ? 'env:\n  - name: ONGRID_POD_UID\n    valueFrom:\n      fieldRef:\n        fieldPath: metadata.uid\n' + (versionField ? `  - name: ONGRID_SERVICE_VERSION\n    valueFrom:\n      fieldRef:\n        fieldPath: ${versionField}\n` : '') + values.map(([name, value]) => `  - name: ${name}\n    value: ${JSON.stringify(name === 'OTEL_RESOURCE_ATTRIBUTES' ? value : value.replace(/\$/g, '$$$$'))}`).join('\n')
    : 'ONGRID_SERVICE_INSTANCE_ID="$(cat /proc/sys/kernel/random/uuid)" || exit 1\n' + values.map(([name, value]) => `export ${name}=${shellQuote(name === 'OTEL_RESOURCE_ATTRIBUTES' ? `${value}${value ? ',' : ''}service.instance.id=` : value)}${name === 'OTEL_RESOURCE_ATTRIBUTES' ? '"$ONGRID_SERVICE_INSTANCE_ID"' : ''}`).join('\n');
  const detected = service.languages ?? [];
  const language = detected.length === 1 ? detected[0] : '';
  const languageGuide = languages.find(([key]) => key === language);
  const languageHelp: Record<string, string> = {
    java: tr('为应用 JVM 加载 OpenTelemetry Java Agent（-javaagent 指向实际安装路径），再使用下面的配置启动应用。', 'Load the OpenTelemetry Java Agent in the application JVM (-javaagent must point to its installed path), then start the application with the configuration below.'),
    nodejs: tr('安装 OpenTelemetry Node.js 自动埋点和 OTLP exporter，并在业务模块加载前注册；ESM 与 CommonJS 的启动配置不同。', 'Install OpenTelemetry Node.js auto-instrumentation and an OTLP exporter, and register them before application modules load. ESM and CommonJS require different startup configuration.'),
    python: tr('安装 OpenTelemetry Python 自动埋点及 OTLP exporter，安装对应框架的埋点包，使用 opentelemetry-instrument 启动应用。', 'Install OpenTelemetry Python auto-instrumentation, an OTLP exporter and the framework instrumentation packages. Start the application with opentelemetry-instrument.'),
    go: tr('在应用中初始化 TracerProvider、OTLP exporter 及 HTTP/gRPC 埋点；采集指标还需 MeterProvider。仅添加环境变量不会自动启用 Go 埋点。', 'Initialize a TracerProvider, OTLP exporter and HTTP/gRPC instrumentation in the application. Metrics also require a MeterProvider. Environment variables alone do not enable Go instrumentation.'),
    dotnet: tr('注册 OpenTelemetry TracerProvider、框架埋点及 OTLP exporter；采集指标还需 MeterProvider。确认 SDK 或应用配置读取这些字段。', 'Register an OpenTelemetry TracerProvider, framework instrumentation and OTLP exporter. Metrics also require a MeterProvider. Ensure the SDK or application configuration reads these fields.'),
    php: tr('安装 OpenTelemetry PHP 扩展、SDK、OTLP exporter 及框架埋点，启用 OTEL_PHP_AUTOLOAD_ENABLED；指标需要单独核对埋点与进程生命周期。', 'Install the OpenTelemetry PHP extension, SDK, OTLP exporter and framework instrumentation, and enable OTEL_PHP_AUTOLOAD_ENABLED. Check metric instrumentation and process lifetime separately.'),
    ruby: tr('初始化 OpenTelemetry Ruby SDK、OTLP exporter 及框架埋点；请求指标的支持需单独核对，不能仅凭 Trace 判断已接入指标。', 'Initialize the OpenTelemetry Ruby SDK, OTLP exporter and framework instrumentation. Verify request metric support separately; traces do not confirm metric ingestion.'),
    cpp: tr('在应用中初始化 OpenTelemetry C++ SDK、OTLP exporter 和请求埋点，并将下面的字段传入 SDK；环境变量是否自动读取取决于组件。', 'Initialize the OpenTelemetry C++ SDK, OTLP exporter and request instrumentation, and pass the fields below to the SDK. Automatic environment variable handling depends on the component.'),
    rust: tr('在应用中初始化 OpenTelemetry Rust SDK、OTLP exporter 和请求埋点，并将下面的字段传入 SDK；环境变量是否自动读取取决于组件。', 'Initialize the OpenTelemetry Rust SDK, OTLP exporter and request instrumentation, and pass the fields below to the SDK. Automatic environment variable handling depends on the component.'),
  };
  const unavailable: Record<string, string> = {
    resource_unlinked: tr('当前服务尚未关联到设备或 Kubernetes 集群，无法定位接收端。', 'This service is not linked to a device or Kubernetes cluster, so its receiver cannot be resolved.'),
    process_unlinked: tr('当前实例没有可关联的进程 ID，无法确定应用所在的网络。', 'This instance has no linked process ID, so its application network cannot be resolved.'),
    controller_unavailable: tr('所属集群尚未连接 Controller，无法读取 Gateway Service。', 'The cluster controller is not connected, so the Gateway Service cannot be read.'),
    process_unavailable: tr('当前观测实例的进程已退出，请刷新服务列表。', 'The observed process has exited. Refresh the services list.'),
    receiver_unavailable: tr('当前实例没有可用的 OTLP 接收端，请检查所属设备或集群的接收服务。', 'No OTLP receiver is available for this instance. Check the receiver on its device or cluster.'),
  };
  return <Modal open onClose={onClose} title={tr('接入配置', 'Ingestion configuration')} size="xl" footer={<>
    <Button variant="outline" onClick={onClose}>{tr('关闭', 'Close')}</Button>
    <Button disabled={!target?.endpoint} onClick={async () => {
      try { await navigator.clipboard.writeText(config); setCopied(config); setCopyError(''); }
      catch { setCopyError(tr('复制失败，请手动选择配置文本。', 'Copy failed. Select the configuration text manually.')); }
    }}>{copied === config ? tr('已复制', 'Copied') : tr('复制配置', 'Copy configuration')}</Button>
  </>}>
    <div className="space-y-4 text-sm">
      <p className="text-text-muted">{tr('根据当前服务的实例和所属接收端自动生成，地址与服务身份均已填好。将以下配置加入应用的启动配置。', 'Generated from this service’s instances and their receiver. The address and service identity are already filled in. Add the configuration below to the application startup configuration.')}</p>
      <dl className="grid gap-3 sm:grid-cols-3">
        {[[tr('服务名', 'Service name'), service.identity.service_name], [tr('业务命名空间', 'Service namespace'), service.identity.service_namespace], [tr('环境', 'Environment'), service.identity.environment]].map(([name, value]) => <div key={name} className="min-w-0"><dt className="text-xs text-text-muted">{name}</dt><dd className="mt-1 break-all">{value || tr('未设置', 'Unset')}</dd></div>)}
      </dl>
      {error ? <div role="alert"><EmptyState title={tr('接入配置加载失败', 'Could not load ingestion configuration')} hint={error} action={<Button onClick={() => setRefresh(value => value + 1)}>{tr('重试', 'Retry')}</Button>} /></div>
        : !result ? <p role="status" className="py-6 text-text-muted">{tr('正在读取实例与接收端配置…', 'Reading instance and receiver configuration…')}</p>
          : !result.targets.length ? <EmptyState title={tr('无法定位接收端', 'Receiver could not be resolved')} hint={unavailable.resource_unlinked} />
            : <>
              {result.targets.length > 1 && <div className="flex flex-wrap gap-3"><FilterField label={tr('接入实例', 'Instance')}><Select aria-label={tr('接入实例', 'Instance')} value={selected} onValueChange={setSelected} options={result.targets.map((item, index) => ({ value: String(index), label: `${item.instance.pod || item.instance.instance_id || item.instance.device_id}${item.instance.k8s_cluster_id ? ` · #${item.instance.k8s_cluster_id}` : item.instance.device_id ? ` · #${item.instance.device_id}` : ''}` }))} /></FilterField></div>}
              {target?.endpoint ? <>
                <p className="break-all text-xs text-text-muted">{tr('接收端', 'Receiver')} · {target.endpoint} · {target.location === 'kubernetes' ? tr('所属集群 Gateway Service', 'Gateway Service in this cluster') : target.location === 'docker' ? tr('当前容器网络的宿主机接收端', 'Host receiver on this container network') : tr('当前设备的本地接收端', 'Local receiver on this device')}</p>
                <pre aria-label={tr('生成的接入配置', 'Generated ingestion configuration')} className="max-h-96 overflow-auto rounded-lg border border-border bg-bg p-3 font-mono text-xs text-text">{config}</pre>
                <p className="text-xs text-text-muted">{kubernetes ? tr('将 env 合并到业务容器配置。实例 ID 通过 Downward API 使用每个 Pod 的 UID，适用于多副本部署。', 'Merge env into the application container configuration. The Downward API uses each Pod’s UID as the instance ID, including multiple replicas.') : tr('将这段 shell 脚本放在启动应用的命令之前，容器放在 entrypoint 中，每次启动均执行。不要作为 env 文件导入。目标 ID 保持不变，实例 UUID 每次启动更新；独立 worker 需分别执行。', 'Run this shell script before the application command on every start; use the container entrypoint for containers. Do not import it as an env file. The target ID stays stable, while each start gets a new instance UUID. Run it separately for independent workers.')}</p>
                <p className="text-xs text-text-muted">{tr('上报信号、传播方式和指标格式由应用 SDK 配置。合并时保留已有的资源属性。', 'Configure exported signals, propagators and metric formats in the application SDK. Retain existing resource attributes when merging.')}</p>
                {kubernetes && <p className="text-xs text-text-muted">{versionField ? tr('版本从每个 Pod 的 app.kubernetes.io/version 标签注入。发布时更新 Pod 模板标签。', 'The version is injected from each Pod’s app.kubernetes.io/version label. Update the Pod template label for each release.') : tr('当前 Pod 未提供可读取的 app.kubernetes.io/version 标签，未生成版本属性；合并时请保留应用已有的版本属性。Kubernetes 不会自动生成应用版本。', 'The current Pod has no readable app.kubernetes.io/version label, so no version attribute is generated. Retain existing version attributes when merging. Kubernetes does not generate application versions.')}</p>}
                <p className="text-xs text-text-muted">{target.metrics ? tr('当前指标链路要求计数器与直方图使用累计值；APM 请求分位数依赖标准 HTTP/RPC 请求指标的经典直方图。', 'The metric pipeline requires cumulative counters and histograms. APM request percentiles require classic histograms for standard HTTP/RPC request metrics.') : tr('该接收端未启用指标接收，请为指标配置其他接收端。', 'Metrics are not enabled on this receiver. Configure another endpoint for metrics.')}</p>
              </> : <EmptyState title={tr('接收端不可用', 'Receiver unavailable')} hint={unavailable[target?.reason ?? ''] ?? unavailable.receiver_unavailable} />}
            </>}
      {target?.endpoint && <div className="space-y-1 border-t border-border pt-3 text-xs text-text-muted">
        <p>{languageHelp[language] || tr('应用需要启用 OpenTelemetry SDK 或 Agent；环境变量不会自动为业务代码添加埋点。', 'Enable an OpenTelemetry SDK or Agent in the application. Environment variables do not add application instrumentation.')}</p>
        {languageGuide && <a className="og-resource-link" href={languageGuide[2]} target="_blank" rel="noreferrer">{tr('查看官方接入文档', 'Read the official instrumentation guide')}</a>}
      </div>}
      {copyError && <p role="alert" className="text-xs text-red-500">{copyError}</p>}
    </div>
  </Modal>;
}
