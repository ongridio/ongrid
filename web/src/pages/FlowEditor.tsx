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
  Clock,
  GitBranch,
  History,
  Siren,
  Sparkles,
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
  listFlowTools,
  runFlow,
  updateFlow,
  type Flow,
  type FlowToolMeta,
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
  'trigger.alert_fired': { icon: Siren, color: 'text-rose-400', zh: '告警触发', en: 'On alert' },
  'trigger.cron': { icon: Clock, color: 'text-amber-400', zh: '定时触发', en: 'On schedule' },
  agent: { icon: Bot, color: 'text-indigo-400', zh: 'Agent（自主）', en: 'Agent' },
  llm: { icon: Sparkles, color: 'text-violet-400', zh: 'LLM（单次）', en: 'LLM' },
  tool: { icon: Wrench, color: 'text-sky-400', zh: '工具', en: 'Tool' },
  condition: { icon: GitBranch, color: 'text-amber-400', zh: '条件', en: 'Condition' },
  notify: { icon: Bell, color: 'text-rose-400', zh: '通知', en: 'Notify' },
  set: { icon: Variable, color: 'text-zinc-400', zh: '变量', en: 'Set var' },
};

// Core nodes the user hand-places. `tool` is excluded here — tool nodes
// come from the searchable catalog (every registered BaseTool), added via
// addNode('tool', {config:{tool}}).
const BASE_NODE_TYPES: FlowNodeType[] = ['trigger.manual', 'trigger.alert_fired', 'trigger.cron', 'agent', 'llm', 'condition', 'notify', 'set'];

const CATEGORY_ORDER = ['observability', 'host', 'topology', 'incident', 'sre', 'knowledge', 'control', 'other'];
const CATEGORY_LABEL: Record<string, { zh: string; en: string }> = {
  observability: { zh: '可观测（指标/日志/链路）', en: 'Observability' },
  host: { zh: '主机直达', en: 'Host' },
  topology: { zh: '拓扑', en: 'Topology' },
  incident: { zh: '告警 / 事件', en: 'Incidents' },
  sre: { zh: '集群 / SRE', en: 'Fleet / SRE' },
  knowledge: { zh: '知识 / 读码', en: 'Knowledge' },
  control: { zh: '控制面', en: 'Control' },
  other: { zh: '其他', en: 'Other' },
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
  const ring = `${statusRing(data.runStatus)} ${selected ? 'ring-1 ring-indigo-500' : ''}`;
  const handleBase = '!h-1.5 !w-1.5 !min-w-0 !border-0';

  // Condition node: a labelled two-way switch. Header row + two output
  // rows (真 / 假) each with its own color-matched source handle, so the
  // branch a downstream edge leaves from is unambiguous.
  if (isCondition) {
    return (
      <div className={`min-w-[120px] max-w-[220px] rounded-md border bg-zinc-900 text-left ${ring}`}>
        <Handle type="target" position={Position.Left} className={`${handleBase} !bg-zinc-500`} style={{ top: 16 }} />
        <div className="flex items-center gap-1.5 px-2 py-1">
          <Icon size={12} className="shrink-0 text-amber-400" />
          <span className="truncate text-[11px] font-medium text-zinc-200">{data.label}</span>
        </div>
        <div className="border-t border-zinc-800">
          <div className="relative flex items-center justify-end px-2 py-0.5 text-[9px] font-medium text-emerald-400">
            真 · true
            <Handle id="true" type="source" position={Position.Right} className={`${handleBase} !bg-emerald-500`} />
          </div>
          <div className="relative flex items-center justify-end border-t border-zinc-800/60 px-2 py-0.5 text-[9px] font-medium text-zinc-500">
            假 · false
            <Handle id="false" type="source" position={Position.Right} className={`${handleBase} !bg-zinc-500`} />
          </div>
        </div>
        <Handle id="error" type="source" position={Position.Bottom} className={`${handleBase} !bg-red-500/80`} />
      </div>
    );
  }

  return (
    <div className={`flex min-w-[96px] max-w-[200px] items-center gap-1.5 rounded-md border bg-zinc-900 px-2 py-1 text-left transition-shadow ${ring}`}>
      {!isTrigger && <Handle type="target" position={Position.Left} className={`${handleBase} !bg-zinc-500`} />}
      <Icon size={12} className={`shrink-0 ${meta?.color ?? 'text-zinc-400'}`} />
      <span className="truncate text-[11px] font-medium text-zinc-200">{data.label}</span>
      <Handle id="next" type="source" position={Position.Right} className={`${handleBase} !bg-indigo-500`} />
      {!isTrigger && (
        <Handle id="error" type="source" position={Position.Bottom} className={`${handleBase} !bg-red-500/80`} />
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
  'trigger.alert_fired': [
    { key: 'rule', zh: '规则名包含（留空=所有告警）', en: 'Rule name contains (blank = all alerts)', kind: 'text', placeholder: '如 disk / cpu' },
    { key: 'min_severity', zh: '最低严重度（warning/error/critical，留空=不限）', en: 'Min severity (warning/error/critical; blank = any)', kind: 'text', placeholder: 'critical' },
  ],
  'trigger.cron': [
    { key: 'cron', zh: '定时表达式（标准 5 段 cron，UTC）', en: 'Cron schedule (standard 5-field, UTC)', kind: 'text', placeholder: '0 8 * * *  (每天 UTC 08:00)' },
  ],
  agent: [
    { key: 'persona', zh: '角色 (persona)', en: 'Persona', kind: 'text', placeholder: 'default / specialist-network / …' },
    { key: 'instruction', zh: '指令（支持 {{…}} 模板）', en: 'Instruction ({{…}} templates)', kind: 'textarea', placeholder: '诊断 {{trigger.host}} 上的磁盘告警…' },
    { key: 'output_schema', zh: '输出 schema（可选，JSON Schema。声明后下游才能引用 structured 字段）', en: 'Output schema (optional; required for structured downstream refs)', kind: 'json' },
  ],
  llm: [
    { key: 'system', zh: '系统提示（可选）', en: 'System prompt (optional)', kind: 'textarea', placeholder: '你是运维助手，简洁回答。' },
    { key: 'prompt', zh: '提示词（支持 {{…}} 模板）', en: 'Prompt ({{…}} templates)', kind: 'textarea', placeholder: '把这段诊断总结成一句话：{{nodes.diag.output.answer}}' },
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
  const [lastRunNodes, setLastRunNodes] = useState<FlowRunNode[]>([]);
  const [copied, setCopied] = useState('');
  const seq = useRef(1);
  const pollRef = useRef<number | null>(null);
  const [tools, setTools] = useState<FlowToolMeta[]>([]);
  const [toolQuery, setToolQuery] = useState('');

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

  useEffect(() => {
    let alive = true;
    // Pull the most recent run's node outputs so the config drawer can show
    // "what each upstream node actually output" for {{...}} reference help —
    // even before the user opens the runs drawer.
    (async () => {
      try {
        const list = await listFlowRuns(flowID, 1);
        const recent = list.items?.[0];
        if (recent && alive) {
          const full = await getFlowRun(recent.id);
          if (alive) setLastRunNodes(full.nodes ?? []);
        }
      } catch {
        /* best-effort */
      }
    })();
    return () => {
      alive = false;
    };
  }, [flowID]);

  useEffect(() => {
    let alive = true;
    listFlowTools()
      .then((r) => {
        if (alive) setTools(r.items ?? []);
      })
      .catch(() => {
        /* tools palette is best-effort; canvas works without it */
      });
    return () => {
      alive = false;
    };
  }, []);

  const addNode = useCallback(
    (t: FlowNodeType, opts?: { label?: string; config?: Record<string, unknown> }) => {
      const meta = NODE_META[t];
      const nid = `n${seq.current++}`;
      setNodes((ns) => [
        ...ns,
        {
          id: nid,
          type: 'flowNode',
          position: { x: 120 + ns.length * 40, y: 120 + ns.length * 30 },
          data: {
            flowType: t,
            label: opts?.label ?? (locale === 'zh-CN' ? meta.zh : meta.en),
            config: opts?.config ?? {},
          },
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
          <div className="flex w-52 shrink-0 flex-col overflow-hidden border-r border-zinc-800">
            <div className="space-y-1 p-2">
              <div className="px-1 pb-1 text-[11px] uppercase tracking-wide text-zinc-600">{tr('基础节点', 'Core nodes')}</div>
              {BASE_NODE_TYPES.map((t) => {
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
            </div>
            <ToolPalette
              tools={tools}
              query={toolQuery}
              onQuery={setToolQuery}
              onPick={(t) =>
                addNode('tool', {
                  label: locale === 'zh-CN' ? t.display_zh || t.name : t.name,
                  config: { tool: t.name, args: {} },
                })
              }
            />
            <div className="border-t border-zinc-800 px-3 py-2 text-[11px] leading-relaxed text-zinc-600">
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
            onNodeDragStop={(_, n) => setSelectedID(n.id)}
            onPaneClick={() => setSelectedID(null)}
            onSelectionChange={({ nodes: sel }) => { if (sel.length === 1) setSelectedID(sel[0].id); }}
            nodesDraggable={canWrite}
            nodesConnectable={canWrite}
            elementsSelectable
            deleteKeyCode={canWrite ? ['Backspace', 'Delete'] : []}
            fitView
            fitViewOptions={{ maxZoom: 1, padding: 0.3 }}
            defaultEdgeOptions={{ type: 'smoothstep' }}
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
            {selected.data.flowType === 'tool' ? (
              <ToolArgsForm
                toolName={(selected.data.config.tool as string) || ''}
                args={(selected.data.config.args as Record<string, unknown>) || {}}
                schema={tools.find((t) => t.name === selected.data.config.tool)}
                disabled={!canWrite}
                onChange={(args) => patchSelected({ config: { ...selected.data.config, args } })}
              />
            ) : (
              CONFIG_FIELDS[selected.data.flowType].map((f) => (
                <ConfigField
                  key={f.key}
                  spec={f}
                  value={selected.data.config[f.key]}
                  disabled={!canWrite}
                  onChange={(v) => patchSelected({ config: { ...selected.data.config, [f.key]: v } })}
                />
              ))
            )}
            {!selected.data.flowType.startsWith('trigger.') && (
              <UpstreamRefs
                selectedId={selected.id}
                nodes={nodes}
                edges={edges}
                runNodes={activeRun?.nodes?.length ? activeRun.nodes : lastRunNodes}
                onCopy={(ref) => {
                  void navigator.clipboard?.writeText(ref);
                  setCopied(ref);
                  window.setTimeout(() => setCopied(''), 1500);
                }}
                copied={copied}
              />
            )}
            <div className="mt-2 rounded-md bg-zinc-900/60 p-2 text-[11px] leading-relaxed text-zinc-500">
              {selected.data.flowType === 'agent' || selected.data.flowType === 'llm'
                ? tr(
                    '不声明输出 schema 时，answer 是自由文本——只能接 Agent / LLM / 通知节点；要接条件 / 工具，必须声明 schema 并引用 output.structured.*。',
                    'Without an output schema the answer is free text — consumable only by agent / LLM / notify nodes. To feed condition / tool nodes, declare a schema and reference output.structured.*.'
                  )
                : tr('本节点输出引用：', 'This node output ref: ') + `{{nodes.${selected.id}.output.…}}`}
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


// ToolPalette — searchable, category-grouped list of every registered
// BaseTool. Picking one drops a tool node pre-filled with that tool name.
function ToolPalette({
  tools,
  query,
  onQuery,
  onPick,
}: {
  tools: FlowToolMeta[];
  query: string;
  onQuery: (q: string) => void;
  onPick: (t: FlowToolMeta) => void;
}) {
  const { tr, locale } = useI18n();
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return tools;
    return tools.filter((t) => t.name.toLowerCase().includes(q) || t.description.toLowerCase().includes(q));
  }, [tools, query]);
  const byCat = useMemo(() => {
    const m = new Map<string, FlowToolMeta[]>();
    for (const t of filtered) {
      const c = t.category || 'other';
      if (!m.has(c)) m.set(c, []);
      m.get(c)!.push(t);
    }
    return m;
  }, [filtered]);
  const cats = CATEGORY_ORDER.filter((c) => byCat.has(c));

  return (
    <div className="flex min-h-0 flex-1 flex-col border-t border-zinc-800">
      <div className="flex items-center justify-between px-3 pt-2">
        <span className="text-[11px] uppercase tracking-wide text-zinc-600">
          {tr('工具', 'Tools')} {tools.length > 0 ? `(${tools.length})` : ''}
        </span>
      </div>
      <div className="px-2 py-1.5">
        <input
          value={query}
          onChange={(e) => onQuery(e.target.value)}
          placeholder={tr('搜索工具…', 'Search tools…')}
          className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-[12px] text-zinc-200 outline-none focus:border-indigo-500"
        />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-1 pb-2">
        {tools.length === 0 ? (
          <div className="px-2 py-3 text-[11px] leading-relaxed text-zinc-600">
            {tr('工具目录为空（LLM 运行时未就绪）。', 'Tool catalog empty (LLM runtime not ready).')}
          </div>
        ) : cats.length === 0 ? (
          <div className="px-2 py-3 text-[11px] text-zinc-600">{tr('无匹配', 'No match')}</div>
        ) : (
          cats.map((cat) => (
            <div key={cat} className="mb-1">
              <div className="px-2 pt-2 text-[10px] uppercase tracking-wide text-zinc-600">
                {locale === 'zh-CN' ? CATEGORY_LABEL[cat]?.zh : CATEGORY_LABEL[cat]?.en}
              </div>
              {byCat.get(cat)!.map((t) => (
                <button
                  key={t.name}
                  type="button"
                  title={t.description + (t.when_to_use ? '\n\n' + t.when_to_use : '')}
                  onClick={() => onPick(t)}
                  className="flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-left text-[12px] text-zinc-300 transition-colors hover:bg-zinc-800"
                >
                  <Wrench size={12} className="shrink-0 text-sky-400/80" />
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate text-[12px] text-zinc-200">
                      {locale === 'zh-CN' ? t.display_zh || t.name : t.name}
                    </span>
                    {locale === 'zh-CN' && t.display_zh ? (
                      <span className="truncate font-mono text-[9px] text-zinc-600">{t.name}</span>
                    ) : null}
                  </span>
                  {t.class !== 'read' && (
                    <span className="ml-auto shrink-0 rounded bg-amber-900/40 px-1 text-[9px] text-amber-400">
                      {t.class === 'destructive' ? tr('危', 'D') : tr('写', 'W')}
                    </span>
                  )}
                </button>
              ))}
            </div>
          ))
        )}
      </div>
    </div>
  );
}

// ToolArgsForm — renders a tool node's args as a typed form driven by the
// tool's JSON Schema. Falls back to a raw JSON textarea when the schema
// is unknown (catalog not loaded / custom tool). Values accept {{...}}
// templates, so every field stays a string in config.args.
function ToolArgsForm({
  toolName,
  args,
  schema,
  disabled,
  onChange,
}: {
  toolName: string;
  args: Record<string, unknown>;
  schema?: FlowToolMeta;
  disabled: boolean;
  onChange: (args: Record<string, unknown>) => void;
}) {
  const { tr } = useI18n();
  const props = schema?.parameters?.properties;
  const required = new Set(schema?.parameters?.required ?? []);

  if (!props || Object.keys(props).length === 0) {
    // Unknown schema → raw JSON editor.
    return (
      <ConfigField
        spec={{ key: 'args', zh: '参数（JSON，值支持 {{…}}）', en: 'Args (JSON; values accept {{…}})', kind: 'json' }}
        value={args}
        disabled={disabled}
        onChange={(v) => onChange((v as Record<string, unknown>) ?? {})}
      />
    );
  }

  // setArg coerces the raw input into the type the tool expects and omits
  // the key entirely when blank (so optional params truly stay unset). A
  // {{…}} value is always kept as a string — it's resolved at run time.
  const setArg = (key: string, raw: string, type?: string) => {
    const next = { ...args };
    const t = raw.trim();
    if (t === '') {
      delete next[key];
    } else if (t.startsWith('{{')) {
      next[key] = raw; // template — resolved at run time, stays a string
    } else if (type === 'array' || type === 'object') {
      try {
        next[key] = JSON.parse(t);
      } catch {
        next[key] = raw; // let the user keep typing; tool will validate
      }
    } else if (type === 'number' || type === 'integer') {
      const n = Number(t);
      next[key] = Number.isNaN(n) ? raw : n;
    } else {
      next[key] = raw;
    }
    onChange(next);
  };

  // display turns a stored arg value back into the input's text form.
  const display = (v: unknown): string => {
    if (v === undefined || v === null) return '';
    if (typeof v === 'string') return v;
    if (Array.isArray(v) || typeof v === 'object') return JSON.stringify(v);
    return String(v);
  };

  return (
    <div>
      <div className="mb-2 rounded-md bg-zinc-900/60 p-2 text-[11px] leading-relaxed text-zinc-500">
        {schema?.description}
        <div className="mt-1 text-zinc-600">
          {tr(
            '可选参数留空即可。数组填 [1, 2]，布尔选 true/false，数字直接填；任意字段也可用 {{…}} 引用上游。',
            'Leave optional params blank. Arrays as [1, 2], booleans true/false, numbers plain; any field also accepts a {{…}} upstream ref.'
          )}
        </div>
      </div>
      {Object.entries(props).map(([key, spec]) => {
        const type = (spec as { type?: string }).type;
        const stored = args[key];
        const val = display(stored);
        const isEnum = Array.isArray(spec.enum) && spec.enum.length > 0;
        const isBool = type === 'boolean';
        const isTemplate = typeof stored === 'string' && stored.trim().startsWith('{{');
        const typeBadge =
          type === 'array' ? tr('数组', 'array')
          : isBool ? tr('布尔', 'bool')
          : type === 'number' || type === 'integer' ? tr('数字', 'number')
          : '';
        const ph =
          type === 'array' ? '[1, 2]  或  {{…}}'
          : type === 'number' || type === 'integer' ? `123  ${tr('或', 'or')}  {{…}}`
          : '{{…}}';
        return (
          <label key={key} className="mb-3 block">
            <span className="mb-1 block text-[12px] text-zinc-500">
              <span className="font-mono text-zinc-400">{key}</span>
              {required.has(key) ? (
                <span className="ml-1 text-red-400">*</span>
              ) : (
                <span className="ml-1 text-[10px] text-zinc-600">{tr('可选', 'optional')}</span>
              )}
              {typeBadge && <span className="ml-1 rounded bg-zinc-800 px-1 text-[9px] text-zinc-500">{typeBadge}</span>}
              {spec.description ? <span className="ml-1 text-zinc-600">— {spec.description}</span> : null}
            </span>
            {isEnum ? (
              <select
                value={val}
                disabled={disabled}
                onChange={(e) => setArg(key, e.target.value, type)}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-[13px] text-zinc-200 outline-none focus:border-indigo-500"
              >
                <option value="">{tr('（不设置）', '(unset)')}</option>
                {(spec.enum as unknown[]).map((o) => (
                  <option key={String(o)} value={String(o)}>
                    {String(o)}
                  </option>
                ))}
              </select>
            ) : isBool && !isTemplate ? (
              <select
                value={val}
                disabled={disabled}
                onChange={(e) => {
                  const next = { ...args };
                  if (e.target.value === '') delete next[key];
                  else next[key] = e.target.value === 'true';
                  onChange(next);
                }}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-[13px] text-zinc-200 outline-none focus:border-indigo-500"
              >
                <option value="">{tr('（不设置）', '(unset)')}</option>
                <option value="true">true</option>
                <option value="false">false</option>
              </select>
            ) : (
              <input
                value={val}
                disabled={disabled}
                placeholder={ph}
                onChange={(e) => setArg(key, e.target.value, type)}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-[12px] text-zinc-200 outline-none focus:border-indigo-500"
              />
            )}
          </label>
        );
      })}
      <div className="mt-1 text-[11px] text-zinc-600">{tr('工具', 'Tool')}: <span className="font-mono">{toolName}</span></div>
    </div>
  );
}

// ---------- upstream reference helper -------------------------------------

// upstreamOf returns the set of node ids reachable backward from targetId
// (every node that runs before it), so the ref panel only offers data that
// actually exists at this point in the flow.
function upstreamOf(targetId: string, edges: Edge[]): Set<string> {
  const incoming = new Map<string, string[]>();
  for (const e of edges as { source: string; target: string }[]) {
    if (!incoming.has(e.target)) incoming.set(e.target, []);
    incoming.get(e.target)!.push(e.source);
  }
  const seen = new Set<string>();
  const stack = [...(incoming.get(targetId) ?? [])];
  while (stack.length) {
    const id = stack.pop()!;
    if (seen.has(id)) continue;
    seen.add(id);
    for (const p of incoming.get(id) ?? []) stack.push(p);
  }
  return seen;
}

// flattenPaths walks a decoded JSON value into dotted leaf paths, using
// [0] for arrays (the engine's expr resolver understands the subscript).
// Capped in breadth + depth so a huge tool result can't explode the panel.
function flattenPaths(v: unknown, prefix = '', out: string[] = [], depth = 0): string[] {
  if (out.length >= 40 || depth > 5) return out;
  if (Array.isArray(v)) {
    if (v.length) flattenPaths(v[0], `${prefix}[0]`, out, depth + 1);
    else if (prefix) out.push(prefix);
  } else if (v && typeof v === 'object') {
    for (const k of Object.keys(v as Record<string, unknown>)) {
      flattenPaths((v as Record<string, unknown>)[k], prefix ? `${prefix}.${k}` : k, out, depth + 1);
    }
  } else if (prefix) {
    out.push(prefix);
  }
  return out;
}

// staticOutputHints is the fallback when a node hasn't run yet — the known
// output shape per node type, so the user still sees what to reference.
function staticOutputHints(flowType: FlowNodeType, hasSchema: boolean): string[] {
  switch (flowType) {
    case 'tool':
      return ['result'];
    case 'agent':
    case 'llm':
      return hasSchema ? ['answer', 'structured'] : ['answer'];
    case 'condition':
      return ['result'];
    case 'set':
      return ['name', 'value'];
    case 'trigger.alert_fired':
      return ['incident_id', 'rule', 'severity', 'edge_id', 'device_id', 'labels', 'fired_at'];
    case 'trigger.cron':
      return ['fired_at', 'cron'];
    default:
      return [];
  }
}

function UpstreamRefs({
  selectedId,
  nodes,
  edges,
  runNodes,
  onCopy,
  copied,
}: {
  selectedId: string;
  nodes: CanvasNode[];
  edges: Edge[];
  runNodes: FlowRunNode[];
  onCopy: (ref: string) => void;
  copied: string;
}) {
  const { tr } = useI18n();
  const ups = useMemo(() => {
    const set = upstreamOf(selectedId, edges);
    const runByID = new Map(runNodes.map((r) => [r.node_id, r]));
    return nodes
      .filter((n) => set.has(n.id))
      .map((n) => {
        const ran = runByID.get(n.id);
        let paths: string[];
        let live = false;
        if (ran && ran.output && Object.keys(ran.output).length) {
          paths = flattenPaths(ran.output);
          live = true;
        } else {
          const hasSchema = !!(n.data.config?.output_schema);
          paths = staticOutputHints(n.data.flowType, hasSchema);
        }
        return { id: n.id, label: n.data.label, type: n.data.flowType, paths, live };
      })
      .filter((u) => u.paths.length > 0);
  }, [selectedId, nodes, edges, runNodes]);

  if (ups.length === 0) {
    return (
      <div className="mt-2 rounded-md border border-zinc-800 bg-zinc-900/40 p-2 text-[11px] text-zinc-600">
        {tr('无上游节点。把触发器 / 其它节点连到本节点后，这里会列出可引用的数据。', 'No upstream nodes. Wire a trigger / other node into this one to see referenceable data here.')}
      </div>
    );
  }

  return (
    <div className="mt-2 rounded-md border border-zinc-800 bg-zinc-900/40 p-2">
      <div className="mb-1 flex items-center justify-between">
        <span className="text-[11px] font-medium text-zinc-400">{tr('可引用的上游数据', 'Upstream data refs')}</span>
        {copied ? <span className="text-[10px] text-emerald-400">{tr('已复制', 'copied')}</span> : null}
      </div>
      <div className="mb-1.5 text-[10px] leading-relaxed text-zinc-600">
        {tr('点字段复制 {{…}} 引用，粘贴到上面的输入框。', 'Click a field to copy its {{…}} ref, paste into a field above.')}
      </div>
      <div className="space-y-1.5">
        {ups.map((u) => (
          <div key={u.id}>
            <div className="flex items-center gap-1 text-[10px] text-zinc-500">
              <span className="font-medium text-zinc-400">{u.label}</span>
              <span className="font-mono text-zinc-600">{u.id}</span>
              {u.live ? (
                <span className="rounded bg-emerald-900/40 px-1 text-[8px] text-emerald-400">{tr('实测', 'live')}</span>
              ) : (
                <span className="rounded bg-zinc-800 px-1 text-[8px] text-zinc-500">{tr('预估', 'shape')}</span>
              )}
            </div>
            <div className="mt-0.5 flex flex-wrap gap-1">
              {u.paths.map((p) => {
                const ref = `{{nodes.${u.id}.output.${p}}}`;
                return (
                  <button
                    key={p}
                    type="button"
                    title={ref}
                    onClick={() => onCopy(ref)}
                    className={`max-w-full truncate rounded border px-1 py-0.5 font-mono text-[9px] transition-colors ${
                      copied === ref
                        ? 'border-emerald-700 bg-emerald-950/40 text-emerald-400'
                        : 'border-zinc-800 bg-zinc-950 text-zinc-400 hover:border-zinc-700 hover:text-zinc-200'
                    }`}
                  >
                    {p}
                  </button>
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
