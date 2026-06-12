// FlowEditor — the React Flow canvas for one workflow (HLD-016).
// Palette (left) adds nodes; edges carry control ports (next / true /
// false / error); the drawer (right) edits the selected node's config.
// Data flows through the run context via {{nodes.<id>.output.<path>}}
// templates — see biz/flow/expr.go.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  addEdge,
  Background,
  BackgroundVariant,
  Connection,
  Controls,
  Edge,
  Handle,
  Node,
  NodeProps,
  Position,
  ReactFlow,
  useEdgesState,
  useNodesState,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import {
  ArrowLeft,
  Bell,
  Bot,
  CircleDot,
  GitBranch,
  History,
  Play,
  Save,
  Trash2,
  Variable,
  Wrench,
} from 'lucide-react';

import {
  getFlow,
  getFlowRun,
  listFlowRuns,
  runFlow,
  updateFlow,
  type Flow,
  type FlowGraph,
  type FlowGraphNode,
  type FlowNodeType,
  type FlowRun,
  type FlowRunNode,
} from '@/api/flows';
import { useI18n } from '@/i18n/locale';
import { useAuth } from '@/store/auth';

// ---------- node visual spec ----------------------------------------------

const NODE_META: Record<FlowNodeType, { icon: typeof Bot; color: string; zh: string; en: string }> = {
  'trigger.manual': { icon: CircleDot, color: 'text-emerald-400', zh: '手动触发', en: 'Manual trigger' },
  agent: { icon: Bot, color: 'text-indigo-400', zh: 'Agent', en: 'Agent' },
  tool: { icon: Wrench, color: 'text-sky-400', zh: '工具', en: 'Tool' },
  condition: { icon: GitBranch, color: 'text-amber-400', zh: '条件', en: 'Condition' },
  notify: { icon: Bell, color: 'text-rose-400', zh: '通知', en: 'Notify' },
  set: { icon: Variable, color: 'text-zinc-400', zh: '变量', en: 'Set var' },
};

type CanvasData = {
  flowType: FlowNodeType;
  label: string;
  config: Record<string, unknown>;
  runStatus?: 'running' | 'succeeded' | 'failed';
};

type CanvasNode = Node<CanvasData>;

function statusRing(s?: string): string {
  switch (s) {
    case 'running':
      return 'border-indigo-500 shadow-[0_0_0_1px_rgba(99,102,241,0.6)]';
    case 'succeeded':
      return 'border-emerald-600';
    case 'failed':
      return 'border-red-600';
    default:
      return 'border-zinc-700';
  }
}

function FlowCanvasNode({ data, selected }: NodeProps<CanvasNode>) {
  const meta = NODE_META[data.flowType];
  const Icon = meta?.icon ?? Wrench;
  const isCondition = data.flowType === 'condition';
  const isTrigger = data.flowType.startsWith('trigger.');
  return (
    <div
      className={`min-w-[150px] rounded-lg border bg-zinc-900 px-3 py-2 text-left transition-shadow ${statusRing(
        data.runStatus
      )} ${selected ? 'ring-1 ring-indigo-500' : ''}`}
    >
      {!isTrigger && <Handle type="target" position={Position.Left} className="!h-2.5 !w-2.5 !bg-zinc-500" />}
      <div className="flex items-center gap-2">
        <Icon size={14} className={meta?.color ?? 'text-zinc-400'} />
        <span className="text-[12px] font-medium text-zinc-200">{data.label}</span>
      </div>
      <div className="mt-0.5 text-[10px] uppercase tracking-wide text-zinc-500">{data.flowType}</div>
      {isCondition ? (
        <>
          <Handle id="true" type="source" position={Position.Right} style={{ top: '35%' }} className="!h-2.5 !w-2.5 !bg-emerald-500" />
          <Handle id="false" type="source" position={Position.Right} style={{ top: '70%' }} className="!h-2.5 !w-2.5 !bg-zinc-500" />
        </>
      ) : (
        <Handle id="next" type="source" position={Position.Right} className="!h-2.5 !w-2.5 !bg-indigo-500" />
      )}
      {!isTrigger && (
        <Handle id="error" type="source" position={Position.Bottom} className="!h-2 !w-2 !bg-red-500/80" />
      )}
    </div>
  );
}

const nodeTypes = { flowNode: FlowCanvasNode };

const EDGE_COLOR: Record<string, string> = {
  next: '#6366f1',
  true: '#10b981',
  false: '#71717a',
  error: '#ef4444',
};

// ---------- graph <-> canvas conversion -----------------------------------

function toCanvas(graph: FlowGraph | undefined): { nodes: CanvasNode[]; edges: Edge[] } {
  const nodes: CanvasNode[] = (graph?.nodes ?? []).map((n, i) => ({
    id: n.id,
    type: 'flowNode',
    position: n.position ?? { x: 80 + i * 220, y: 160 },
    data: { flowType: n.type, label: n.name || n.type, config: n.config ?? {} },
  }));
  const edges: Edge[] = (graph?.edges ?? []).map((e) => {
    const port = e.sourcePort || 'next';
    return {
      id: e.id,
      source: e.source,
      sourceHandle: port,
      target: e.target,
      label: port === 'next' ? undefined : port,
      style: { stroke: EDGE_COLOR[port] ?? '#6366f1' },
      labelStyle: { fill: '#a1a1aa', fontSize: 10 },
    };
  });
  return { nodes, edges };
}

function fromCanvas(nodes: CanvasNode[], edges: Edge[]): FlowGraph {
  return {
    nodes: nodes.map(
      (n): FlowGraphNode => ({
        id: n.id,
        type: n.data.flowType,
        name: n.data.label,
        config: n.data.config,
        position: { x: n.position.x, y: n.position.y },
      })
    ),
    edges: edges.map((e) => ({
      id: e.id,
      source: e.source,
      sourcePort: (e.sourceHandle as string) || 'next',
      target: e.target,
    })),
  };
}

// ---------- config drawer field specs --------------------------------------

type FieldSpec = { key: string; zh: string; en: string; kind: 'text' | 'textarea' | 'json'; placeholder?: string };

const CONFIG_FIELDS: Record<FlowNodeType, FieldSpec[]> = {
  'trigger.manual': [],
  agent: [
    { key: 'persona', zh: '角色 (persona)', en: 'Persona', kind: 'text', placeholder: 'default / specialist-network / …' },
    { key: 'instruction', zh: '指令（支持 {{…}} 模板）', en: 'Instruction ({{…}} templates)', kind: 'textarea', placeholder: '诊断 {{trigger.host}} 上的磁盘告警…' },
    { key: 'output_schema', zh: '输出 schema（可选，JSON Schema。声明后下游才能引用 structured 字段）', en: 'Output schema (optional; required for structured downstream refs)', kind: 'json' },
  ],
  tool: [
    { key: 'tool', zh: '工具名', en: 'Tool name', kind: 'text', placeholder: 'query_promql / bash / restart_service / …' },
    { key: 'args', zh: '参数（JSON，值支持 {{…}}）', en: 'Args (JSON; values accept {{…}})', kind: 'json' },
  ],
  condition: [
    { key: 'expr', zh: '表达式', en: 'Expression', kind: 'text', placeholder: '{{nodes.diag.output.structured.severity}} == "critical"' },
  ],
  notify: [
    { key: 'channel_ids', zh: '渠道 ID（JSON 数组）', en: 'Channel ids (JSON array)', kind: 'json', placeholder: '[1]' },
    { key: 'title', zh: '标题', en: 'Title', kind: 'text' },
    { key: 'message', zh: '内容（支持 {{…}}）', en: 'Message ({{…}} templates)', kind: 'textarea' },
  ],
  set: [
    { key: 'name', zh: '变量名', en: 'Variable name', kind: 'text' },
    { key: 'value', zh: '值（支持 {{…}}）', en: 'Value ({{…}} templates)', kind: 'text' },
  ],
};

// ---------- page -----------------------------------------------------------

export default function FlowEditorPage() {
  const { tr, locale } = useI18n();
  const navigate = useNavigate();
  const { id } = useParams();
  const flowID = Number(id);
  const role = useAuth((s) => s.role);
  const canWrite = role !== 'viewer';

  const [flow, setFlow] = useState<Flow | null>(null);
  const [nodes, setNodes, onNodesChange] = useNodesState<CanvasNode>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [runs, setRuns] = useState<FlowRun[]>([]);
  const [showRuns, setShowRuns] = useState(false);
  const [activeRun, setActiveRun] = useState<{ run: FlowRun; nodes: FlowRunNode[] } | null>(null);
  const seq = useRef(1);
  const pollRef = useRef<number | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const f = await getFlow(flowID);
        if (!alive) return;
        setFlow(f);
        const { nodes: ns, edges: es } = toCanvas(f.graph);
        // seed the id counter past existing node ids (n1, n2, …)
        for (const n of ns) {
          const m = /^n(\d+)$/.exec(n.id);
          if (m) seq.current = Math.max(seq.current, Number(m[1]) + 1);
        }
        setNodes(ns);
        setEdges(es);
      } catch (e) {
        if (alive) setError(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => {
      alive = false;
      if (pollRef.current) window.clearInterval(pollRef.current);
    };
  }, [flowID, setNodes, setEdges]);

  const addNode = useCallback(
    (t: FlowNodeType) => {
      const meta = NODE_META[t];
      const nid = `n${seq.current++}`;
      setNodes((ns) => [
        ...ns,
        {
          id: nid,
          type: 'flowNode',
          position: { x: 120 + ns.length * 40, y: 120 + ns.length * 30 },
          data: { flowType: t, label: locale === 'zh-CN' ? meta.zh : meta.en, config: {} },
        },
      ]);
      setSelectedID(nid);
      setDirty(true);
    },
    [locale, setNodes]
  );

  const onConnect = useCallback(
    (c: Connection) => {
      const port = c.sourceHandle || 'next';
      setEdges((es) =>
        addEdge(
          {
            ...c,
            id: `e${Date.now()}_${Math.floor(Math.random() * 1000)}`,
            label: port === 'next' ? undefined : port,
            style: { stroke: EDGE_COLOR[port] ?? '#6366f1' },
            labelStyle: { fill: '#a1a1aa', fontSize: 10 },
          },
          es
        )
      );
      setDirty(true);
    },
    [setEdges]
  );

  const selected = useMemo(() => nodes.find((n) => n.id === selectedID) ?? null, [nodes, selectedID]);

  const patchSelected = useCallback(
    (patch: Partial<CanvasData>) => {
      if (!selectedID) return;
      setNodes((ns) => ns.map((n) => (n.id === selectedID ? { ...n, data: { ...n.data, ...patch } } : n)));
      setDirty(true);
    },
    [selectedID, setNodes]
  );

  const removeSelected = useCallback(() => {
    if (!selectedID) return;
    setNodes((ns) => ns.filter((n) => n.id !== selectedID));
    setEdges((es) => es.filter((e) => e.source !== selectedID && e.target !== selectedID));
    setSelectedID(null);
    setDirty(true);
  }, [selectedID, setNodes, setEdges]);

  const onSave = useCallback(async () => {
    if (!flow) return;
    setSaving(true);
    setError('');
    try {
      const f = await updateFlow(flow.id, { name: flow.name, description: flow.description, graph: fromCanvas(nodes, edges) });
      setFlow(f);
      setDirty(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }, [flow, nodes, edges]);

  const applyRunToCanvas = useCallback(
    (rnodes: FlowRunNode[]) => {
      const byID = new Map(rnodes.map((n) => [n.node_id, n.status]));
      setNodes((ns) => ns.map((n) => ({ ...n, data: { ...n.data, runStatus: byID.get(n.id) } })));
    },
    [setNodes]
  );

  const pollRun = useCallback(
    (runID: string) => {
      if (pollRef.current) window.clearInterval(pollRef.current);
      const tick = async () => {
        try {
          const r = await getFlowRun(runID);
          setActiveRun(r);
          applyRunToCanvas(r.nodes);
          if (r.run.status !== 'running' && r.run.status !== 'pending' && pollRef.current) {
            window.clearInterval(pollRef.current);
            pollRef.current = null;
          }
        } catch {
          /* transient poll errors ignored */
        }
      };
      void tick();
      pollRef.current = window.setInterval(() => void tick(), 1500);
    },
    [applyRunToCanvas]
  );

  const onRun = useCallback(async () => {
    if (!flow) return;
    setError('');
    if (dirty) await onSave();
    try {
      const run = await runFlow(flow.id);
      setShowRuns(true);
      pollRun(run.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [flow, dirty, onSave, pollRun]);

  const loadRuns = useCallback(async () => {
    try {
      const r = await listFlowRuns(flowID);
      setRuns(r.items ?? []);
    } catch {
      /* list errors non-fatal */
    }
  }, [flowID]);

  useEffect(() => {
    if (showRuns) void loadRuns();
  }, [showRuns, loadRuns]);

  if (!flow) {
    return (
      <main className="anim-fade flex flex-1 items-center justify-center text-[13px] text-zinc-500">
        {error || tr('加载中…', 'Loading…')}
      </main>
    );
  }

  return (
    <main className="anim-fade flex min-w-0 flex-1 flex-col overflow-hidden">
      {/* toolbar */}
      <div className="flex items-center gap-3 border-b border-zinc-800 px-4 py-2">
        <button
          type="button"
          onClick={() => navigate('/workflows')}
          className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
        >
          <ArrowLeft size={16} />
        </button>
        <input
          value={flow.name}
          disabled={!canWrite}
          onChange={(e) => {
            setFlow({ ...flow, name: e.target.value });
            setDirty(true);
          }}
          className="w-64 rounded-md border border-transparent bg-transparent px-2 py-1 text-[14px] font-medium text-zinc-100 outline-none focus:border-zinc-700"
        />
        <span className="text-[11px] text-zinc-600">v{flow.version}</span>
        {dirty && <span className="text-[11px] text-amber-500">{tr('未保存', 'Unsaved')}</span>}
        <div className="flex-1" />
        {error && <span className="max-w-md truncate text-[12px] text-red-400">{error}</span>}
        <button
          type="button"
          onClick={() => setShowRuns((v) => !v)}
          className={`inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-[12px] transition-colors ${
            showRuns ? 'bg-zinc-800 text-zinc-200' : 'text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200'
          }`}
        >
          <History size={14} />
          {tr('运行记录', 'Runs')}
        </button>
        {canWrite && (
          <>
            <button
              type="button"
              onClick={() => void onSave()}
              disabled={saving || !dirty}
              className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 px-2.5 py-1.5 text-[12px] text-zinc-300 transition-colors hover:bg-zinc-800 disabled:opacity-40"
            >
              <Save size={14} />
              {tr('保存', 'Save')}
            </button>
            <button
              type="button"
              onClick={() => void onRun()}
              className="inline-flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-[12px] font-medium text-white transition-colors hover:bg-indigo-500"
            >
              <Play size={14} />
              {tr('运行', 'Run')}
            </button>
          </>
        )}
      </div>

      <div className="flex min-h-0 flex-1">
        {/* palette */}
        {canWrite && (
          <div className="w-44 shrink-0 space-y-1 overflow-y-auto border-r border-zinc-800 p-2">
            <div className="px-1 pb-1 text-[11px] uppercase tracking-wide text-zinc-600">{tr('节点', 'Nodes')}</div>
            {(Object.keys(NODE_META) as FlowNodeType[]).map((t) => {
              const meta = NODE_META[t];
              const Icon = meta.icon;
              return (
                <button
                  key={t}
                  type="button"
                  onClick={() => addNode(t)}
                  className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[12px] text-zinc-300 transition-colors hover:bg-zinc-800"
                >
                  <Icon size={14} className={meta.color} />
                  {locale === 'zh-CN' ? meta.zh : meta.en}
                </button>
              );
            })}
            <div className="px-1 pt-3 text-[11px] leading-relaxed text-zinc-600">
              {tr(
                '连线 = 控制流。数据用 {{nodes.<id>.output.<字段>}} 引用上游。',
                'Edges are control flow. Reference upstream data via {{nodes.<id>.output.<field>}}.'
              )}
            </div>
          </div>
        )}

        {/* canvas */}
        <div className="min-w-0 flex-1">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={(c) => {
              onNodesChange(c);
              if (c.some((x) => x.type === 'position' && x.dragging === false)) setDirty(true);
            }}
            onEdgesChange={(c) => {
              onEdgesChange(c);
              if (c.some((x) => x.type === 'remove')) setDirty(true);
            }}
            onConnect={onConnect}
            onNodeClick={(_, n) => setSelectedID(n.id)}
            onPaneClick={() => setSelectedID(null)}
            nodesDraggable={canWrite}
            nodesConnectable={canWrite}
            elementsSelectable
            deleteKeyCode={canWrite ? ['Backspace', 'Delete'] : []}
            fitView
            proOptions={{ hideAttribution: true }}
          >
            <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
            <Controls showInteractive={false} />
          </ReactFlow>
        </div>

        {/* config drawer */}
        {selected && (
          <div className="w-80 shrink-0 overflow-y-auto border-l border-zinc-800 p-3">
            <div className="mb-2 flex items-center justify-between">
              <div className="text-[12px] font-medium uppercase tracking-wide text-zinc-500">{selected.data.flowType}</div>
              {canWrite && (
                <button
                  type="button"
                  onClick={removeSelected}
                  className="rounded-md p-1 text-zinc-500 hover:bg-zinc-800 hover:text-red-400"
                >
                  <Trash2 size={14} />
                </button>
              )}
            </div>
            <label className="mb-3 block">
              <span className="mb-1 block text-[12px] text-zinc-500">{tr('名称', 'Name')}</span>
              <input
                value={selected.data.label}
                disabled={!canWrite}
                onChange={(e) => patchSelected({ label: e.target.value })}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-[13px] text-zinc-200 outline-none focus:border-indigo-500"
              />
            </label>
            {CONFIG_FIELDS[selected.data.flowType].map((f) => (
              <ConfigField
                key={f.key}
                spec={f}
                value={selected.data.config[f.key]}
                disabled={!canWrite}
                onChange={(v) => patchSelected({ config: { ...selected.data.config, [f.key]: v } })}
              />
            ))}
            <div className="mt-2 rounded-md bg-zinc-900/60 p-2 text-[11px] leading-relaxed text-zinc-500">
              {selected.data.flowType === 'agent'
                ? tr(
                    '不声明输出 schema 时，answer 是自由文本——只能接 Agent / 通知节点；要接条件 / 工具，必须声明 schema 并引用 output.structured.*。',
                    'Without an output schema the answer is free text — consumable only by agent / notify nodes. To feed condition / tool nodes, declare a schema and reference output.structured.*.'
                  )
                : tr('节点 id 用于数据引用：', 'Node id for data refs: ') + `{{nodes.${selected.id}.output.…}}`}
            </div>
          </div>
        )}

        {/* runs drawer */}
        {showRuns && !selected && (
          <div className="w-80 shrink-0 overflow-y-auto border-l border-zinc-800 p-3">
            <div className="mb-2 text-[12px] font-medium uppercase tracking-wide text-zinc-500">{tr('运行记录', 'Runs')}</div>
            {runs.length === 0 && !activeRun ? (
              <div className="text-[12px] text-zinc-600">{tr('暂无运行', 'No runs yet')}</div>
            ) : (
              <div className="space-y-1.5">
                {(activeRun && !runs.some((r) => r.id === activeRun.run.id) ? [activeRun.run, ...runs] : runs).map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    onClick={() => pollRun(r.id)}
                    className={`flex w-full items-center justify-between rounded-md border px-2.5 py-2 text-left text-[12px] transition-colors ${
                      activeRun?.run.id === r.id ? 'border-indigo-700 bg-indigo-950/30' : 'border-zinc-800 hover:border-zinc-700'
                    }`}
                  >
                    <span className="font-mono text-zinc-400">{r.id.slice(0, 8)}</span>
                    <RunStatusChip status={r.status} />
                  </button>
                ))}
              </div>
            )}
            {activeRun && (
              <div className="mt-3 space-y-1.5">
                <div className="text-[12px] font-medium text-zinc-400">{tr('节点明细', 'Node detail')}</div>
                {activeRun.nodes.map((n) => (
                  <div key={`${n.node_id}`} className="rounded-md border border-zinc-800 p-2">
                    <div className="flex items-center justify-between">
                      <span className="text-[12px] text-zinc-300">{n.node_name || n.node_id}</span>
                      <RunStatusChip status={n.status} />
                    </div>
                    {n.error && <div className="mt-1 break-all text-[11px] text-red-400">{n.error}</div>}
                    <details className="mt-1">
                      <summary className="cursor-pointer text-[11px] text-zinc-600">{tr('输入 / 输出', 'Input / output')}</summary>
                      <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-1.5 text-[10px] text-zinc-500">
                        {JSON.stringify({ input: n.input, output: n.output }, null, 1)}
                      </pre>
                    </details>
                  </div>
                ))}
                {activeRun.run.error && (
                  <div className="rounded-md border border-red-900/50 bg-red-950/30 p-2 text-[11px] text-red-400">{activeRun.run.error}</div>
                )}
              </div>
            )}
          </div>
        )}
      </div>
    </main>
  );
}

function RunStatusChip({ status }: { status: string }) {
  const cls =
    status === 'succeeded'
      ? 'text-emerald-400'
      : status === 'failed'
        ? 'text-red-400'
        : status === 'running'
          ? 'text-indigo-400'
          : 'text-zinc-500';
  return <span className={`text-[11px] ${cls}`}>{status}</span>;
}

function ConfigField({
  spec,
  value,
  disabled,
  onChange,
}: {
  spec: FieldSpec;
  value: unknown;
  disabled: boolean;
  onChange: (v: unknown) => void;
}) {
  const { tr, locale } = useI18n();
  const label = locale === 'zh-CN' ? spec.zh : spec.en;
  const [jsonText, setJsonText] = useState(() =>
    spec.kind === 'json' ? (value === undefined ? '' : JSON.stringify(value, null, 2)) : ''
  );
  const [jsonErr, setJsonErr] = useState(false);

  if (spec.kind === 'json') {
    return (
      <label className="mb-3 block">
        <span className="mb-1 block text-[12px] text-zinc-500">{label}</span>
        <textarea
          value={jsonText}
          disabled={disabled}
          rows={4}
          placeholder={spec.placeholder}
          onChange={(e) => {
            const t = e.target.value;
            setJsonText(t);
            if (!t.trim()) {
              setJsonErr(false);
              onChange(undefined);
              return;
            }
            try {
              onChange(JSON.parse(t));
              setJsonErr(false);
            } catch {
              setJsonErr(true);
            }
          }}
          className={`w-full rounded-md border bg-zinc-950 px-2 py-1.5 font-mono text-[12px] text-zinc-200 outline-none focus:border-indigo-500 ${
            jsonErr ? 'border-red-700' : 'border-zinc-800'
          }`}
        />
        {jsonErr && <span className="text-[11px] text-red-400">{tr('JSON 无效（未保存到节点）', 'Invalid JSON (not applied)')}</span>}
      </label>
    );
  }
  const common =
    'w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-[13px] text-zinc-200 outline-none focus:border-indigo-500';
  return (
    <label className="mb-3 block">
      <span className="mb-1 block text-[12px] text-zinc-500">{label}</span>
      {spec.kind === 'textarea' ? (
        <textarea
          value={(value as string) ?? ''}
          disabled={disabled}
          rows={4}
          placeholder={spec.placeholder}
          onChange={(e) => onChange(e.target.value)}
          className={common}
        />
      ) : (
        <input
          value={(value as string) ?? ''}
          disabled={disabled}
          placeholder={spec.placeholder}
          onChange={(e) => onChange(e.target.value)}
          className={common}
        />
      )}
    </label>
  );
}
