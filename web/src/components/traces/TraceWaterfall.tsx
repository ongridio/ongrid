import { Label, Input } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  ListTree,
  Maximize2,
  Minimize2,
  Search,
} from 'lucide-react';
import {
  type OtlpAttribute,
  type OtlpResourceSpans,
  type OtlpSpanEvent,
  type TraceGetResponse,
} from '@/api/traces';
import { Button, Card, Chip } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

export type TraceSpanNode = {
  key: string;
  spanId?: string;
  parentSpanId?: string;
  parentKey?: string;
  name: string;
  service: string;
  peerService?: string;
  kind?: number | string;
  startMs: number;
  endMs: number;
  durationMs: number;
  statusCode?: number | string;
  statusMessage?: string;
  attributes: OtlpAttribute[];
  resourceAttributes: OtlpAttribute[];
  events: OtlpSpanEvent[];
  children: TraceSpanNode[];
};

export type TraceWaterfallModel = {
  spans: TraceSpanNode[];
  roots: TraceSpanNode[];
  byKey: Map<string, TraceSpanNode>;
  startMs: number;
  endMs: number;
  durationMs: number;
  errorCount: number;
  services: string[];
};

type VisibleSpan = { node: TraceSpanNode; depth: number };

type Props = {
  trace: TraceGetResponse;
  traceId: string;
  fallbackService?: string;
  fallbackName?: string;
};

export function TraceWaterfall({ trace, traceId, fallbackService, fallbackName }: Props) {
  const { tr } = useI18n();
  const model = useMemo(() => buildTraceWaterfallModel(trace), [trace]);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [collapsedKeys, setCollapsedKeys] = useState<Set<string>>(new Set());
  const [filterText, setFilterText] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const workspaceRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setSelectedKey(model.roots[0]?.key ?? model.spans[0]?.key ?? null);
    setCollapsedKeys(new Set());
    setFilterText('');
    setErrorsOnly(false);
  }, [model]);

  useEffect(() => {
    const syncFullscreen = () => setIsFullscreen(document.fullscreenElement === workspaceRef.current);
    document.addEventListener('fullscreenchange', syncFullscreen);
    return () => document.removeEventListener('fullscreenchange', syncFullscreen);
  }, []);

  const visible = useMemo(
    () => visibleSpans(model, collapsedKeys, filterText, errorsOnly),
    [model, collapsedKeys, filterText, errorsOnly],
  );
  const selected = (selectedKey && model.byKey.get(selectedKey)) || visible[0]?.node || null;
  const root = model.roots[0] ?? model.spans[0];
  const service = root?.service || fallbackService || '-';
  const name = root?.name || fallbackName || tr('未命名 trace', 'Unnamed trace');

  if (model.spans.length === 0) {
    return (
      <Card className="text-xs text-zinc-500">
        {tr('trace 详情为空（Tempo 可能尚未刷盘）。', 'No span detail (Tempo may not have flushed yet).')}
      </Card>
    );
  }

  const toggleCollapsed = (key: string) => {
    setCollapsedKeys((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const toggleFullscreen = () => {
    const action = document.fullscreenElement
      ? document.exitFullscreen()
      : workspaceRef.current?.requestFullscreen();
    void action?.catch(() => undefined);
  };

  return (
    <div
      ref={workspaceRef}
      className={cn('space-y-3', isFullscreen && 'h-screen overflow-auto bg-zinc-950 p-4')}
      data-testid="trace-waterfall"
    >
      <Card className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <span className={cn('h-2 w-2 shrink-0 rounded-full', model.errorCount ? 'bg-red-500' : 'bg-zinc-500')} />
            <Hint content={name}><h2 className="truncate font-mono text-sm font-medium text-zinc-100" >{name}</h2></Hint>
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-zinc-500">
            <span>{service}</span>
            <span>·</span>
            <Hint content={traceId}><span className="font-mono" >{shortId(traceId)}</span></Hint>
            <CopyValueButton value={traceId} label={tr('复制 trace_id', 'Copy trace_id')} />
          </div>
        </div>
        <div className="flex flex-wrap items-center justify-end gap-1.5">
          <Chip>{formatDuration(model.durationMs)}</Chip>
          <Chip>{tr(`${model.spans.length} 个 Span`, `${model.spans.length} spans`)}</Chip>
          <Chip>{tr(`${model.services.length} 个服务`, `${model.services.length} services`)}</Chip>
          {model.errorCount > 0 && (
            <Chip tone="danger">{tr(`${model.errorCount} 个错误`, `${model.errorCount} errors`)}</Chip>
          )}
          <Hint content={isFullscreen ? tr('退出全屏', 'Exit fullscreen') : tr('最大化链路工作区', 'Maximize trace workspace')}><Button
            onClick={toggleFullscreen}
            aria-pressed={isFullscreen}

          >
            {isFullscreen ? <Minimize2 size={12} /> : <Maximize2 size={12} />}
            {isFullscreen ? tr('退出全屏', 'Exit fullscreen') : tr('最大化', 'Maximize')}
          </Button></Hint>
        </div>
      </Card>

      <section className="overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40">
        <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800/60 px-3 py-2.5">
          <Label className="relative min-w-56 flex-1">
            <Search size={12} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500" />
            <Input
              value={filterText}
              onChange={(event) => setFilterText(event.target.value)}
              aria-label={tr('搜索 Span', 'Search spans')}
              placeholder={tr('搜索 Span、服务或 Span ID', 'Search span, service, or span ID')}
              className="w-full pl-8 pr-2"
            />
          </Label>
          <Button
            aria-pressed={errorsOnly}
            onClick={() => setErrorsOnly((value) => !value)}
            className={cn(errorsOnly && 'border-red-500/40 bg-red-500/10 text-red-300')}
          >
            <AlertTriangle size={12} /> {tr('只看错误', 'Errors only')}
          </Button>
          <Hint content={tr('展开全部 Span', 'Expand all spans')}><Button onClick={() => setCollapsedKeys(new Set())} >
            <ListTree size={12} /> {tr('展开全部', 'Expand all')}
          </Button></Hint>
          <Hint content={tr('折叠所有父 Span', 'Collapse all parent spans')}><Button
            onClick={() => setCollapsedKeys(new Set(model.spans.filter((span) => span.children.length > 0).map((span) => span.key)))}

          >
            {tr('折叠全部', 'Collapse all')}
          </Button></Hint>
          <span className="ml-auto text-[11px] text-zinc-500">
            {tr(`显示 ${visible.length}/${model.spans.length}`, `${visible.length}/${model.spans.length} shown`)}
          </span>
        </div>

        <div className="grid grid-cols-[minmax(260px,36%)_minmax(420px,1fr)] border-b border-zinc-800/60 bg-zinc-950/40 text-[10px] uppercase tracking-wide text-zinc-500">
          <div className="border-r border-zinc-800/60 px-3 py-2">{tr('调用树', 'Span tree')}</div>
          <div className="relative px-3 py-2">
            <div className="flex justify-between font-mono normal-case tracking-normal">
              {[0, 0.25, 0.5, 0.75, 1].map((ratio) => (
                <span key={ratio}>{formatDuration(model.durationMs * ratio)}</span>
              ))}
            </div>
          </div>
        </div>

        <div className={cn('overflow-auto', isFullscreen ? 'max-h-[calc(100vh-260px)]' : 'max-h-[430px]')}>
          {visible.length === 0 ? (
            <div className="px-4 py-10 text-center text-xs text-zinc-500">
              {tr('没有匹配的 Span', 'No matching spans')}
            </div>
          ) : (
            visible.map(({ node, depth }) => {
              const active = selected?.key === node.key;
              const collapsed = collapsedKeys.has(node.key);
              return (
                <div
                  key={node.key}
                  className={cn(
                    'grid min-w-[760px] grid-cols-[minmax(260px,36%)_minmax(420px,1fr)] border-b border-zinc-800/40 text-[11px] last:border-b-0',
                    active ? 'bg-zinc-800/60' : 'hover:bg-zinc-900/60',
                  )}
                >
                  <div className="flex min-w-0 items-center border-r border-zinc-800/60 px-2 py-1.5">
                    <span className="shrink-0" style={{ width: depth * 14 }} aria-hidden="true" />
                    {node.children.length > 0 ? (
                      <Button variant="subtle" size="sm"
                        type="button"
                        onClick={() => toggleCollapsed(node.key)}
                        className="mr-1 p-0.5"
                        aria-label={collapsed ? tr('展开子 Span', 'Expand child spans') : tr('折叠子 Span', 'Collapse child spans')}
                      >
                        {collapsed ? <ChevronRight size={12} /> : <ChevronDown size={12} />}
                      </Button>
                    ) : (
                      <span className="mr-1 w-[16px] shrink-0" />
                    )}
                    <button
                      type="button"
                      onClick={() => setSelectedKey(node.key)}
                      className="flex min-w-0 flex-1 items-center gap-2 text-left focus:outline-none"
                    >
                      <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', isErrorStatus(node.statusCode) ? 'bg-red-500' : 'bg-zinc-500')} />
                      <span className="min-w-0 flex-1">
                        <Hint content={node.name}><span className="block truncate font-mono text-zinc-200" >{node.name}</span></Hint>
                        <Hint content={spanServiceLabel(node)}><span className="block truncate text-[10px] text-zinc-500" >{spanServiceLabel(node)}</span></Hint>
                      </span>
                      <span className="shrink-0 font-mono text-[10px] text-zinc-400">{formatDuration(node.durationMs)}</span>
                    </button>
                  </div>
                  <button
                    type="button"
                    onClick={() => setSelectedKey(node.key)}
                    className="relative min-h-8 px-3 text-left focus:outline-none"
                    aria-label={tr(`选择 Span ${node.name}`, `Select span ${node.name}`)}
                  >
                    {[0.25, 0.5, 0.75].map((ratio) => (
                      <span
                        key={ratio}
                        className="pointer-events-none absolute inset-y-0 border-l border-zinc-800/60"
                        style={{ left: `${ratio * 100}%` }}
                        aria-hidden="true"
                      />
                    ))}
                    <Hint content={`${node.name} · ${formatDuration(node.durationMs)}`}><span
                      className={cn(
                        'absolute top-1/2 h-3 -translate-y-1/2 rounded-sm',
                        active ? 'bg-indigo-500' : isErrorStatus(node.statusCode) ? 'bg-red-500' : 'bg-sky-500/70',
                      )}
                      style={spanBarStyle(node, model)}

                    /></Hint>
                  </button>
                </div>
              );
            })
          )}
        </div>
      </section>

      {selected && <SpanDetails span={selected} traceStartMs={model.startMs} />}
    </div>
  );
}

function SpanDetails({ span, traceStartMs }: { span: TraceSpanNode; traceStartMs: number }) {
  const { tr } = useI18n();
  const offsetMs = Math.max(0, span.startMs - traceStartMs);
  return (
    <Card className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className={cn('h-2 w-2 rounded-full', isErrorStatus(span.statusCode) ? 'bg-red-500' : 'bg-zinc-500')} />
            <Hint content={span.name}><h3 className="truncate font-mono text-sm font-medium text-zinc-100" >{span.name}</h3></Hint>
          </div>
          <p className="mt-1 text-[11px] text-zinc-500">{spanServiceLabel(span)}</p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <Chip>{spanKindLabel(span.kind)}</Chip>
          <Chip>{formatDuration(span.durationMs)}</Chip>
          <Chip>{tr(`起点 +${formatDuration(offsetMs)}`, `Starts +${formatDuration(offsetMs)}`)}</Chip>
          {isErrorStatus(span.statusCode) ? <Chip tone="danger">error</Chip> : <Chip>{isOkStatus(span.statusCode) ? 'ok' : 'unset'}</Chip>}
        </div>
      </div>

      {span.statusMessage && (
        <div className="flex items-start gap-2 rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
          <AlertTriangle size={12} className="mt-0.5 shrink-0" />
          <span className="break-words">{span.statusMessage}</span>
        </div>
      )}

      <div className="grid gap-3 xl:grid-cols-[280px_minmax(0,1fr)]">
        <section>
          <h4 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-zinc-500">{tr('概览', 'Overview')}</h4>
          <dl className="divide-y divide-zinc-800/60 overflow-hidden rounded-lg border border-zinc-800/60 text-[11px]">
            <DetailRow label="span_id" value={span.spanId || '-'} copy={span.spanId} />
            <DetailRow label="parent_span_id" value={span.parentSpanId || '-'} copy={span.parentSpanId} />
            <DetailRow label={tr('开始', 'Start')} value={`+${formatDuration(offsetMs)}`} />
            <DetailRow label={tr('耗时', 'Duration')} value={formatDuration(span.durationMs)} />
          </dl>
        </section>
        <div className="grid min-w-0 gap-3 lg:grid-cols-2">
          <AttributeList title={tr('Span 属性', 'Span attributes')} attributes={span.attributes} />
          <AttributeList title={tr('资源属性', 'Resource attributes')} attributes={span.resourceAttributes} />
        </div>
      </div>

      {span.events.length > 0 && (
        <section>
          <h4 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-zinc-500">
            {tr(`事件 (${span.events.length})`, `Events (${span.events.length})`)}
          </h4>
          <div className="divide-y divide-zinc-800/60 overflow-hidden rounded-lg border border-zinc-800/60">
            {span.events.map((event, index) => (
              <div key={`${event.name}-${event.timeUnixNano ?? index}`} className="px-3 py-2 text-[11px]">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-mono text-zinc-200">{event.name}</span>
                  <span className="text-zinc-500">+{formatDuration(Math.max(0, nanosToMs(event.timeUnixNano) - traceStartMs))}</span>
                </div>
                {event.attributes && event.attributes.length > 0 && (
                  <div className="mt-1 font-mono text-[10px] text-zinc-500">
                    {event.attributes.map((attribute) => `${attribute.key}=${attributeValue(attribute)}`).join(' · ')}
                  </div>
                )}
              </div>
            ))}
          </div>
        </section>
      )}
    </Card>
  );
}

function DetailRow({ label, value, copy }: { label: string; value: string; copy?: string }) {
  return (
    <div className="grid grid-cols-[104px_minmax(0,1fr)] gap-2 px-3 py-2">
      <dt className="text-zinc-500">{label}</dt>
      <dd className="flex min-w-0 items-center gap-1 font-mono text-zinc-300">
        <Hint content={value}><span className="truncate" >{value}</span></Hint>
        {copy && <CopyValueButton value={copy} label={`Copy ${label}`} />}
      </dd>
    </div>
  );
}

function AttributeList({ title, attributes }: { title: string; attributes: OtlpAttribute[] }) {
  return (
    <section className="min-w-0">
      <h4 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-zinc-500">{title}</h4>
      <dl className="divide-y divide-zinc-800/60 overflow-hidden rounded-lg border border-zinc-800/60 text-[11px]">
        {attributes.length === 0 ? (
          <div className="px-3 py-5 text-center text-zinc-500">-</div>
        ) : attributes.map((attribute) => (
          <div key={attribute.key} className="grid grid-cols-[minmax(110px,34%)_minmax(0,1fr)] gap-2 px-3 py-1.5">
            <Hint content={attribute.key}><dt className="truncate font-mono text-zinc-500" >{attribute.key}</dt></Hint>
            <dd className="break-all font-mono text-zinc-300">{attributeValue(attribute)}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

function CopyValueButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Hint content={label}><Button variant="subtle" size="sm"
      type="button"
      onClick={() => {
        void navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1200);
        });
      }}
      className="shrink-0 p-0.5"

      aria-label={label}
    >
      {copied ? <Check size={10} /> : <Copy size={10} />}
    </Button></Hint>
  );
}

export function buildTraceWaterfallModel(trace: TraceGetResponse): TraceWaterfallModel {
  const groups: OtlpResourceSpans[] = [];
  if (Array.isArray(trace.batches)) groups.push(...trace.batches);
  if (Array.isArray(trace.resourceSpans)) groups.push(...trace.resourceSpans);

  const spans: TraceSpanNode[] = [];
  const usedKeys = new Set<string>();
  const bySpanId = new Map<string, TraceSpanNode>();

  groups.forEach((group, groupIndex) => {
    const resourceAttributes = group.resource?.attributes ?? [];
    const service = readAttribute(resourceAttributes, 'service.name') ?? '';
    const scopes = [...(group.scopeSpans ?? []), ...(group.instrumentationLibrarySpans ?? [])];
    scopes.forEach((scope, scopeIndex) => {
      (scope.spans ?? []).forEach((span, spanIndex) => {
        const fallbackKey = `${groupIndex}:${scopeIndex}:${spanIndex}`;
        const preferredKey = span.spanId || fallbackKey;
        const key = usedKeys.has(preferredKey) ? fallbackKey : preferredKey;
        usedKeys.add(key);
        const startMs = nanosToMs(span.startTimeUnixNano);
        const endMs = nanosToMs(span.endTimeUnixNano);
        const node: TraceSpanNode = {
          key,
          spanId: span.spanId,
          parentSpanId: span.parentSpanId,
          name: span.name,
          service,
          peerService: readAttribute(span.attributes ?? [], 'peer.service'),
          kind: span.kind,
          startMs,
          endMs,
          durationMs: Math.max(0, endMs - startMs),
          statusCode: span.status?.code,
          statusMessage: span.status?.message,
          attributes: span.attributes ?? [],
          resourceAttributes,
          events: span.events ?? [],
          children: [],
        };
        spans.push(node);
        if (span.spanId && !bySpanId.has(span.spanId)) bySpanId.set(span.spanId, node);
      });
    });
  });

  for (const node of spans) {
    const parent = node.parentSpanId ? bySpanId.get(node.parentSpanId) : undefined;
    if (parent && parent !== node) {
      node.parentKey = parent.key;
      parent.children.push(node);
    }
  }

  const compare = (a: TraceSpanNode, b: TraceSpanNode) => a.startMs - b.startMs || b.durationMs - a.durationMs;
  spans.sort(compare);
  for (const node of spans) node.children.sort(compare);

  const roots = spans.filter((span) => !span.parentKey);
  const reachable = new Set<string>();
  const markReachable = (node: TraceSpanNode) => {
    if (reachable.has(node.key)) return;
    reachable.add(node.key);
    for (const child of node.children) markReachable(child);
  };
  roots.forEach(markReachable);
  for (const node of spans) {
    if (!reachable.has(node.key)) {
      roots.push(node);
      markReachable(node);
    }
  }
  roots.sort(compare);

  const starts = spans.map((span) => span.startMs).filter(Number.isFinite);
  const ends = spans.map((span) => span.endMs).filter(Number.isFinite);
  const startMs = starts.length ? Math.min(...starts) : 0;
  const endMs = ends.length ? Math.max(...ends) : startMs;
  const services = Array.from(new Set(
    spans.flatMap((span) => [span.service, span.peerService]).filter((service): service is string => Boolean(service)),
  )).sort();
  return {
    spans,
    roots,
    byKey: new Map(spans.map((span) => [span.key, span])),
    startMs,
    endMs,
    durationMs: Math.max(0, endMs - startMs),
    errorCount: spans.filter((span) => isErrorStatus(span.statusCode)).length,
    services,
  };
}

function visibleSpans(
  model: TraceWaterfallModel,
  collapsedKeys: Set<string>,
  filterText: string,
  errorsOnly: boolean,
): VisibleSpan[] {
  const query = filterText.trim().toLowerCase();
  const filtering = Boolean(query || errorsOnly);
  const allowed = new Set<string>();

  if (filtering) {
    for (const span of model.spans) {
      const matchesQuery = !query || `${span.name} ${spanServiceLabel(span)} ${span.spanId ?? ''}`.toLowerCase().includes(query);
      const matchesError = !errorsOnly || isErrorStatus(span.statusCode);
      if (!matchesQuery || !matchesError) continue;
      let current: TraceSpanNode | undefined = span;
      const seenParents = new Set<string>();
      while (current && !seenParents.has(current.key)) {
        allowed.add(current.key);
        seenParents.add(current.key);
        current = current.parentKey ? model.byKey.get(current.parentKey) : undefined;
      }
    }
  }

  const out: VisibleSpan[] = [];
  const visited = new Set<string>();
  const visit = (node: TraceSpanNode, depth: number) => {
    if (visited.has(node.key)) return;
    visited.add(node.key);
    if (!filtering || allowed.has(node.key)) out.push({ node, depth });
    if (!filtering && collapsedKeys.has(node.key)) return;
    for (const child of node.children) visit(child, depth + 1);
  };
  for (const root of model.roots) visit(root, 0);
  return out;
}

function spanServiceLabel(span: TraceSpanNode): string {
  if (span.peerService && span.peerService !== span.service) return `${span.service || '-'} → ${span.peerService}`;
  return span.service || span.peerService || '-';
}

function spanBarStyle(node: TraceSpanNode, model: TraceWaterfallModel) {
  const total = Math.max(model.durationMs, 0.001);
  const left = clamp(((node.startMs - model.startMs) / total) * 100, 0, 100);
  const maxWidth = Math.max(0.25, 100 - left);
  const width = Math.min(maxWidth, Math.max(0.25, (Math.max(node.durationMs, 0.001) / total) * 100));
  return { left: `${left}%`, width: `${width}%` };
}

function attributeValue(attribute: OtlpAttribute): string {
  return valueText(attribute.value);
}

function valueText(value: OtlpAttribute['value']): string {
  if (!value) return '';
  if (value.stringValue !== undefined) return value.stringValue;
  if (value.intValue !== undefined) return String(value.intValue);
  if (value.doubleValue !== undefined) return String(value.doubleValue);
  if (value.boolValue !== undefined) return String(value.boolValue);
  if (value.bytesValue !== undefined) return value.bytesValue;
  if (value.arrayValue?.values) return `[${value.arrayValue.values.map(valueText).join(', ')}]`;
  if (value.kvlistValue?.values) {
    return `{ ${value.kvlistValue.values.map((attribute) => `${attribute.key}: ${attributeValue(attribute)}`).join(', ')} }`;
  }
  return '';
}

function readAttribute(attributes: OtlpAttribute[], key: string): string | undefined {
  const value = attributes.find((attribute) => attribute.key === key);
  return value ? attributeValue(value) : undefined;
}

function nanosToMs(value?: string): number {
  if (!value) return 0;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed / 1_000_000 : 0;
}

function isErrorStatus(code?: number | string): boolean {
  return code === 2 || code === '2' || code === 'STATUS_CODE_ERROR';
}

function isOkStatus(code?: number | string): boolean {
  return code === 1 || code === '1' || code === 'STATUS_CODE_OK';
}

function spanKindLabel(kind?: number | string): string {
  if (kind === undefined || kind === null) return '-';
  if (typeof kind === 'string' && /^[A-Z_]+$/.test(kind)) return kind.replace(/^SPAN_KIND_/, '').toLowerCase();
  const numeric = typeof kind === 'number' ? kind : Number(kind);
  return ['-', 'internal', 'server', 'client', 'producer', 'consumer'][numeric] ?? '-';
}

function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '-';
  if (ms === 0) return '0ms';
  if (ms < 1) return `${(ms * 1000).toFixed(0)}μs`;
  if (ms < 1000) return `${ms.toFixed(ms < 10 ? 2 : 1)}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

function shortId(id: string): string {
  if (id.length <= 16) return id;
  return `${id.slice(0, 8)}…${id.slice(-6)}`;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
