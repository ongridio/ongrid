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
  ReactFlowInstance,
  useEdgesState,
  useNodesState,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import {
  ArrowLeft,
  Bell,
  Bot,
  ChevronDown,
  ChevronRight,
  CircleDot,
  Clock,
  Globe,
  GitBranch,
  History,
  Shuffle,
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
  listNodeTypes,
  runFlow,
  testFlowNode,
  updateFlow,
  type Flow,
  type FlowToolMeta,
  type NodeType,
  type FlowGraph,
  type FlowGraphNode,
  type FlowNodeType,
  type FlowRun,
  type FlowRunNode,
} from '@/api/flows';
import { useI18n } from '@/i18n/locale';
import { useAuth } from '@/store/auth';
import { toolGroupKey, groupTag, groupTitle, orderedGroupKeys } from '@/lib/toolSkill';
import { paramDescEn } from '@/lib/paramDescEn';

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
  transform: { icon: Shuffle, color: 'text-teal-400', zh: '字段映射', en: 'Edit Fields' },
  http_request: { icon: Globe, color: 'text-cyan-400', zh: 'HTTP 请求', en: 'HTTP Request' },
};

// Core nodes the user hand-places. `tool` is excluded here — tool nodes
// come from the searchable catalog (every registered BaseTool), added via
// addNode('tool', {config:{tool}}).
const BASE_NODE_TYPES: FlowNodeType[] = ['trigger.manual', 'trigger.alert_fired', 'trigger.cron', 'agent', 'llm', 'condition', 'notify', 'set', 'transform', 'http_request'];


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
  const { tr } = useI18n();
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
            {tr('真', 'True')}
            <Handle id="true" type="source" position={Position.Right} className={`${handleBase} !bg-emerald-500`} />
          </div>
          <div className="relative flex items-center justify-end border-t border-zinc-800/60 px-2 py-0.5 text-[9px] font-medium text-zinc-500">
            {tr('假', 'False')}
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
  transform: [
    { key: 'fields', zh: '字段映射（JSON，每个字段值支持 {{…}}）。把上游数据重组成下游需要的字段。', en: 'Field mapping (JSON; each value accepts {{…}}). Reshape upstream data into the fields a downstream node needs.', kind: 'json' },
  ],
  http_request: [
    { key: 'method', zh: '方法（GET / POST / PUT / PATCH / DELETE）', en: 'Method (GET / POST / PUT / PATCH / DELETE)', kind: 'text', placeholder: 'GET' },
    { key: 'url', zh: 'URL（支持 {{…}}）', en: 'URL ({{…}} templates)', kind: 'text', placeholder: 'https://api.example.com/v1/{{nodes.a.output.result.id}}' },
    { key: 'headers', zh: '请求头（JSON 对象，值支持 {{…}}）', en: 'Headers (JSON object; values accept {{…}})', kind: 'json', placeholder: '{"Authorization": "Bearer {{vars.token}}"}' },
    { key: 'body', zh: '请求体（JSON / 文本，支持 {{…}}）', en: 'Body (JSON / text; {{…}} templates)', kind: 'textarea', placeholder: '{"text": "{{nodes.diag.output.answer}}"}' },
    { key: 'timeout_seconds', zh: '超时秒数（默认 30，最大 120）', en: 'Timeout seconds (default 30, max 120)', kind: 'text', placeholder: '30' },
  ],
};

// ---------- data-driven node metadata ------------------------------------
// Node label / config form / output shape come from the backend NodeSpec
// registry (GET /v1/flow-node-types). The built-in NODE_META / CONFIG_FIELDS
// tables remain only as a visual map (icon/color) and a graceful fallback
// if the node-types fetch hasn't landed.

function nodeLabelOf(type: FlowNodeType, locale: string, nodeTypes: Record<string, NodeType>): string {
  const nt = nodeTypes[type];
  if (nt) return locale === 'zh-CN' ? nt.label_zh : nt.label_en;
  const m = NODE_META[type];
  return m ? (locale === 'zh-CN' ? m.zh : m.en) : type;
}

function configFieldsFor(type: FlowNodeType, nodeTypes: Record<string, NodeType>): FieldSpec[] {
  const nt = nodeTypes[type];
  if (nt && nt.config_fields?.length) {
    return nt.config_fields.map((f) => ({
      key: f.key,
      zh: f.label_zh,
      en: f.label_en,
      kind: f.kind === 'select' ? 'text' : f.kind,
      placeholder: f.placeholder,
    }));
  }
  return CONFIG_FIELDS[type] ?? [];
}

// baseNodeTypesFrom derives the hand-placed node palette from the backend
// (every registered type except `tool`, which has its own catalog),
// grouped by kind. Falls back to the static BASE_NODE_TYPES list.
function baseNodeTypesFrom(nodeTypes: Record<string, NodeType>): FlowNodeType[] {
  const all = Object.values(nodeTypes).filter((nt) => nt.type !== 'tool');
  if (all.length === 0) return BASE_NODE_TYPES;
  const kindOrder = ['trigger', 'ai', 'action', 'control', 'flow', 'data'];
  return all
    .slice()
    .sort((a, b) => {
      const ka = kindOrder.indexOf(a.category);
      const kb = kindOrder.indexOf(b.category);
      if (ka !== kb) return (ka < 0 ? 99 : ka) - (kb < 0 ? 99 : kb);
      return a.type.localeCompare(b.type);
    })
    .map((nt) => nt.type);
}

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
  const [testOut, setTestOut] = useState<Record<string, unknown>>({});
  const [testing, setTesting] = useState(false);
  const [testErr, setTestErr] = useState('');
  const [runInputText, setRunInputText] = useState('');
  const [showRunInput, setShowRunInput] = useState(false);
  const [runInputErr, setRunInputErr] = useState('');
  const seq = useRef(1);
  const pollRef = useRef<number | null>(null);
  const rfRef = useRef<ReactFlowInstance<CanvasNode, Edge> | null>(null);
  const [tools, setTools] = useState<FlowToolMeta[]>([]);
  const [toolQuery, setToolQuery] = useState('');
  const [nodeSpecs, setNodeSpecs] = useState<Record<string, NodeType>>({});

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

  useEffect(() => {
    let alive = true;
    listNodeTypes()
      .then((r) => {
        if (!alive) return;
        const m: Record<string, NodeType> = {};
        for (const nt of r.items ?? []) m[nt.type] = nt;
        setNodeSpecs(m);
      })
      .catch(() => {
        /* falls back to the built-in NODE_META / CONFIG_FIELDS tables */
      });
    return () => {
      alive = false;
    };
  }, []);

  const addNode = useCallback(
    (t: FlowNodeType, opts?: { label?: string; config?: Record<string, unknown> }) => {
      const meta = NODE_META[t];
      const nid = `n${seq.current++}`;
      const pos = { x: 120 + nodes.length * 40, y: 120 + nodes.length * 30 };
      setNodes((ns) => [
        ...ns,
        {
          id: nid,
          type: 'flowNode',
          position: pos,
          data: {
            flowType: t,
            label: opts?.label ?? nodeLabelOf(t, locale, nodeSpecs),
            config: opts?.config ?? {},
          },
        },
      ]);
      setSelectedID(nid);
      setDirty(true);
      // Pan the canvas to the freshly added node so it's never off-screen.
      window.setTimeout(() => {
        const inst = rfRef.current;
        if (inst) inst.setCenter(pos.x + 75, pos.y + 16, { zoom: inst.getZoom(), duration: 400 });
      }, 30);
    },
    [nodes.length, locale, nodeSpecs, setNodes]
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
      // A config edit invalidates this node's cached test-run output — it no
      // longer reflects the new config, so drop it (the panel falls back to
      // the shape hint until the user re-tests).
      if ('config' in patch) {
        setTestOut((prev) => {
          if (!(selectedID in prev)) return prev;
          const next = { ...prev };
          delete next[selectedID];
          return next;
        });
      }
      setDirty(true);
    },
    [selectedID, setNodes, setTestOut]
  );

  const removeSelected = useCallback(() => {
    if (!selectedID) return;
    setNodes((ns) => ns.filter((n) => n.id !== selectedID));
    setEdges((es) => es.filter((e) => e.source !== selectedID && e.target !== selectedID));
    setSelectedID(null);
    setDirty(true);
  }, [selectedID, setNodes, setEdges]);

  const runTest = useCallback(
    async (node: CanvasNode) => {
      if (!flow) return;
      setTesting(true);
      setTestErr('');
      try {
        const r = await testFlowNode(flow.id, {
          node_type: node.data.flowType,
          config: node.data.config,
        });
        if (r.error) {
          setTestErr(r.error);
        } else {
          setTestOut((prev) => ({ ...prev, [node.id]: r.output }));
        }
      } catch (e) {
        setTestErr(e instanceof Error ? e.message : String(e));
      } finally {
        setTesting(false);
      }
    },
    [flow]
  );

  const onSave = useCallback(async (): Promise<boolean> => {
    if (!flow) return false;
    setSaving(true);
    setError('');
    try {
      const f = await updateFlow(flow.id, { name: flow.name, description: flow.description, graph: fromCanvas(nodes, edges) });
      setFlow(f);
      setDirty(false);
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return false;
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
    // Manual-trigger payload: optional JSON object the user typed, exposed
    // to nodes as {{trigger.<field>}}. Empty → {} (unchanged behaviour).
    let input: Record<string, unknown> = {};
    const txt = runInputText.trim();
    if (txt) {
      try {
        const parsed = JSON.parse(txt);
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          setRunInputErr(tr('输入必须是 JSON 对象', 'Input must be a JSON object'));
          return;
        }
        input = parsed as Record<string, unknown>;
      } catch {
        setRunInputErr(tr('输入不是合法 JSON', 'Input is not valid JSON'));
        return;
      }
    }
    setRunInputErr('');
    // Don't launch a run against a flow whose latest edits failed to save —
    // it would execute the stale server-side graph and mislead the user.
    if (dirty && !(await onSave())) return;
    try {
      const run = await runFlow(flow.id, input);
      setShowRuns(true);
      setShowRunInput(false);
      pollRun(run.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [flow, dirty, onSave, pollRun, runInputText, tr]);

  const hasManualTrigger = useMemo(() => nodes.some((n) => n.data.flowType === 'trigger.manual'), [nodes]);

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
            {hasManualTrigger && (
              <div className="relative">
                <button
                  type="button"
                  onClick={() => setShowRunInput((v) => !v)}
                  title={tr('手动触发输入（JSON，节点用 {{trigger.字段}} 引用）', 'Manual trigger input (JSON; referenced as {{trigger.<field>}})')}
                  className={`inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-[12px] transition-colors ${
                    showRunInput || runInputText.trim()
                      ? 'border-indigo-700 bg-indigo-950/30 text-indigo-300'
                      : 'border-zinc-700 text-zinc-300 hover:bg-zinc-800'
                  }`}
                >
                  <Variable size={14} />
                  {tr('输入', 'Input')}
                </button>
                {showRunInput && (
                  <div className="absolute right-0 top-full z-30 mt-1 w-72 rounded-md border border-zinc-700 bg-zinc-900 p-2 shadow-lg">
                    <div className="mb-1 text-[11px] font-medium text-zinc-300">{tr('手动触发输入（JSON）', 'Manual trigger input (JSON)')}</div>
                    <textarea
                      value={runInputText}
                      onChange={(e) => setRunInputText(e.target.value)}
                      rows={4}
                      spellCheck={false}
                      placeholder={'{"host":"vm-1"}'}
                      className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-200 outline-none focus:border-zinc-600"
                    />
                    <div className="mt-1 text-[10px] leading-relaxed text-zinc-600">
                      {tr('运行时作为触发器载荷；节点用 {{trigger.字段}} 引用。', 'Used as the trigger payload at run time; reference it with {{trigger.<field>}}.')}
                    </div>
                    {runInputErr && <div className="mt-1 text-[10px] text-red-400">{runInputErr}</div>}
                  </div>
                )}
              </div>
            )}
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
              {baseNodeTypesFrom(nodeSpecs).map((t) => {
                const meta = NODE_META[t];
                const Icon = meta?.icon ?? Wrench;
                return (
                  <button
                    key={t}
                    type="button"
                    onClick={() => addNode(t)}
                    className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[12px] text-zinc-300 transition-colors hover:bg-zinc-800"
                  >
                    <Icon size={14} className={meta.color} />
                    {nodeLabelOf(t, locale, nodeSpecs)}
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
            onInit={(inst) => {
              rfRef.current = inst;
            }}
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
              configFieldsFor(selected.data.flowType, nodeSpecs).map((f) => (
                <ConfigField
                  key={f.key}
                  spec={f}
                  value={selected.data.config[f.key]}
                  disabled={!canWrite}
                  onChange={(v) => patchSelected({ config: { ...selected.data.config, [f.key]: v } })}
                />
              ))
            )}
            <ReferencedData config={selected.data.config} nodes={nodes} />
            {!selected.data.flowType.startsWith('trigger.') && (
              <UpstreamRefs
                selectedId={selected.id}
                nodes={nodes}
                edges={edges}
                runNodes={activeRun?.nodes?.length ? activeRun.nodes : lastRunNodes}
                nodeSpecs={nodeSpecs}
                onCopy={(ref) => {
                  void navigator.clipboard?.writeText(ref);
                  setCopied(ref);
                  window.setTimeout(() => setCopied(''), 1500);
                }}
                copied={copied}
              />
            )}
            {canWrite && (selected.data.flowType === 'tool' || selected.data.flowType === 'llm' || selected.data.flowType === 'agent') && (
              <div className="mt-2">
                <button
                  type="button"
                  onClick={() => void runTest(selected)}
                  disabled={testing}
                  className="inline-flex w-full items-center justify-center gap-1.5 rounded-md border border-violet-700/60 bg-violet-950/30 px-2.5 py-1.5 text-[12px] font-medium text-violet-300 transition-colors hover:bg-violet-900/40 disabled:opacity-50"
                >
                  <Play size={13} />
                  {testing ? tr('试跑中…', 'Testing…') : tr('试跑此节点（看真实输出）', 'Test this node (see real output)')}
                </button>
                {testErr && (
                  <div title={testErr} className="mt-1 break-all rounded-md border border-red-900/50 bg-red-950/30 px-2 py-1 text-[11px] text-red-400">{friendlyFlowError(testErr, tr)}</div>
                )}
                {!testErr && testOut[selected.id] !== undefined && (
                  <div className="mt-1 rounded-md border border-violet-900/40 bg-violet-950/20 p-2">
                    <div className="mb-1 text-[11px] font-medium text-violet-300">{tr('试跑输出', 'Test output')}</div>
                    {isEmptyOutput(testOut[selected.id]) ? (
                      <div className="text-[11px] leading-relaxed text-amber-400/90">
                        {tr(
                          '节点执行成功，但没有返回数据。常见原因：参数为空，或参数引用了上游/触发器数据——单节点试跑是隔离运行的，{{nodes.*}} / {{trigger.*}} 在这里没有值。请先填好该节点自己的参数（如 device_ids），再试跑。',
                          'The node ran but returned no data. Usually that means a required arg is empty, or an arg references upstream/trigger data — single-node test-run is isolated, so {{nodes.*}} / {{trigger.*}} have no value here. Fill in this node\'s own args (e.g. device_ids) and test again.'
                        )}
                      </div>
                    ) : null}
                    <pre className="mt-1 max-h-56 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-1.5 text-[10px] leading-relaxed text-zinc-300">
                      {JSON.stringify(testOut[selected.id], null, 2)}
                    </pre>
                  </div>
                )}
              </div>
            )}
            <SelfOutputRefs
              node={selected}
              testOutput={testOut[selected.id]}
              runNodes={activeRun?.nodes?.length ? activeRun.nodes : lastRunNodes}
              nodeSpecs={nodeSpecs}
              onCopy={(ref) => {
                void navigator.clipboard?.writeText(ref);
                setCopied(ref);
                window.setTimeout(() => setCopied(''), 1500);
              }}
              copied={copied}
            />
            {(selected.data.flowType === 'agent' || selected.data.flowType === 'llm') && (
              <div className="mt-2 rounded-md bg-zinc-900/60 p-2 text-[11px] leading-relaxed text-zinc-500">
                {tr(
                  '不声明输出 schema 时，answer 是自由文本——只能接 Agent / LLM / 通知节点；要接条件 / 工具，必须声明 schema 并引用 output.structured.*。',
                  'Without an output schema the answer is free text — consumable only by agent / LLM / notify nodes. To feed condition / tool nodes, declare a schema and reference output.structured.*.'
                )}
              </div>
            )}
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
                      <div className="flex items-center gap-1.5">
                        {n.fired_port && n.fired_port !== 'next' && (
                          <span
                            className={`rounded px-1 text-[9px] font-medium ${
                              n.fired_port === 'true'
                                ? 'bg-emerald-900/40 text-emerald-400'
                                : n.fired_port === 'error'
                                  ? 'bg-red-900/40 text-red-400'
                                  : 'bg-zinc-800 text-zinc-400'
                            }`}
                            title={tr('该节点触发的输出端口', 'The output port this node fired')}
                          >
                            → {n.fired_port}
                          </span>
                        )}
                        <RunStatusChip status={n.status} />
                      </div>
                    </div>
                    {n.error && <div title={n.error} className="mt-1 break-all text-[11px] text-red-400">{friendlyFlowError(n.error, tr)}</div>}
                    <details className="mt-1">
                      <summary className="cursor-pointer text-[11px] text-zinc-600">{tr('输入 / 输出', 'Input / output')}</summary>
                      <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-1.5 text-[10px] text-zinc-500">
                        {JSON.stringify({ input: n.input, output: n.output }, null, 1)}
                      </pre>
                    </details>
                  </div>
                ))}
                {activeRun.run.error && (
                  <div title={activeRun.run.error} className="rounded-md border border-red-900/50 bg-red-950/30 p-2 text-[11px] text-red-400">{friendlyFlowError(activeRun.run.error, tr)}</div>
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
      const c = toolGroupKey(t.name);
      if (!m.has(c)) m.set(c, []);
      m.get(c)!.push(t);
    }
    return m;
  }, [filtered]);
  const cats = useMemo(() => orderedGroupKeys(byCat.keys()), [byCat]);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const toggleGroup = (k: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      next.has(k) ? next.delete(k) : next.add(k);
      return next;
    });

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
          cats.map((cat) => {
            const tag = groupTag(cat);
            const isCollapsed = collapsed.has(cat);
            return (
            <div key={cat} className="mb-1">
              <button
                type="button"
                onClick={() => toggleGroup(cat)}
                className="flex w-full items-center gap-1 px-2 pt-2 pb-0.5 text-left text-[10px] uppercase tracking-wide text-zinc-500 hover:text-zinc-300"
              >
                {isCollapsed ? <ChevronRight size={11} /> : <ChevronDown size={11} />}
                <span>{groupTitle(cat, locale === 'zh-CN')}</span>
                {tag === 'mcp' && <span className="rounded bg-sky-900/40 px-1 text-[8px] font-medium normal-case text-sky-300">mcp</span>}
                {tag === 'ext' && <span className="rounded bg-violet-900/40 px-1 text-[8px] font-medium normal-case text-violet-300">{tr('扩展', 'ext')}</span>}
                <span className="text-zinc-600">{byCat.get(cat)!.length}</span>
              </button>
              {!isCollapsed && byCat.get(cat)!.map((t) => (
                <button
                  key={t.name}
                  type="button"
                  title={(locale === 'zh-CN' ? t.description_zh || t.description : t.description) + (t.when_to_use ? '\n\n' + t.when_to_use : '')}
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
            );
          })
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
  const { tr, locale } = useI18n();
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
      // Quote bare {{…}} templates so "[{{ref}}]" parses as a real array of
      // template strings (each resolved at run time) instead of one literal
      // string. Idempotent: already-quoted templates re-quote cleanly.
      const jsonish = t.replace(/"?\{\{[^{}]*\}\}"?/g, (m) => JSON.stringify(m.replace(/^"|"$/g, '')));
      try {
        next[key] = JSON.parse(jsonish);
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
    // Render {{…}} template elements unquoted so the input round-trips to what
    // the user typed (e.g. [{{ref}}]), while non-template values stay JSON.
    if (Array.isArray(v)) {
      return '[' + v.map((el) => (typeof el === 'string' && el.trim().startsWith('{{') ? el : JSON.stringify(el))).join(', ') + ']';
    }
    if (typeof v === 'object') return JSON.stringify(v);
    return String(v);
  };

  return (
    <div>
      <div className="mb-2 flex items-center gap-1.5">
        <span className="text-[11px] text-zinc-500">{tr('工具', 'Tool')}</span>
        <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[11px] text-zinc-300">{toolName}</span>
      </div>
      <div className="mb-2 rounded-md bg-zinc-900/60 p-2 text-[11px] leading-relaxed text-zinc-500">
        {locale === 'zh-CN' ? schema?.description_zh || schema?.description : schema?.description}
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
              {(() => {
                const desc = locale === 'en-US' ? paramDescEn[toolName]?.[key] ?? spec.description : spec.description;
                return desc ? <span className="ml-1 text-zinc-600">— {desc}</span> : null;
              })()}
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

// OutputEntry is one referenceable leaf: its dotted path plus, when the
// node has actually run (live / test), the real value at that path. value
// is undefined for static "shape" hints (the node hasn't run yet).
type OutputEntry = { path: string; value?: unknown };

// flattenEntries walks a decoded JSON value into dotted leaf paths AND
// their values, using [0] for arrays (the engine's expr resolver
// understands the subscript). Capped in breadth + depth so a huge tool
// result can't explode the panel.
function flattenEntries(v: unknown, prefix = '', out: OutputEntry[] = [], depth = 0): OutputEntry[] {
  if (out.length >= 40 || depth > 5) return out;
  if (Array.isArray(v)) {
    if (v.length) flattenEntries(v[0], `${prefix}[0]`, out, depth + 1);
    else if (prefix) out.push({ path: prefix, value: [] });
  } else if (v && typeof v === 'object') {
    for (const k of Object.keys(v as Record<string, unknown>)) {
      flattenEntries((v as Record<string, unknown>)[k], prefix ? `${prefix}.${k}` : k, out, depth + 1);
    }
  } else if (prefix) {
    out.push({ path: prefix, value: v });
  }
  return out;
}

// isEmptyOutput reports whether a test output carries no useful data — an
// empty object, or a single `result` wrapper that is null / "" / {} / has an
// empty results array. Used to nudge the user toward filling in the node's
// own args (single-node test-run is isolated; upstream refs are blank).
function isEmptyOutput(v: unknown): boolean {
  if (v === null || v === undefined) return true;
  if (typeof v !== 'object') return false;
  const keys = Object.keys(v as Record<string, unknown>);
  if (keys.length === 0) return true;
  if (keys.length === 1 && keys[0] === 'result') {
    const r = (v as Record<string, unknown>).result;
    if (r === null || r === undefined) return true;
    if (typeof r === 'string') return r.trim() === '';
    if (typeof r === 'object') {
      const rk = Object.keys(r as Record<string, unknown>);
      if (rk.length === 0) return true;
      const results = (r as Record<string, unknown>).results;
      if (Array.isArray(results) && results.length === 0) return true;
    }
  }
  return false;
}

// previewValue renders a leaf value as a short single-line string for the
// ref chip ("= 42", '= "critical"', "= {…}"). Empty for null/undefined.
function previewValue(v: unknown): string {
  if (v === null || v === undefined) return '';
  let s = typeof v === 'string' ? v : JSON.stringify(v);
  if (s === undefined) return '';
  s = s.replace(/\s+/g, ' ').trim();
  return s.length > 48 ? `${s.slice(0, 48)}…` : s;
}

// --- friendly references (HLD-016 step 1) -------------------------------
// Storage stays canonical ({{nodes.<id>.output.<path>}}); these helpers are
// the DISPLAY/authoring layer that turns a cryptic path into a name a user
// reads at a glance ("主机负载 › CPU 使用率"). Step 2 (pills) renders over the
// same mapping. FIELD_LABELS hand-labels the common leaves; everything else
// falls back to a prettified last segment.
const FIELD_LABELS: Record<string, { zh: string; en: string }> = {
  cpu_pct: { zh: 'CPU 使用率', en: 'CPU %' },
  mem_pct: { zh: '内存使用率', en: 'Memory %' },
  disk_used_pct: { zh: '磁盘使用率', en: 'Disk %' },
  load1: { zh: '1 分钟负载', en: 'Load 1m' },
  load5: { zh: '5 分钟负载', en: 'Load 5m' },
  load15: { zh: '15 分钟负载', en: 'Load 15m' },
  sampled_at: { zh: '采样时间', en: 'Sampled at' },
  fired_at: { zh: '触发时间', en: 'Fired at' },
  device_id: { zh: '设备 ID', en: 'Device ID' },
  device_ids: { zh: '设备 ID 列表', en: 'Device IDs' },
  edge_id: { zh: 'Edge ID', en: 'Edge ID' },
  incident_id: { zh: '告警 ID', en: 'Incident ID' },
  severity: { zh: '严重度', en: 'Severity' },
  rule: { zh: '规则', en: 'Rule' },
  labels: { zh: '标签', en: 'Labels' },
  answer: { zh: '回答', en: 'Answer' },
  result: { zh: '结果', en: 'Result' },
  results: { zh: '结果', en: 'Results' },
  error: { zh: '错误', en: 'Error' },
  error_count: { zh: '失败数', en: 'Error count' },
  success_count: { zh: '成功数', en: 'Success count' },
  host_load: { zh: '主机负载', en: 'Host load' },
  process_list: { zh: '进程列表', en: 'Process list' },
  processes: { zh: '进程', en: 'Processes' },
  pid: { zh: 'PID', en: 'PID' },
  cmdline: { zh: '命令行', en: 'Command line' },
  user: { zh: '用户', en: 'User' },
  structured: { zh: '结构化输出', en: 'Structured' },
  name: { zh: '名称', en: 'Name' },
  value: { zh: '值', en: 'Value' },
  channels: { zh: '渠道数', en: 'Channels' },
  sent: { zh: '已发送', en: 'Sent' },
  cron: { zh: '定时表达式', en: 'Cron' },
};

// TOOL_OUTPUT_SHAPES curates the referenceable output paths for the common
// tools, so a tool node's "本节点输出" panel shows its real fields (cpu_pct,
// pid, …) BEFORE the user test-runs it. Tools don't declare an output schema,
// so without this the static shape is just the bare `result` wrapper. Unknown
// tools fall back to `result` until a test-run expands the live output. (The
// authoritative fix — tools declaring output fields backend-side — is step 2.)
const TOOL_OUTPUT_SHAPES: Record<string, string[]> = {
  get_host_load: [
    'result.results[0].device_id',
    'result.results[0].host_load.cpu_pct',
    'result.results[0].host_load.mem_pct',
    'result.results[0].host_load.disk_used_pct',
    'result.results[0].host_load.load1',
    'result.results[0].host_load.load5',
    'result.results[0].host_load.load15',
    'result.results[0].host_load.sampled_at',
    'result.success_count',
    'result.error_count',
  ],
  get_host_processes: [
    'result.results[0].device_id',
    'result.results[0].process_list.processes[0].pid',
    'result.results[0].process_list.processes[0].name',
    'result.results[0].process_list.processes[0].cmdline',
    'result.results[0].process_list.processes[0].cpu_pct',
    'result.results[0].process_list.processes[0].mem_pct',
    'result.results[0].process_list.processes[0].user',
    'result.results[0].process_list.sampled_at',
    'result.success_count',
    'result.error_count',
  ],
};

function prettifyLeaf(seg: string): string {
  return seg
    .replace(/\[\d+\]/g, '')
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase())
    .trim();
}

// friendlyFieldLabel turns a dotted path into a readable field name. Uses the
// LEAF segment (last non-array key) so "result.results[0].host_load.cpu_pct"
// reads as "CPU 使用率". A row index >0 is appended as "#n" to disambiguate.
function friendlyFieldLabel(path: string, locale: string): string {
  const segs = path.split('.');
  const leafRaw = segs[segs.length - 1] || path;
  const leaf = leafRaw.replace(/\[\d+\]/g, '');
  const known = FIELD_LABELS[leaf];
  let label = known ? (locale === 'zh-CN' ? known.zh : known.en) : prettifyLeaf(leaf);
  const idxMatch = path.match(/\[(\d+)\]/g);
  if (idxMatch) {
    const last = idxMatch[idxMatch.length - 1].match(/\d+/);
    if (last && Number(last[0]) > 0) label += ` #${Number(last[0]) + 1}`;
  }
  return label;
}

// friendlyFlowError turns a raw backend node error into a clear, localized
// hint when it's a recognizable shape. The classic one: a Go json type
// mismatch (e.g. a single number wired into an array field) — opaque to users.
function friendlyFlowError(raw: string, tr: (zh: string, en: string) => string): string {
  if (!raw) return raw;
  const m = raw.match(/cannot unmarshal (\w+) into Go struct field \S+\.(\w+) of type (\S+)/);
  if (m) {
    const got = m[1];
    const field = m[2];
    const want = m[3];
    if (want.startsWith('[]')) {
      return tr(
        `参数「${field}」需要数组（列表），但收到的是单个 ${got}。用 [ … ] 包一层——例如把 {{…}} 改成 [{{…}}]。`,
        `Param "${field}" expects an array, but got a single ${got}. Wrap it in [ … ] — e.g. change {{…}} to [{{…}}].`,
      );
    }
    return tr(
      `参数「${field}」的类型应为 ${want}，但收到的是 ${got}。检查该字段填的值或 {{…}} 引用。`,
      `Param "${field}" should be ${want} but got ${got}. Check the value or {{…}} ref in that field.`,
    );
  }
  return raw;
}

// friendlyRef decodes a whole {{...}} reference into "节点名 › 字段名", or null
// if it isn't a recognised reference. nodeLabels maps node id → canvas label.
function friendlyRef(ref: string, nodeLabels: Map<string, string>, locale: string): string | null {
  const m = ref.match(/^\{\{\s*([^{}]+?)\s*\}\}$/);
  if (!m) return null;
  const expr = m[1];
  let mm = expr.match(/^nodes\.([^.]+)\.output\.(.+)$/);
  if (mm) return `${nodeLabels.get(mm[1]) ?? mm[1]} › ${friendlyFieldLabel(mm[2], locale)}`;
  mm = expr.match(/^trigger\.(.+)$/);
  if (mm) return `${locale === 'zh-CN' ? '触发器' : 'Trigger'} › ${friendlyFieldLabel(mm[1], locale)}`;
  mm = expr.match(/^vars\.(.+)$/);
  if (mm) return `${locale === 'zh-CN' ? '变量' : 'Var'} › ${mm[1]}`;
  return null;
}

// collectRefs pulls every {{...}} template out of a config object's string
// leaves (deep), so the panel can show what a node references at a glance.
function collectRefs(v: unknown, out: string[] = []): string[] {
  if (typeof v === 'string') {
    const matches = v.match(/\{\{\s*[^{}]+?\s*\}\}/g);
    if (matches) for (const r of matches) if (!out.includes(r)) out.push(r);
  } else if (Array.isArray(v)) {
    for (const e of v) collectRefs(e, out);
  } else if (v && typeof v === 'object') {
    for (const e of Object.values(v as Record<string, unknown>)) collectRefs(e, out);
  }
  return out;
}

// staticOutputHints is the fallback when a node hasn't run yet — the known
// output shape per node type, so the user still sees what to reference. The
// backend NodeSpec.output_shape (specShape) is the source of truth for
// static-shape nodes; only the two genuinely DYNAMIC cases keep frontend
// logic: transform (= the user's declared field keys) and agent/llm (which
// gain a `structured` field only when an output_schema is configured).
function staticOutputHints(
  flowType: FlowNodeType,
  config: Record<string, unknown>,
  specShape?: string[]
): string[] {
  switch (flowType) {
    case 'agent':
    case 'llm':
      return config?.output_schema ? ['answer', 'structured'] : (specShape?.length ? specShape : ['answer']);
    case 'transform':
      // Output shape = the fields the user declared (dynamic).
      return Object.keys((config?.fields as Record<string, unknown>) ?? {});
    case 'tool': {
      // Curated rich shape for known tools; else the bare `result` wrapper
      // (a test-run then expands the real live output).
      const toolName = config?.tool as string | undefined;
      const curated = toolName ? TOOL_OUTPUT_SHAPES[toolName] : undefined;
      if (curated?.length) return curated;
      return specShape?.length ? specShape : ['result'];
    }
    default:
      return specShape?.length ? specShape : [];
  }
}

// shapeEntries adapts static path hints into valueless OutputEntry list.
function shapeEntries(
  flowType: FlowNodeType,
  config: Record<string, unknown>,
  specShape?: string[]
): OutputEntry[] {
  return staticOutputHints(flowType, config, specShape).map((path) => ({ path }));
}

function UpstreamRefs({
  selectedId,
  nodes,
  edges,
  runNodes,
  nodeSpecs,
  onCopy,
  copied,
}: {
  selectedId: string;
  nodes: CanvasNode[];
  edges: Edge[];
  runNodes: FlowRunNode[];
  nodeSpecs: Record<string, NodeType>;
  onCopy: (ref: string) => void;
  copied: string;
}) {
  const { tr, locale } = useI18n();
  const ups = useMemo(() => {
    const set = upstreamOf(selectedId, edges);
    const runByID = new Map(runNodes.map((r) => [r.node_id, r]));
    return nodes
      .filter((n) => set.has(n.id))
      .map((n) => {
        const ran = runByID.get(n.id);
        let entries: OutputEntry[];
        let live = false;
        if (ran && ran.output && Object.keys(ran.output).length) {
          entries = flattenEntries(ran.output);
          live = true;
        } else {
          entries = shapeEntries(n.data.flowType, n.data.config ?? {}, nodeSpecs[n.data.flowType]?.output_shape);
        }
        return { id: n.id, label: n.data.label, type: n.data.flowType, entries, live };
      })
      .filter((u) => u.entries.length > 0);
  }, [selectedId, nodes, edges, runNodes, nodeSpecs]);

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
              {u.entries.map((e) => {
                const ref = `{{nodes.${u.id}.output.${e.path}}}`;
                const label = friendlyFieldLabel(e.path, locale);
                const preview = u.live ? previewValue(e.value) : '';
                return (
                  <button
                    key={e.path}
                    type="button"
                    title={`${label}\n${e.path}\n${ref}${preview ? `\n= ${preview}` : ''}`}
                    onClick={() => onCopy(ref)}
                    className={`max-w-full truncate rounded border px-1 py-0.5 text-[10px] transition-colors ${
                      copied === ref
                        ? 'border-emerald-700 bg-emerald-950/40 text-emerald-400'
                        : 'border-zinc-800 bg-zinc-950 text-zinc-300 hover:border-zinc-700 hover:text-zinc-100'
                    }`}
                  >
                    {label}
                    {preview ? <span className="ml-1 font-mono text-[9px] text-zinc-500">= {preview}</span> : null}
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

// ReferencedData decodes the {{...}} templates inside the selected node's
// config into friendly "节点名 › 字段名" chips — so the cryptic refs the user
// pasted into config inputs are readable at a glance (step-1 friendly read-
// back; the input box itself still holds the canonical template).
function ReferencedData({ config, nodes }: { config: Record<string, unknown>; nodes: CanvasNode[] }) {
  const { tr, locale } = useI18n();
  const items = useMemo(() => {
    const labels = new Map(nodes.map((n) => [n.id, n.data.label]));
    return collectRefs(config)
      .map((ref) => ({ ref, friendly: friendlyRef(ref, labels, locale) }))
      .filter((x): x is { ref: string; friendly: string } => !!x.friendly);
  }, [config, nodes, locale]);
  if (items.length === 0) return null;
  return (
    <div className="mt-2 rounded-md border border-indigo-900/40 bg-indigo-950/15 p-2">
      <div className="mb-1 text-[11px] font-medium text-indigo-300/90">{tr('本节点引用了', 'This node references')}</div>
      <div className="flex flex-wrap gap-1">
        {items.map((it) => (
          <span
            key={it.ref}
            title={it.ref}
            className="rounded border border-indigo-900/50 bg-indigo-950/30 px-1.5 py-0.5 text-[10px] text-indigo-200"
          >
            {it.friendly}
          </span>
        ))}
      </div>
    </div>
  );
}

// SelfOutputRefs shows the SELECTED node's own output fields — what it
// emits, for downstream {{nodes.<id>.output.…}} refs and to understand a
// tool's return shape. Live paths from the last run, else the node type's
// known shape.
function SelfOutputRefs({
  node,
  testOutput,
  runNodes,
  nodeSpecs,
  onCopy,
  copied,
}: {
  node: CanvasNode;
  testOutput?: unknown;
  runNodes: FlowRunNode[];
  nodeSpecs: Record<string, NodeType>;
  onCopy: (ref: string) => void;
  copied: string;
}) {
  const { tr, locale } = useI18n();
  const { entries, source } = useMemo(() => {
    // Priority: a fresh node-level test run → the latest flow run → the
    // node type's known shape.
    if (testOutput && typeof testOutput === 'object' && Object.keys(testOutput as object).length) {
      return { entries: flattenEntries(testOutput), source: 'test' as const };
    }
    const ran = runNodes.find((r) => r.node_id === node.id);
    if (ran && ran.output && Object.keys(ran.output).length) {
      return { entries: flattenEntries(ran.output), source: 'live' as const };
    }
    return {
      entries: shapeEntries(node.data.flowType, node.data.config ?? {}, nodeSpecs[node.data.flowType]?.output_shape),
      source: 'shape' as const,
    };
  }, [node, testOutput, runNodes, nodeSpecs]);
  const hasValues = source === 'test' || source === 'live';
  const toolName = typeof node.data.config?.tool === 'string' ? (node.data.config.tool as string) : '';
  const isMcp = toolName.startsWith('mcp__');

  if (entries.length === 0) return null;

  return (
    <div className="mt-2 rounded-md border border-zinc-800 bg-zinc-900/40 p-2">
      <div className="mb-1 flex items-center justify-between">
        <span className="text-[11px] font-medium text-zinc-400">{tr('本节点输出', 'This node output')}</span>
        {copied ? (
          <span className="text-[10px] text-emerald-400">{tr('已复制', 'copied')}</span>
        ) : source === 'test' ? (
          <span className="rounded bg-violet-900/40 px-1 text-[8px] text-violet-300">{tr('试跑', 'tested')}</span>
        ) : source === 'live' ? (
          <span className="rounded bg-emerald-900/40 px-1 text-[8px] text-emerald-400">{tr('实测', 'live')}</span>
        ) : (
          <span className="rounded bg-zinc-800 px-1 text-[8px] text-zinc-500">{tr('预估', 'shape')}</span>
        )}
      </div>
      <div className="mb-1.5 text-[10px] leading-relaxed text-zinc-600">
        {tr('本节点输出的字段（友好名），供下游引用。点字段复制 {{…}}。', "This node's output fields (friendly names), for downstream refs. Click to copy {{…}}.")}
      </div>
      {isMcp && (
        <div className="mb-1.5 rounded bg-sky-900/20 px-1.5 py-1 text-[10px] leading-relaxed text-sky-300/90">
          {tr(
            'MCP 工具多返回文本：整体引用 {{…output.result}}，或接 Agent / LLM 节点解析后再用结构化字段。',
            'MCP tools often return plain text — reference it whole via {{…output.result}}, or feed it to an Agent / LLM node to parse into structured fields.',
          )}
        </div>
      )}
      <div className="flex flex-wrap gap-1">
        {entries.map((e) => {
          const ref = `{{nodes.${node.id}.output.${e.path}}}`;
          const label = friendlyFieldLabel(e.path, locale);
          const preview = hasValues ? previewValue(e.value) : '';
          return (
            <button
              key={e.path}
              type="button"
              title={`${label}\n${e.path}\n${ref}${preview ? `\n= ${preview}` : ''}`}
              onClick={() => onCopy(ref)}
              className={`max-w-full truncate rounded border px-1 py-0.5 text-[10px] transition-colors ${
                copied === ref
                  ? 'border-emerald-700 bg-emerald-950/40 text-emerald-400'
                  : 'border-zinc-800 bg-zinc-950 text-zinc-300 hover:border-zinc-700 hover:text-zinc-100'
              }`}
            >
              {label}
              {preview ? <span className="ml-1 font-mono text-[9px] text-zinc-500">= {preview}</span> : null}
            </button>
          );
        })}
      </div>
    </div>
  );
}
