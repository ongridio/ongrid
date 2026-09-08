import { Button, Input, Textarea, Label } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { Select } from '@/components/ui/Select';
import { useEffect, useState } from 'react';
import { Modal } from './Modal';
import { NLQueryHelper } from './NLQueryHelper';
import {
  type MonitorPanel,
  type MonitorPanelInput,
  type MonitorPanelType,
} from '@/api/monitorPanels';
import { useI18n } from '@/i18n/locale';

const TYPE_OPTIONS_DEF: { value: MonitorPanelType; zh: string; en: string }[] = [
  { value: 'timeseries', zh: '时序曲线 (timeseries)', en: 'Time series (timeseries)' },
  { value: 'stat', zh: '单值 (stat)', en: 'Stat (stat)' },
  { value: 'gauge', zh: '仪表盘 (gauge)', en: 'Gauge (gauge)' },
];

const PROMQL_TEMPLATES_DEF: { zh: string; en: string; query: string }[] = [
  { zh: '主机 CPU 使用率 (top 5)', en: 'Host CPU usage (top 5)', query: 'topk(5, 100 - (avg by(device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100))' },
  { zh: '主机内存使用率', en: 'Host memory usage', query: 'host_mem_pct' },
  { zh: '磁盘使用率超 80%', en: 'Disk usage over 80%', query: 'host_disk_used_pct > 80' },
  { zh: '网络入站流量', en: 'Network rx traffic', query: 'rate(node_network_receive_bytes_total[5m])' },
  { zh: 'load1 趋势', en: 'load1 trend', query: 'node_load1' },
];

const UNIT_OPTIONS_DEF: { value: string; zh: string; en: string }[] = [
  { value: '', zh: '无单位 (none)', en: 'None (none)' },
  { value: 'percent', zh: '百分比 (percent)', en: 'Percent (percent)' },
  { value: 'bytes', zh: '字节 (bytes)', en: 'Bytes (bytes)' },
  { value: 'Bps', zh: '吞吐 (Bps)', en: 'Throughput (Bps)' },
  { value: 'reqps', zh: '请求/秒 (req/s)', en: 'Requests/sec (req/s)' },
  { value: 'short', zh: '短数字 (short)', en: 'Short number (short)' },
];

export type MonitorPanelModalProps = {
  open: boolean;
  panel?: MonitorPanel | null; // null/undefined = create mode
  onClose(): void;
  onSubmit(input: MonitorPanelInput): Promise<void>;
};

export function MonitorPanelModal({ open, panel, onClose, onSubmit }: MonitorPanelModalProps) {
  const { tr } = useI18n();
  const TYPE_OPTIONS = TYPE_OPTIONS_DEF.map((o) => ({ value: o.value, label: tr(o.zh, o.en) }));
  const PROMQL_TEMPLATES = PROMQL_TEMPLATES_DEF.map((t) => ({ label: tr(t.zh, t.en), query: t.query }));
  const UNIT_OPTIONS = UNIT_OPTIONS_DEF.map((u) => ({ value: u.value, label: tr(u.zh, u.en) }));
  const isEdit = !!panel;
  const [title, setTitle] = useState('');
  const [type, setType] = useState<MonitorPanelType>('timeseries');
  const [promql, setPromql] = useState('');
  const [legend, setLegend] = useState('');
  const [unit, setUnit] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errMsg, setErrMsg] = useState<string | null>(null);

  // Reset form whenever the modal opens or the editing target changes.
  // Otherwise stale state from a previous edit leaks across opens.
  useEffect(() => {
    if (!open) return;
    setTitle(panel?.title ?? '');
    setType(panel?.type ?? 'timeseries');
    setPromql(panel?.promql ?? '');
    setLegend(panel?.legend ?? '');
    setUnit(panel?.unit ?? '');
    setErrMsg(null);
    setSubmitting(false);
  }, [open, panel]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (submitting) return;
    setErrMsg(null);
    if (!title.trim()) {
      setErrMsg(tr('标题不能为空', 'Title cannot be empty'));
      return;
    }
    if (!promql.trim()) {
      setErrMsg(tr('PromQL 不能为空', 'PromQL cannot be empty'));
      return;
    }
    setSubmitting(true);
    try {
      await onSubmit({
        title: title.trim(),
        type,
        promql: promql.trim(),
        legend: legend.trim(),
        unit: unit.trim(),
      });
    } catch (err) {
      setErrMsg((err as Error)?.message ?? tr('保存失败', 'Save failed'));
      setSubmitting(false);
      return;
    }
    setSubmitting(false);
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={isEdit ? tr('编辑面板', 'Edit panel') : tr('添加面板', 'Add panel')}
      footer={
        <>
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            className="px-3 py-1.5"
          >
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="outline" size="sm"
            type="submit"
            form="monitor-panel-form"
            disabled={submitting}
            className="px-3 py-1.5 text-emerald-200"
          >
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </>
      }
    >
      <form id="monitor-panel-form" onSubmit={handleSubmit} className="space-y-4">
        <Field label={tr('标题', 'Title')}>
          <Input
            type="text"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={tr('如 入站流量 / 业务 QPS', 'e.g. Inbound traffic / Service QPS')}
            className="w-full"
          />
        </Field>

        <Field label={tr('类型', 'Type')}>
          <Select label={tr('类型', 'Type')}
            value={type}
            onValueChange={(selectedValue) => setType(selectedValue as MonitorPanelType)}
            className="w-full"
          >
            {TYPE_OPTIONS.map((o) => (
              <option key={o.value} value={o.value} className="bg-zinc-900">
                {o.label}
              </option>
            ))}
          </Select>
        </Field>

        <Field label={tr('PromQL 查询', 'PromQL query')}>
          <div className="flex items-start gap-1.5">
            <Textarea
              value={promql}
              onChange={(e) => setPromql(e.target.value)}
              autoFocus
              rows={4}
              placeholder={tr(
                '示例: sum by (device_id) (rate(node_network_receive_bytes_total[5m]))',
                'e.g. sum by (device_id) (rate(node_network_receive_bytes_total[5m]))',
              )}
              className="w-full resize-y font-mono"
            />
            <NLQueryHelper
              dialect="promql"
              onAccept={(translated) => {
                // Fill the PromQL textarea only; user hits 保存 themselves.
                setPromql(translated);
              }}
            />
          </div>
          <div className="mt-2 flex flex-wrap gap-1.5">
            <span className="text-[11px] text-zinc-500">{tr('模板:', 'Templates:')}</span>
            {PROMQL_TEMPLATES.map((t) => (
              <Hint key={t.label} content={t.query}><Button variant="outline" size="sm"

                type="button"

                onClick={() => setPromql(t.query)}
                className="px-2 py-0.5"
              >
                {t.label}
              </Button></Hint>
            ))}
          </div>
        </Field>

        <Field label={tr('图例 (legend)', 'Legend (legend)')}>
          <Input
            type="text"
            value={legend}
            onChange={(e) => setLegend(e.target.value)}
            placeholder={tr('示例: {{device_id}} / {{instance}}（留空则按 series 自动）', 'e.g. {{device_id}} / {{instance}} (empty = auto by series)')}
            className="w-full"
          />
          <p className="mt-1 text-[11px] text-zinc-500">
            {tr('支持 ', 'Supports ')}{'{{label}}'}{tr(' 占位符，与 Grafana 一致。', ' placeholder, same as Grafana.')}
          </p>
        </Field>

        <Field label={tr('单位', 'Unit')}>
          <Select label={tr('单位', 'Unit')}
            value={unit}
            onValueChange={(selectedValue) => setUnit(selectedValue)}
            className="w-full"
          >
            {UNIT_OPTIONS.map((o) => (
              <option key={o.value} value={o.value} className="bg-zinc-900">
                {o.label}
              </option>
            ))}
          </Select>
        </Field>

        {errMsg && (
          <div className="rounded-md border border-red-700/40 bg-red-900/20 px-3 py-2 text-xs text-red-200">
            {errMsg}
          </div>
        )}
        <p className="text-[11px] text-zinc-500">
          {tr('保存后会异步同步到 Grafana 的 ongrid-monitor 仪表盘（单向：ongrid → Grafana）。同步失败不影响本面板渲染。', 'After save, the panel is asynchronously synced to the Grafana ongrid-monitor dashboard (one-way: ongrid → Grafana). A sync failure does not affect rendering here.')}
        </p>
      </form>
    </Modal>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Label className="block">
      <span className="mb-1 block text-[11px] font-medium uppercase tracking-wide text-zinc-400">
        {label}
      </span>
      {children}
    </Label>
  );
}
