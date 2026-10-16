import { useEffect, useRef, useState } from 'react';
import { ArrowRight, Check, Terminal } from 'lucide-react';
import {
  getAgentIngestion,
  type AgentIdentity,
  type AgentIngestion,
} from '@/api/agent-observability';
import { listKubernetesClusters } from '@/api/kubernetes';
import { listDevices } from '@/api/devices';
import { Modal } from '@/components/Modal';
import { Button, Chip, Input, Label, Select } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { CodeValue, CopyButton } from './AgentParts';
import { sdkLabels } from './model';
import { environmentScript, pythonExample, sdkPackages } from './guides';

export function AgentSetup({
  onClose,
  onVerify,
}: {
  onClose(): void;
  onVerify(identity: AgentIdentity): void;
}) {
  const { tr } = useI18n();
  const [sdk, setSDK] = useState('openinference');
  const [location, setLocation] = useState('external');
  const [fields, setFields] = useState({
    service_name: '',
    service_namespace: '',
    environment: '',
    endpoint: '',
    cluster_id: '',
    device_id: '',
    instance_id: '',
    container_name: '',
  });
  const [resources, setResources] = useState<
    { value: string; label: string }[]
  >([]);
  const [resourceError, setResourceError] = useState('');
  const [result, setResult] = useState<AgentIngestion>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const request = useRef<AbortController>();
  useEffect(() => () => request.current?.abort(), []);
  useEffect(() => {
    let cancelled = false;
    setResources([]);
    setResourceError('');
    if (location === 'external') return;
    const load =
      location === 'kubernetes'
        ? listKubernetesClusters({ limit: 200 })
        : listDevices({ roles: 'server' });
    void load
      .then((response) => {
        if (!cancelled)
          setResources(
            (response.items || []).map((item) => ({
              value: String(item.id),
              label: `${item.name} (#${item.id})`,
            })),
          );
      })
      .catch((err: Error) => {
        if (!cancelled) setResourceError(err.message);
      });
    return () => {
      cancelled = true;
    };
  }, [location]);
  const update = (key: keyof typeof fields, value: string) => {
    setFields((current) => ({ ...current, [key]: value }));
    setResult(undefined);
  };
  const generate = async () => {
    request.current?.abort();
    request.current = new AbortController();
    const signal = request.current.signal;
    setBusy(true);
    setError('');
    setResult(undefined);
    try {
      setResult(
        await getAgentIngestion(
          new URLSearchParams({ sdk, location, ...fields }),
          signal,
        ),
      );
    } catch (err) {
      if (!signal.aborted) setError((err as Error).message);
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  };
  const ready = !!result?.receiver.endpoint;
  const environment = result ? environmentScript(result.environment) : '';
  const code = pythonExample(sdk);
  return (
    <Modal
      open
      onClose={onClose}
      title={tr('接入 Agent 应用', 'Connect an Agent application')}
      size="xl"
      footer={
        <>
          <Button onClick={onClose}>{tr('关闭', 'Close')}</Button>
          {ready && (
            <Button
              variant="primary"
              onClick={() => onVerify(result!.identity)}
            >
              {tr('查看接入结果', 'Check received traces')}
              <ArrowRight size={14} />
            </Button>
          )}
        </>
      }
    >
      <p className="mb-5 text-sm text-text-muted">
        {tr(
          '选择现有 SDK，把 OTLP 数据发送到 Ongrid 接收端。先运行示例，再接入你的应用。',
          'Use an existing SDK to send OTLP traces to Ongrid. Run the example, then connect your application.',
        )}
      </p>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void generate();
        }}
        className="space-y-4"
      >
        <fieldset disabled={busy} className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="agent-sdk">
                {tr('采集 SDK', 'Instrumentation SDK')}
              </Label>
              <Select
                id="agent-sdk"
                value={sdk}
                onValueChange={(value) => {
                  setSDK(value);
                  setResult(undefined);
                }}
                className="w-full"
                options={[
                  'openinference',
                  'langsmith',
                  'openllmetry',
                  'langfuse',
                ].map((value) => ({ value, label: sdkLabels[value] }))}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="agent-location">
                {tr('部署位置', 'Deployment')}
              </Label>
              <Select
                id="agent-location"
                value={location}
                onValueChange={(value) => {
                  setLocation(value);
                  setResult(undefined);
                }}
                className="w-full"
                options={[
                  {
                    value: 'external',
                    label: tr('已有 Collector 地址', 'Existing Collector URL'),
                  },
                  {
                    value: 'kubernetes',
                    label: tr('Kubernetes 集群', 'Kubernetes cluster'),
                  },
                  {
                    value: 'host',
                    label: tr('已关联的主机进程', 'Linked host process'),
                  },
                ]}
              />
            </div>
          </div>
          <div className="grid gap-4 sm:grid-cols-3">
            {(
              ['service_name', 'service_namespace', 'environment'] as const
            ).map((key, index) => (
              <div className="space-y-1.5" key={key}>
                <Label htmlFor={`agent-${key}`}>
                  {
                    [
                      tr('应用名称', 'Application name'),
                      tr('命名空间', 'Namespace'),
                      tr('环境', 'Environment'),
                    ][index]
                  }
                </Label>
                <Input
                  id={`agent-${key}`}
                  required
                  maxLength={256}
                  value={fields[key]}
                  onChange={(event) => update(key, event.target.value)}
                  placeholder={
                    ['support-agent', 'customer-success', 'production'][index]
                  }
                  className="w-full"
                />
              </div>
            ))}
          </div>
          {location === 'external' ? (
            <div className="space-y-1.5">
              <Label htmlFor="agent-endpoint">
                {tr('OTLP HTTP 接收地址', 'OTLP HTTP receiver URL')}
              </Label>
              <Input
                id="agent-endpoint"
                type="url"
                required
                maxLength={2048}
                value={fields.endpoint}
                onChange={(event) => update('endpoint', event.target.value)}
                placeholder="http://collector:4318"
                className="w-full font-mono"
              />
              <p className="text-xs text-text-muted">
                {tr(
                  '填写应用可访问的接收地址。不要填写 Manager 管理 API，也不要附带 Edge 管理凭据。',
                  'Use a receiver reachable from the application. Do not use the Manager API or Edge management credentials.',
                )}
              </p>
            </div>
          ) : (
            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="agent-resource">
                  {location === 'kubernetes'
                    ? tr('集群', 'Cluster')
                    : tr('主机', 'Host')}
                </Label>
                <Select
                  id="agent-resource"
                  value={
                    location === 'kubernetes'
                      ? fields.cluster_id
                      : fields.device_id
                  }
                  onValueChange={(value) =>
                    update(
                      location === 'kubernetes' ? 'cluster_id' : 'device_id',
                      value,
                    )
                  }
                  required
                  className="w-full"
                  options={[
                    { value: '', label: tr('请选择资源', 'Select a resource') },
                    ...resources,
                  ]}
                />
              </div>
              {location === 'host' && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label htmlFor="agent-instance">service.instance.id</Label>
                    <Input
                      id="agent-instance"
                      value={fields.instance_id}
                      onChange={(event) =>
                        update('instance_id', event.target.value)
                      }
                      className="w-full"
                      placeholder={tr(
                        '已关联的进程实例标识',
                        'Linked process instance ID',
                      )}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="agent-container">
                      {tr('容器名称（可选）', 'Container name (optional)')}
                    </Label>
                    <Input
                      id="agent-container"
                      value={fields.container_name}
                      onChange={(event) =>
                        update('container_name', event.target.value)
                      }
                      className="w-full"
                    />
                  </div>
                </div>
              )}
              {resourceError && (
                <p role="alert" className="text-xs text-red-500">
                  {resourceError}
                </p>
              )}
            </div>
          )}
        </fieldset>
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="primary" type="submit" disabled={busy}>
            <Terminal size={14} />
            {busy
              ? tr('正在解析接收端…', 'Resolving receiver…')
              : tr('生成接入配置', 'Generate configuration')}
          </Button>
          <Chip>Python</Chip>
          <span className="text-xs text-text-muted">
            {tr(
              '每个应用选择一套埋点，避免重复计数。',
              'Use one instrumentation stack per application to avoid duplicate counts.',
            )}
          </span>
        </div>
        {error && (
          <p role="alert" className="text-sm text-red-500">
            {error}
          </p>
        )}
        {result && !ready && (
          <p role="status" className="text-sm text-amber-500">
            {tr(
              '暂时无法确认接收端。请检查资源连接与进程标识，或填写已有 Collector 地址。',
              'Receiver could not be resolved. Check the resource connection and process identity, or use an existing Collector URL.',
            )}{' '}
            ({result.receiver.reason})
          </p>
        )}
      </form>
      {ready && (
        <div className="mt-6 space-y-5 border-t border-border pt-5">
          <div className="flex flex-wrap items-center gap-2 text-xs text-text-muted">
            <Check size={14} className="text-emerald-500" />
            <span className="break-all font-mono">
              {result!.receiver.endpoint}
            </span>
          </div>
          <div>
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-sm font-medium text-text">
                1. {tr('安装已验证版本', 'Install verified versions')}
              </h3>
              <CopyButton
                value={`pip install ${sdkPackages[sdk]}`}
                label={tr('复制安装命令', 'Copy install command')}
              />
            </div>
            <CodeValue value={`pip install ${sdkPackages[sdk]}`} />
          </div>
          <div>
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-sm font-medium text-text">
                2. {tr('设置环境变量', 'Set environment variables')}
              </h3>
              <CopyButton
                value={environment}
                label={tr('复制环境变量', 'Copy environment variables')}
              />
            </div>
            <CodeValue value={environment} />
          </div>
          <div>
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-sm font-medium text-text">
                3.{' '}
                {tr('运行无模型费用的测试', 'Run a test without model charges')}
              </h3>
              <CopyButton
                value={code}
                label={tr('复制 Python 示例', 'Copy Python example')}
              />
            </div>
            <CodeValue value={code} />
          </div>
          <p className="text-xs leading-relaxed text-text-muted">
            {tr(
              '生产应用在启动时初始化 SDK，并执行真实任务。输入输出可能包含业务内容，请使用 SDK 的内容采集和脱敏设置。此示例只验证 Trace 导出。',
              'Initialize the SDK at application startup and execute a real task. Inputs and outputs may contain business content; use the SDK capture and masking settings. This example validates trace export only.',
            )}
          </p>
        </div>
      )}
    </Modal>
  );
}
