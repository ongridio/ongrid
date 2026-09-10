// TopologyGraph — react-flow visualisation for nodes +
// relations. Dagre lays out the graph left-to-right; node fill color
// is keyed on Node.type, edge stroke color on RelationType.semantics_tag
// (so a "hard_dep" edge always reads the same regardless of which
// custom relation type carries that tag — matches the AIOps dispatch
// rule).
import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Background,
  BackgroundVariant,
  BaseEdge,
  Controls,
  Edge,
  EdgeProps,
  getSmoothStepPath,
  Handle,
  MiniMap,
  Node,
  NodeChange,
  NodeProps,
  Position,
  ReactFlow,
  useNodes,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import dagre from '@dagrejs/dagre';

import type {
  RelationType,
  TopologyNode,
  TopologyRelation,
} from '@/api/topology';
import { useThemeMode } from '@/store/mode';
import { facingSides, routeAroundNodes } from './route';
import { alignResourceColumns } from './layout';

const NODE_WIDTH = 160;
const NODE_HEIGHT = 44;
const HIDDEN_HANDLE_STYLE = { visibility: 'hidden' as const };

// Per-node-type fill / border colors. Falls back to neutral zinc for
// any user-defined type not in this table.
type NodeColor = { bg: string; border: string; fg: string };
const NODE_COLORS_DARK: Record<string, NodeColor> = {
  device: { bg: '#1e293b', border: '#475569', fg: '#e4e4e7' },
  network_device: { bg: '#164e63', border: '#22d3ee', fg: '#ecfeff' },
  service: { bg: '#312e81', border: '#6366f1', fg: '#e4e4e7' },
  cluster: { bg: '#064e3b', border: '#10b981', fg: '#e4e4e7' },
  app: { bg: '#7c2d12', border: '#fb923c', fg: '#e4e4e7' },
  rack: { bg: '#3f3f46', border: '#a1a1aa', fg: '#e4e4e7' },
};
const NODE_COLORS_LIGHT: Record<string, NodeColor> = {
  device: { bg: '#e2e8f0', border: '#94a3b8', fg: '#0f172a' },  // slate-200/400/900
  network_device: { bg: '#cffafe', border: '#0891b2', fg: '#164e63' }, // cyan-100/600/900
  service: { bg: '#e0e7ff', border: '#6366f1', fg: '#1e1b4b' }, // indigo-100/500/950
  cluster: { bg: '#d1fae5', border: '#10b981', fg: '#064e3b' }, // emerald-100/500/900
  app: { bg: '#ffedd5', border: '#fb923c', fg: '#7c2d12' },    // orange-100/400/900
  rack: { bg: '#e4e4e7', border: '#a1a1aa', fg: '#27272a' },   // zinc-200/400/800
};
const NODE_COLORS_FALLBACK_DARK: NodeColor = { bg: '#1f2937', border: '#3f3f46', fg: '#e4e4e7' };
const NODE_COLORS_FALLBACK_LIGHT: NodeColor = { bg: '#f1f5f9', border: '#cbd5e1', fg: '#0f172a' };

// Brighter palette for the MiniMap dots — card border colors are
// tuned for legibility AT card size; once shrunk to the minimap they
// lose contrast against the mask. Same colors for both themes since
// the minimap background is always dark per our index.css override.
const MINIMAP_NODE_COLORS: Record<string, string> = {
  device: '#60a5fa',  // blue-400
  network_device: '#22d3ee', // cyan-400
  service: '#818cf8', // indigo-400
  cluster: '#34d399', // emerald-400
  app: '#fb923c',     // orange-400
  rack: '#d4d4d8',    // zinc-300
};

// Per-semantics-tag edge color. AIOps walks edges by semantics_tag,
// so the UI uses the same dimension — operators immediately see which
// edges participate in failure propagation (the hot ones).
const EDGE_COLORS: Record<string, string> = {
  hard_dep: '#f87171',     // red — most critical
  runtime_dep: '#fb923c',  // orange
  traffic: '#fbbf24',      // amber
  redundancy: '#34d399',   // green
  observation: '#60a5fa',  // blue
  aggregation: '#a78bfa',  // violet
  annotation: '#6b7280',   // gray — never propagates
};

// Per-semantics-tag edge style. Containment / structural relations
// (member_of, deployed_on) render as DASHED lines so the eye reads
// them as "scaffolding". Dependency relations (depends_on, routes_to)
// stay SOLID — those are the arrows that matter for failure flow.
// Observation / annotation use a fainter dot pattern.
const EDGE_DASH: Record<string, string | undefined> = {
  hard_dep: undefined,           // solid
  traffic: undefined,            // solid
  runtime_dep: '6 3',            // dashed (containment of process on host)
  aggregation: '5 4',            // dashed (member_of structural)
  redundancy: '8 3 2 3',         // dash-dot (replica pairs)
  observation: '2 4',            // dotted (side-channel)
  annotation: '2 4',             // dotted
};

function semanticsForType(relTypes: RelationType[], typeName: string): string {
  const rt = relTypes.find((t) => t.name === typeName);
  return rt?.semantics_tag ?? 'annotation';
}

function isNetworkDevice(node: TopologyNode | undefined): boolean {
  return node?.type === 'device' && node.props?.device_kind === 'network';
}

function visualNodeType(node: TopologyNode | undefined): string {
  return isNetworkDevice(node) ? 'network_device' : (node?.type ?? '');
}

// CustomTopologyNode renders one node tile inside react-flow. Colors
// come pre-resolved from the data payload (set up in layoutGraph
// based on theme) so the node doesn't need its own theme-mode hook.
function CustomTopologyNode(props: NodeProps) {
  const data = props.data as {
    label: string;
    type: string;
    selected: boolean;
    colors: NodeColor;
    selectionRing: string;
  };
  const colors = data.colors;
  return (
    <div
      style={{
        width: NODE_WIDTH,
        height: NODE_HEIGHT,
        background: colors.bg,
        border: `1.5px solid ${data.selected ? data.selectionRing : colors.border}`,
        borderRadius: 8,
        padding: '6px 10px',
        boxShadow: data.selected ? `0 0 0 2px ${data.selectionRing}55` : undefined,
        color: colors.fg,
        fontSize: 11,
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'center',
        overflow: 'hidden',
      }}
    >
      {Object.values(Position).flatMap((position) => (['source', 'target'] as const).map((type) => (
        <Handle key={`${type}-${position}`} id={`${type}-${position}`} type={type} position={position} style={HIDDEN_HANDLE_STYLE} />
      )))}
      <div
        style={{
          fontWeight: 500,
          fontSize: 12,
          whiteSpace: 'nowrap',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
        }}
      >
        {data.label}
      </div>
      <div style={{ fontSize: 10, opacity: 0.6, fontFamily: 'monospace' }}>{data.type}</div>
    </div>
  );
}

const nodeTypes = { topo: CustomTopologyNode };

function AvoidingEdge(props: EdgeProps) {
  const nodes = useNodes();
  const start = { x: props.sourceX, y: props.sourceY };
  const end = { x: props.targetX, y: props.targetY };
  const offset = (point: { x: number; y: number }, side: Position) => ({
    x: point.x + (side === Position.Left ? -24 : side === Position.Right ? 24 : 0),
    y: point.y + (side === Position.Top ? -24 : side === Position.Bottom ? 24 : 0),
  });
  const route = routeAroundNodes(
    offset(start, props.sourcePosition),
    offset(end, props.targetPosition),
    nodes.map((n) => ({ ...n.position, width: n.width ?? NODE_WIDTH, height: n.height ?? NODE_HEIGHT })),
  );
  let [path, labelX, labelY] = getSmoothStepPath(props);
  if (route) {
    const points = [start, ...route, end];
    path = points.map((p, i) => `${i ? 'L' : 'M'} ${p.x} ${p.y}`).join(' ');
    const segments = points.slice(1).map((p, i) => ({ a: points[i], b: p, length: Math.abs(p.x - points[i].x) + Math.abs(p.y - points[i].y) }));
    const longest = segments.reduce((a, b) => a.length >= b.length ? a : b);
    labelX = (longest.a.x + longest.b.x) / 2;
    labelY = (longest.a.y + longest.b.y) / 2;
  }
  return <BaseEdge id={props.id} path={path} style={props.style} markerEnd={props.markerEnd} markerStart={props.markerStart}
    label={props.label} labelX={labelX} labelY={labelY} labelStyle={props.labelStyle}
    labelShowBg={props.labelShowBg} labelBgStyle={props.labelBgStyle}
    labelBgPadding={props.labelBgPadding} labelBgBorderRadius={props.labelBgBorderRadius} />;
}

const edgeTypes = { avoiding: AvoidingEdge };

type Props = {
  nodes: TopologyNode[];
  relations: TopologyRelation[];
  relationTypes: RelationType[];
  selectedID: number | null;
  /** When true, drop nodes with no inbound or outbound relations from
   *  the graph. Recommended default for clean views — fresh tenants
   *  with lots of un-related devices otherwise see a vertical wall of
   *  orphan cards crowding the layout. */
  hideOrphans?: boolean;
  /** Set of relation type names to render. When omitted, all are
   *  visible. The graph tab passes this from a checklist so operators
   *  can hide "noise" types (annotation / observation) and focus on
   *  the failure-propagation set (depends_on / routes_to). */
  visibleRelationTypes?: Set<string>;
  onSelect(node: TopologyNode): void;
};

export function TopologyGraph({
  nodes,
  relations,
  relationTypes,
  selectedID,
  hideOrphans,
  visibleRelationTypes,
  onSelect,
}: Props) {
  const { resolved } = useThemeMode();
  const isLight = resolved === 'light';
  const { rfNodes: layoutNodes, rfEdges: layoutEdges } = useMemo(
    () =>
      layoutGraph(
        nodes,
        relations,
        relationTypes,
        selectedID,
        hideOrphans ?? false,
        visibleRelationTypes,
        isLight,
      ),
    [nodes, relations, relationTypes, selectedID, hideOrphans, visibleRelationTypes, isLight],
  );
  // React Flow is controlled here because the graph is rebuilt from the
  // server-side topology data. Keep drag positions separately so a node does
  // not snap back on every render or when another node is selected.
  const [draggedPositions, setDraggedPositions] = useState<Record<string, { x: number; y: number }>>({});
  const rfNodes = useMemo(
    () => layoutNodes.map((node) => ({
      ...node,
      position: draggedPositions[node.id] ?? node.position,
    })),
    [layoutNodes, draggedPositions],
  );
  const rfEdges = useMemo(() => {
    const positions = new Map(rfNodes.map((node) => [node.id, node.position]));
    return layoutEdges.map((edge) => {
      const [source, target] = facingSides(positions.get(edge.source)!, positions.get(edge.target)!);
      return { ...edge, sourceHandle: `source-${source}`, targetHandle: `target-${target}` };
    });
  }, [rfNodes, layoutEdges]);
  const onNodesChange = useCallback((changes: NodeChange[]) => {
    setDraggedPositions((previous) => {
      const next = { ...previous };
      for (const change of changes) {
        if (change.type === 'position' && change.position) {
          next[change.id] = change.position;
        }
        if (change.type === 'remove') {
          delete next[change.id];
        }
      }
      return next;
    });
  }, []);

  // Cheap effect: force a window-resize event after first paint so
  // react-flow recomputes its container bounds. Without it the canvas
  // sometimes mounts at 0×0 inside a flex parent that hasn't laid out
  // its children yet.
  useEffect(() => {
    const t = setTimeout(() => window.dispatchEvent(new Event('resize')), 50);
    return () => clearTimeout(t);
  }, []);

  return (
    <div style={{ width: '100%', height: '100%' }}>
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodesChange={onNodesChange}
        nodesDraggable
        nodesConnectable={false}
        elementsSelectable
        proOptions={{ hideAttribution: true }}
        fitView
        fitViewOptions={{ padding: 0.2, maxZoom: 1.2 }}
        minZoom={0.2}
        maxZoom={2.5}
        onNodeClick={(_, n) => {
          const id = Number(n.id);
          const orig = nodes.find((x) => x.id === id);
          if (orig) onSelect(orig);
        }}
        style={{ background: isLight ? '#fafafa' : '#09090b' }}
      >
        <Background
          variant={BackgroundVariant.Dots}
          color={isLight ? '#d4d4d8' : '#27272a'}
          gap={20}
        />
        <MiniMap
          // Background + mask styled via index.css (.react-flow__minimap*)
          // so dark-mode overrides survive react-flow upgrades. Inline
          // props here only carry the per-node fill / stroke logic.
          nodeColor={(n) => MINIMAP_NODE_COLORS[(n.data as { type: string }).type] ?? '#a1a1aa'}
          nodeStrokeColor="rgba(0,0,0,0.4)"
          nodeStrokeWidth={2}
          nodeBorderRadius={3}
          pannable
          zoomable
        />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}

// layoutGraph runs dagre to assign positions then builds the react-flow
// node + edge arrays. Pure function — no react state or DOM access.
//
function layoutGraph(
  nodes: TopologyNode[],
  relations: TopologyRelation[],
  relationTypes: RelationType[],
  selectedID: number | null,
  hideOrphans: boolean,
  visibleRelationTypes?: Set<string>,
  isLight: boolean = false,
): { rfNodes: Node[]; rfEdges: Edge[] } {
  const colorTable = isLight ? NODE_COLORS_LIGHT : NODE_COLORS_DARK;
  const colorFallback = isLight ? NODE_COLORS_FALLBACK_LIGHT : NODE_COLORS_FALLBACK_DARK;
  const selectionRing = isLight ? '#0f172a' : '#ffffff';
  // Filter relations by the visibility set first — a hidden relation
  // shouldn't make its endpoints "referenced" for orphan logic.
  const includedRelations = relations.filter(
    (r) => !visibleRelationTypes || visibleRelationTypes.has(r.type),
  );

  const allIDs = new Set<number>();
  for (const n of nodes) allIDs.add(n.id);

  const referenced = new Set<number>();
  for (const r of includedRelations) {
    if (allIDs.has(r.src_id) && allIDs.has(r.dst_id)) {
      referenced.add(r.src_id);
      referenced.add(r.dst_id);
    }
  }
  const visibleNodes = hideOrphans
    ? nodes.filter((n) => referenced.has(n.id))
    : nodes;
  const visibleNodeIDs = new Set(visibleNodes.map((n) => n.id));

  // Dagre keeps related nodes on nearby rows; resource columns are fixed below.
  const g = new dagre.graphlib.Graph();
  g.setDefaultEdgeLabel(() => ({}));
  g.setGraph({
    rankdir: 'LR',
    nodesep: 80,
    ranksep: 110,
    marginx: 40,
    marginy: 40,
  });

  for (const n of visibleNodes) {
    g.setNode(String(n.id), { width: NODE_WIDTH, height: NODE_HEIGHT });
  }
  for (const r of includedRelations) {
    if (!visibleNodeIDs.has(r.src_id) || !visibleNodeIDs.has(r.dst_id)) continue;
    g.setEdge(String(r.src_id), String(r.dst_id));
  }
  dagre.layout(g);

  const positions = alignResourceColumns(visibleNodes.map((n) => {
    const pos = g.node(String(n.id));
    return { id: String(n.id), type: visualNodeType(n), x: pos.x - NODE_WIDTH / 2, y: pos.y - NODE_HEIGHT / 2 };
  }), NODE_WIDTH, NODE_HEIGHT);
  const rfNodes: Node[] = visibleNodes.map((n) => {
    return {
      id: String(n.id),
      type: 'topo',
      position: positions.get(String(n.id))!,
      // Explicit width/height — the MiniMap reads these to draw the
      // proxy rect for each node. Without them it can't paint anything
      // before the DOM measure pass lands, which on a slow first load
      // makes the minimap look empty even though the canvas is full.
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
      data: {
        label: n.name,
        type: visualNodeType(n),
        selected: selectedID === n.id,
        // Pre-resolve colors here so CustomTopologyNode stays a pure
        // presentational component (no hook needed inside the
        // ReactFlow render tree).
        colors: colorTable[visualNodeType(n)] ?? colorFallback,
        selectionRing,
      },
    };
  });

  // Label dedup: when multiple relations exist between the same pair
  // (e.g. order-api -> mysql-prod with both depends_on AND member_of),
  // react-flow stacks both labels at the midpoint and the text becomes
  // unreadable ("depends_onds_on"). We render the label only on the
  // FIRST edge of each pair; the others draw the line + arrow + dash
  // pattern without a text label. Operator still sees the visual style
  // distinction; the dropper drawer shows the full relation list per
  // node for the labels.
  const seenPairs = new Set<string>();
  const rfEdges: Edge[] = includedRelations
    .filter((r) => visibleNodeIDs.has(r.src_id) && visibleNodeIDs.has(r.dst_id))
    .map((r) => {
      const tag = semanticsForType(relationTypes, r.type);
      const stroke = EDGE_COLORS[tag] ?? '#52525b';
      const dash = EDGE_DASH[tag];
      const isSel = selectedID === r.src_id || selectedID === r.dst_id;
      const pairKey = `${r.src_id}->${r.dst_id}`;
      const showLabel = !seenPairs.has(pairKey);
      seenPairs.add(pairKey);
      return {
        id: `rel-${r.id}`,
        source: String(r.src_id),
        target: String(r.dst_id),
        type: 'avoiding',
        animated: false,
        label: showLabel ? r.type : undefined,
        labelStyle: { fill: stroke, fontSize: 10, fontFamily: 'monospace' },
        labelBgStyle: { fill: isLight ? '#fafafa' : '#09090b', fillOpacity: 0.9 },
        labelBgPadding: [4, 2] as [number, number],
        labelBgBorderRadius: 3,
        labelShowBg: true,
        style: {
          stroke,
          strokeWidth: isSel ? 2.5 : 1.4,
          strokeDasharray: dash,
          opacity: selectedID && !isSel ? 0.25 : 0.85,
        },
        markerEnd: { type: 'arrowclosed', color: stroke, width: 16, height: 16 } as Edge['markerEnd'],
      };
    });

  return { rfNodes, rfEdges };
}
