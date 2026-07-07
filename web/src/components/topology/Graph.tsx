// TopologyGraph — react-flow visualisation for nodes +
// relations. Dagre lays out the graph left-to-right; node fill color
// is keyed on Node.type, edge stroke color on RelationType.semantics_tag
// (so a "hard_dep" edge always reads the same regardless of which
// custom relation type carries that tag — matches the AIOps dispatch
// rule).
import { useEffect, useMemo } from 'react';
import {
  Background,
  BackgroundVariant,
  Controls,
  Edge,
  Handle,
  MiniMap,
  Node,
  NodeProps,
  Position,
  ReactFlow,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import dagre from '@dagrejs/dagre';

import type {
  RelationType,
  TopologyNode,
  TopologyRelation,
} from '@/api/topology';
import { useThemeMode } from '@/store/mode';

const NODE_WIDTH = 160;
const NODE_HEIGHT = 44;
const HANDLE_TARGET_TOP = 'target-top';
const HANDLE_TARGET_BOTTOM = 'target-bottom';
const HANDLE_SOURCE_TOP = 'source-top';
const HANDLE_SOURCE_BOTTOM = 'source-bottom';
const HIDDEN_HANDLE_STYLE = { visibility: 'hidden' as const };

// Per-node-type fill / border colors. Falls back to neutral zinc for
// any user-defined type not in this table.
type NodeColor = { bg: string; border: string; fg: string };
const NODE_COLORS_DARK: Record<string, NodeColor> = {
  device: { bg: '#1e293b', border: '#475569', fg: '#e4e4e7' },
  service: { bg: '#312e81', border: '#6366f1', fg: '#e4e4e7' },
  cluster: { bg: '#064e3b', border: '#10b981', fg: '#e4e4e7' },
  app: { bg: '#7c2d12', border: '#fb923c', fg: '#e4e4e7' },
  rack: { bg: '#3f3f46', border: '#a1a1aa', fg: '#e4e4e7' },
};
const NODE_COLORS_LIGHT: Record<string, NodeColor> = {
  device: { bg: '#e2e8f0', border: '#94a3b8', fg: '#0f172a' },  // slate-200/400/900
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

// Hierarchical tier per node type. Lower number = higher in the
// vertical stack (business intent at top, raw infrastructure at
// bottom). Used to bucket nodes onto fixed horizontal bands so the
// graph reads as a layer diagram instead of a tangled mesh.
const TYPE_TIER: Record<string, number> = {
  app: 0,        // 业务系统（顶层）
  service: 1,    // 微服务
  cluster: 2,    // 集群（有状态组件）
  device: 3,     // 主机
  rack: 4,       // 物理位置（底层）
};
const TIER_BAND_HEIGHT = NODE_HEIGHT + 140;
const NODE_X_SPACING = NODE_WIDTH + 80;

function semanticsForType(relTypes: RelationType[], typeName: string): string {
  const rt = relTypes.find((t) => t.name === typeName);
  return rt?.semantics_tag ?? 'annotation';
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
      {/* The graph is tiered, but relation direction is semantic rather
          than always top->bottom (e.g. device member_of cluster points
          upward). Expose hidden handles on both vertical sides so each
          edge can pick the side that faces its target tier. */}
      <Handle id={HANDLE_TARGET_TOP} type="target" position={Position.Top} style={HIDDEN_HANDLE_STYLE} />
      <Handle id={HANDLE_SOURCE_TOP} type="source" position={Position.Top} style={HIDDEN_HANDLE_STYLE} />
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
      <Handle id={HANDLE_TARGET_BOTTOM} type="target" position={Position.Bottom} style={HIDDEN_HANDLE_STYLE} />
      <Handle id={HANDLE_SOURCE_BOTTOM} type="source" position={Position.Bottom} style={HIDDEN_HANDLE_STYLE} />
    </div>
  );
}

const nodeTypes = { topo: CustomTopologyNode };

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
  const { rfNodes, rfEdges } = useMemo(
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
// Tuning notes (2026-05-17 — was crowded on first dogfood):
//   - nodesep 30 → 60: same-rank nodes (e.g. orphan devices in column 0)
//     were touching; bumped so edge labels never overlap node borders.
//   - ranksep 80 → 180: gave parallel `member_of` + `depends_on` edges
//     room to route around each other instead of dog-piling labels at
//     mid-segment.
//   - hideOrphans drops nodes without any relations entirely so a fresh
//     tenant with 50 unrelated devices isn't dominated by a wall of
//     disconnected cards. The chip filter above is the operator's
//     escape hatch to see them.
//   - Parallel edges (two relations between the same pair) get a small
//     curvature offset per-index so their labels separate; otherwise
//     smoothstep stacks them at the same midpoint.
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

  // ----- Dagre TB pass -----
  // Run a top-to-bottom dagre layout to get X positions (which
  // minimise edge crossings within rows) — then we OVERRIDE the Y
  // positions to snap each node onto its type-based tier band.
  // This gives a hierarchical "layer diagram" feel where app sits at
  // the top, devices at the bottom, dependencies as colored arrows.
  const g = new dagre.graphlib.Graph();
  g.setDefaultEdgeLabel(() => ({}));
  g.setGraph({
    rankdir: 'TB',
    nodesep: 80,
    ranksep: 60,
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

  // ----- Tier snap + X spread -----
  // Group visible nodes by their tier, then within each tier sort by
  // dagre's chosen X (preserves crossing-minimisation order) and lay
  // them out on an even spacing grid. Snapping Y to dagre's value made
  // siblings overlap because dagre's X-spacing assumes the original Y
  // — once we override Y the X-collision avoidance breaks. Explicit
  // spread fixes it.
  const byTier = new Map<number, TopologyNode[]>();
  for (const n of visibleNodes) {
    const tier = TYPE_TIER[n.type] ?? 99;
    const bucket = byTier.get(tier) ?? [];
    bucket.push(n);
    byTier.set(tier, bucket);
  }
  // Stable sort within each tier by dagre x; widest tier sets the
  // overall canvas width so narrower tiers can centre under it.
  const tierLayout = new Map<number, { y: number; xs: number[] }>();
  const sortedTiers = [...byTier.keys()].sort((a, b) => a - b);
  let maxRowWidth = 0;
  for (const tier of sortedTiers) {
    const bucket = byTier.get(tier)!;
    bucket.sort((a, b) => (g.node(String(a.id))?.x ?? 0) - (g.node(String(b.id))?.x ?? 0));
    const rowWidth = bucket.length * NODE_X_SPACING;
    if (rowWidth > maxRowWidth) maxRowWidth = rowWidth;
  }
  sortedTiers.forEach((tier, tierIdx) => {
    const bucket = byTier.get(tier)!;
    const rowWidth = bucket.length * NODE_X_SPACING;
    const startX = 40 + (maxRowWidth - rowWidth) / 2;
    const xs = bucket.map((_, i) => startX + i * NODE_X_SPACING);
    tierLayout.set(tier, { y: 40 + tierIdx * TIER_BAND_HEIGHT, xs });
  });
  const positionFor = new Map<number, { x: number; y: number }>();
  for (const tier of sortedTiers) {
    const bucket = byTier.get(tier)!;
    const { y, xs } = tierLayout.get(tier)!;
    bucket.forEach((n, i) => positionFor.set(n.id, { x: xs[i], y }));
  }

  const rfNodes: Node[] = visibleNodes.map((n) => {
    const pos = positionFor.get(n.id) ?? { x: 0, y: 0 };
    return {
      id: String(n.id),
      type: 'topo',
      position: pos,
      // Explicit width/height — the MiniMap reads these to draw the
      // proxy rect for each node. Without them it can't paint anything
      // before the DOM measure pass lands, which on a slow first load
      // makes the minimap look empty even though the canvas is full.
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
      data: {
        label: n.name,
        type: n.type,
        selected: selectedID === n.id,
        // Pre-resolve colors here so CustomTopologyNode stays a pure
        // presentational component (no hook needed inside the
        // ReactFlow render tree).
        colors: colorTable[n.type] ?? colorFallback,
        selectionRing,
      },
    };
  });

  // Edges. For TB tier layout with Top/Bottom handles, smoothstep gives
  // clean orthogonal lines. Pick handles per edge direction: semantic
  // relations can point upward (device -> cluster member_of), and those
  // must leave the source from the top instead of looping below the card.
  //
  // Label dedup: when multiple relations exist between the same pair
  // (e.g. order-api -> mysql-prod with both depends_on AND member_of),
  // react-flow stacks both labels at the midpoint and the text becomes
  // unreadable ("depends_onds_on"). We render the label only on the
  // FIRST edge of each pair; the others draw the line + arrow + dash
  // pattern without a text label. Operator still sees the visual style
  // distinction; the dropper drawer shows the full relation list per
  // node for the labels.
  const seenPairs = new Set<string>();
  const nodeByID = new Map(visibleNodes.map((n) => [n.id, n]));
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
      const srcTier = TYPE_TIER[nodeByID.get(r.src_id)?.type ?? ''] ?? 99;
      const dstTier = TYPE_TIER[nodeByID.get(r.dst_id)?.type ?? ''] ?? 99;
      const pointsUp = srcTier > dstTier;
      return {
        id: `rel-${r.id}`,
        source: String(r.src_id),
        target: String(r.dst_id),
        sourceHandle: pointsUp ? HANDLE_SOURCE_TOP : HANDLE_SOURCE_BOTTOM,
        targetHandle: pointsUp ? HANDLE_TARGET_BOTTOM : HANDLE_TARGET_TOP,
        type: 'smoothstep',
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
