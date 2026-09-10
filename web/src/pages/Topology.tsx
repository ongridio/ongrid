import { FilterField } from '@/components/ui/FilterField';
import { Input, Textarea, Label } from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/Tabs';
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/Popover';
import { Checkbox } from '@/components/ui/Checkbox';
import { Select } from '@/components/ui/Select';
// Topology page. MVP scope: nodes list + filter +
// search, click-to-detail showing the node's neighbors + props, and
// the relation-type registry where admins register custom kinds.
// Graph visualisation (react-flow) ships separately as PR-3b.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Network, Plus, RefreshCw, Search, Trash2, X } from 'lucide-react';
import { cn } from '@/lib/cn';
import { Modal } from '@/components/Modal';
import { Button, Card, EmptyState, PageHeader } from '@/components/ui';
import { TopologyGraph } from '@/components/topology/Graph';
import { ApiError } from '@/api/client';
import { useAuth } from '@/store/auth';
import { useI18n } from '@/i18n/locale';
import {
  createNode,
  createNodeType,
  createRelation,
  createRelationType,
  deleteNode,
  deleteNodeType,
  deleteRelation,
  deleteRelationType,
  getNode,
  listNodes,
  listNodeTypes,
  listRelations,
  listRelationTypes,
  localizedTypeLabel,
  RELATION_DIRECTIONS,
  SEMANTICS_TAGS,
  type NodeType,
  type RelationDirection,
  type RelationType,
  type SemanticsTag,
  type TopologyNode,
  type TopologyRelation,
} from '@/api/topology';

type Tab = 'graph' | 'nodes' | 'relation-types';

const isDomainManagedNodeType = (type: string) => type === 'device' || type === 'cluster';
const isReportedService = (node: TopologyNode) => node.type === 'service' && node.props?.source === 'apm';

export default function TopologyPage() {
  const role = useAuth((s) => s.role);
  const isAdmin = role === 'admin';
  const [tab, setTab] = useState<Tab>('graph');
  const { tr } = useI18n();

  return (
    <Tabs value={tab} onValueChange={setTab} className="contents"><main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <Network size={16} className="text-zinc-400" />
            {tr('拓扑', 'Topology')}
          </span>
        }
        subtitle={tr(
          '业务图谱：节点 / 关系 / 关系类型',
          'Business graph: nodes / relations / types',
        )}
        extra={
          <TabsList className="flex items-center gap-1.5 text-xs">
            <TabsTrigger  value={'graph'}>
              {tr('图谱', 'Graph')}
            </TabsTrigger>
            <TabsTrigger  value={'nodes'}>
              {tr('节点 + 关系', 'Nodes + relations')}
            </TabsTrigger>
            <TabsTrigger  value={'relation-types'}>
              {tr('类型管理', 'Types')}
            </TabsTrigger>
          </TabsList>
        }
      />
      <TabsContent value={tab} className="contents">{tab === 'graph' && <GraphTab isAdmin={isAdmin} />}
      {tab === 'nodes' && <NodesTab isAdmin={isAdmin} />}
      {tab === 'relation-types' && (
        <div className="flex flex-1 flex-col overflow-auto">
          <NodeTypesPanel isAdmin={isAdmin} />
          <RelationTypesTab isAdmin={isAdmin} />
        </div>
      )}</TabsContent>
    </main></Tabs>
  );
}



function FilterChip({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <Button variant="plain" size="sm"
      type="button"
      onClick={onClick}
      className={cn(
        'rounded-full border px-2 py-0.5 text-[11px] transition-colors',
        active
          // Was text-indigo-100 — invisible on the light content area
          // (HTML class=dark but the content panel paints light zinc-50
          // bg; pale indigo text on faint indigo bg = unreadable). The
          // sidebar 's chips don't hit this because their parent is
          // zinc-950. Use indigo-700 for legible contrast in both themes.
          ? 'border-indigo-500/60 bg-indigo-500/20 text-indigo-700'
          : 'border-zinc-800 bg-zinc-900/40 text-zinc-400 hover:bg-zinc-900',
      )}
    >
      {children}
    </Button>
  );
}

// buildTypeChips reads chip labels from the NodeType registry (a row
// per kind with operator-supplied display_name). 5 builtin rows are
// always in the result; custom ones registered by the operator land
// in tier order. As a defensive fallback we also surface any type
// that's PRESENT in the node set but NOT yet in the registry (legacy
// rows from before the registry shipped, or a race where a node was
// created with a type still pending registration). Those fall back
// to the raw type string as the label.
function buildTypeChips(
  nodes: TopologyNode[],
  nodeTypes: NodeType[],
  locale: 'zh-CN' | 'en-US',
): { value: string; label: string }[] {
  // Registry-known types use localizedTypeLabel so chip labels follow
  // the operator's locale (display_name_en when set, fallback to
  // display_name, then to name). Types only present in node data but
  // missing from the registry (legacy rows / race) fall through to
  // the raw type string — no locale switch can save those.
  const inRegistry: { value: string; label: string }[] = nodeTypes.map((nt) => ({
    value: nt.name,
    label: localizedTypeLabel(nt, locale),
  }));
  const registryNames = new Set(nodeTypes.map((nt) => nt.name));
  const seenExtras = new Set<string>();
  const extras: { value: string; label: string }[] = [];
  for (const n of nodes) {
    if (!n.type || registryNames.has(n.type) || seenExtras.has(n.type)) continue;
    seenExtras.add(n.type);
    extras.push({ value: n.type, label: n.type });
  }
  extras.sort((a, b) => a.label.localeCompare(b.label));
  // The leading "All" chip falls back to tr() at the call site
  // because module-level constants can't subscribe to locale changes;
  // we tag it as '' here and components remap below.
  return [{ value: '', label: '__ALL__' }, ...inRegistry, ...extras];
}

function NodesTab({ isAdmin }: { isAdmin: boolean }) {
  const { tr, locale } = useI18n();
  const [items, setItems] = useState<TopologyNode[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [typeFilter, setTypeFilter] = useState('');
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState<TopologyNode | null>(null);
  // creatingType doubles as visible flag (truthy = open) + type pre-fill
  // for the modal. null = closed.
  const [creatingType, setCreatingType] = useState<string | null>(null);
  const canCreateSelectedType = !isDomainManagedNodeType(typeFilter);

  const [nodeTypes, setNodeTypes] = useState<NodeType[]>([]);
  const typeChips = useMemo(
    () => buildTypeChips(items, nodeTypes, locale),
    [items, nodeTypes, locale],
  );
  useEffect(() => {
    listNodeTypes().then((r) => setNodeTypes(r.items ?? [])).catch(() => undefined);
  }, []);

  const fetchNodes = useCallback(async () => {
    setLoading(true);
    try {
      const r = await listNodes({ type: typeFilter || undefined, q: query || undefined, limit: 200 });
      setItems(r.items ?? []);
      setTotal(r.total ?? 0);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [typeFilter, query]);

  useEffect(() => {
    fetchNodes();
  }, [fetchNodes]);

  return (
    <div className="flex flex-1 overflow-hidden">
      <div className="flex flex-1 flex-col overflow-hidden">
        <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800/60 px-6 py-3">
          <div className="flex flex-wrap items-center gap-1.5">
            {typeChips.map((c) => (
              <FilterChip
                key={c.value || 'all'}
                active={typeFilter === c.value}
                onClick={() => setTypeFilter(c.value)}
              >
                {c.label === '__ALL__' ? tr('全部', 'All') : c.label}
              </FilterChip>
            ))}
          </div>
          <div className="relative ml-2 flex-1 max-w-xs">
            <Search size={12} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={tr('按 name 搜索', 'Search by name')}
              className="w-full pl-7 pr-2.5"
            />
          </div>
          <div className="ml-auto flex items-center gap-1.5">
            <Button onClick={fetchNodes} disabled={loading}>
              <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
              {tr('刷新', 'Refresh')}
            </Button>
            {isAdmin && canCreateSelectedType && (
              <Button
                variant="primary"
                onClick={() => setCreatingType(typeFilter || 'service')}
              >
                <Plus size={12} />
                {creatingTypeLabel(typeFilter, tr)}
              </Button>
            )}
          </div>
        </div>
        <div className="flex-1 overflow-auto px-6 py-4">
          {err && (
            <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {err}
            </div>
          )}
          {loading && items.length === 0 ? (
            <div className="text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : items.length === 0 ? (
            <EmptyState
              title={tr('没有节点', 'No nodes yet')}
              hint={isAdmin
                ? tr('录入第一个开始', 'Add your first node to get started')
                : tr('联系管理员录入节点', 'Ask an admin to add nodes')}
              action={isAdmin && canCreateSelectedType ? (
                <Button variant="primary" onClick={() => setCreatingType(typeFilter || 'service')}>
                  <Plus size={12} /> {creatingTypeLabel(typeFilter, tr)}
                </Button>
              ) : undefined}
            />
          ) : (
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
              {items.map((n) => (
                <NodeCard
                  key={n.id}
                  node={n}
                  selected={selected?.id === n.id}
                  onClick={() => setSelected(n)}
                />
              ))}
            </div>
          )}
          <div className="mt-3 text-xs text-zinc-600">{tr(`共 ${total} 个节点`, `${total} nodes total`)}</div>
        </div>
      </div>
      {selected && (
        <NodeDetailDrawer
          node={selected}
          isAdmin={isAdmin}
          onClose={() => setSelected(null)}
          onChanged={fetchNodes}
        />
      )}
      {creatingType && (
        <CreateNodeModal
          defaultType={creatingType}
          onClose={() => setCreatingType(null)}
          onCreated={() => {
            setCreatingType(null);
            fetchNodes();
          }}
        />
      )}
    </div>
  );
}

function NodeCard({
  node,
  selected,
  onClick,
}: {
  node: TopologyNode;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <Card
      interactive
      onClick={onClick}
      className={cn('cursor-pointer', selected && 'border-zinc-600 bg-zinc-900/60')}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium text-zinc-100">{node.name}</div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            <span className="rounded bg-zinc-800 px-1 py-0.5 font-mono">{node.type}</span>
            <span className="ml-1.5">#{node.id}</span>
          </div>
        </div>
      </div>
      {node.props && typeof node.props === 'object' && Object.keys(node.props).length > 0 && (
        <div className="mt-2 line-clamp-1 text-[11px] text-zinc-500">
          {Object.keys(node.props).length} props
        </div>
      )}
    </Card>
  );
}

// NodeDetailDrawer slides in from the right, shows the node's props
// + neighbor relations (both inbound and outbound via src_or_dst_id).
// Admins can add a relation or delete the node from here.
function NodeDetailDrawer({
  node,
  isAdmin,
  onClose,
  onChanged,
}: {
  node: TopologyNode;
  isAdmin: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const { confirmAction, alertAction, dialog } = useDialogs();
  const { tr } = useI18n();
  const [neighbors, setNeighbors] = useState<TopologyRelation[]>([]);
  const [nodeMap, setNodeMap] = useState<Map<number, TopologyNode>>(new Map());
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [addingRelation, setAddingRelation] = useState(false);
  const [busy, setBusy] = useState(false);
  const reportedService = isReportedService(node);
  const domainManaged = isDomainManagedNodeType(node.type) || reportedService;

  const fetchNeighbors = useCallback(async () => {
    setLoading(true);
    try {
      const r = await listRelations({ src_or_dst_id: node.id, limit: 200 });
      setNeighbors(r.items ?? []);
      // Hydrate the other-side node for each relation so the UI can
      // render names instead of bare ids. Parallel GET per unique id
      // (fine for ≤200 neighbors; switch to a batch endpoint if a
      // single node ever lights up with thousands of edges).
      const ids = new Set<number>();
      for (const rel of r.items ?? []) {
        ids.add(rel.src_id === node.id ? rel.dst_id : rel.src_id);
      }
      const got = await Promise.all(
        [...ids].map((id) => getNode(id).catch(() => null)),
      );
      const map = new Map<number, TopologyNode>();
      for (const n of got) {
        if (n) map.set(n.id, n);
      }
      setNodeMap(map);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [node.id]);

  useEffect(() => {
    fetchNeighbors();
  }, [fetchNeighbors]);

  const handleDeleteNode = async () => {
    if (!(await confirmAction(tr(
      `确认删除节点 "${node.name}"？请先确保已无关系引用。`,
      `Delete node "${node.name}"? Make sure no relations reference it first.`,
    )))) return;
    setBusy(true);
    try {
      await deleteNode(node.id);
      onChanged();
      onClose();
    } catch (e) {
      (await alertAction(e instanceof ApiError ? e.message : (e as Error).message));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>{dialog}<aside className="flex w-96 shrink-0 flex-col border-l border-zinc-800/60 bg-zinc-950/40">
      <div className="flex items-start justify-between gap-2 border-b border-zinc-800/60 px-4 py-3">
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold text-zinc-100">{node.name}</div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            <span className="rounded bg-zinc-800 px-1 py-0.5 font-mono">{node.type}</span>
            <span className="ml-1.5">#{node.id}</span>
          </div>
        </div>
        <Button variant="subtle" size="sm"
          type="button"
          onClick={onClose}
          className="p-1"
          aria-label={tr('关闭', 'Close')}
        >
          <X size={14} />
        </Button>
      </div>
      <div className="flex-1 overflow-auto px-4 py-3">
        {reportedService && (
          <Link className="mb-3 inline-block text-xs text-indigo-400 hover:underline" to={`/apm?${new URLSearchParams({
            service_name: String(node.props?.service_name ?? node.name),
            service_namespace: String(node.props?.service_namespace ?? ''),
            environment: String(node.props?.environment ?? ''),
          })}`}>
            {tr('查看服务', 'View service')}
          </Link>
        )}
        {node.props && typeof node.props === 'object' && Object.keys(node.props).length > 0 && (
          <div className="mb-4">
            <div className="mb-1.5 text-[11px] font-medium uppercase tracking-wider text-zinc-500">
              {tr('属性', 'Props')}
            </div>
            <pre className="overflow-auto rounded-md border border-zinc-800 bg-zinc-900/40 p-2 text-[11px] text-zinc-300">
              {JSON.stringify(node.props, null, 2)}
            </pre>
          </div>
        )}
        <div className="mb-1.5 flex items-center justify-between">
          <div className="text-[11px] font-medium uppercase tracking-wider text-zinc-500">
            {tr(`关系（${neighbors.length}）`, `Relations (${neighbors.length})`)}
          </div>
          {isAdmin && (
            <Button variant="outline" size="sm"
              type="button"
              onClick={() => setAddingRelation(true)}
              className="inline-flex items-center gap-1 px-2 py-0.5"
            >
              <Plus size={10} /> {tr('添加', 'Add')}
            </Button>
          )}
        </div>
        {err && (
          <div className="mb-2 rounded-md border border-red-500/40 bg-red-500/10 px-2 py-1.5 text-[11px] text-red-300">
            {err}
          </div>
        )}
        {loading ? (
          <div className="text-[11px] text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : neighbors.length === 0 ? (
          <div className="rounded-md border border-dashed border-zinc-800 px-3 py-4 text-center text-[11px] text-zinc-600">
            {tr('还没有关系', 'No relations yet')}
          </div>
        ) : (
          <ul className="space-y-1.5">
            {neighbors.map((rel) => (
              <NeighborRow
                key={rel.id}
                rel={rel}
                centerNode={node}
                otherNode={nodeMap.get(rel.src_id === node.id ? rel.dst_id : rel.src_id)}
                isAdmin={isAdmin}
                onDeleted={fetchNeighbors}
              />
            ))}
          </ul>
        )}
      </div>
      {isAdmin && !domainManaged && (
        <div className="border-t border-zinc-800/60 px-4 py-3">
          <Button variant="danger" onClick={handleDeleteNode} disabled={busy} className="w-full justify-center">
            <Trash2 size={12} />
            {tr('删除节点', 'Delete node')}
          </Button>
        </div>
      )}
      {isAdmin && domainManaged && (
        <div className="border-t border-zinc-800/60 px-4 py-3 text-[11px] text-zinc-500">
          {reportedService
            ? tr('此服务及其设备关联由遥测上报自动维护，不能手动修改。', 'This service and its device links are maintained by telemetry and cannot be edited manually.')
            : node.type === 'device'
            ? tr('此节点由设备管理同步，请从设备页面删除。', 'This node is synced from Devices. Delete it from the Devices page.')
            : node.props?.source === 'kubernetes'
              ? tr('此节点由 Kubernetes 托管，请从 Kubernetes 集群页面删除。', 'This node is managed by Kubernetes. Delete it from the Kubernetes cluster page.')
              : tr('此节点由集群管理同步，请从集群页面删除。', 'This node is synced from Clusters. Delete it from the Clusters page.')}
        </div>
      )}
      {addingRelation && (
        <AddRelationModal
          centerNode={node}
          onClose={() => setAddingRelation(false)}
          onCreated={() => {
            setAddingRelation(false);
            fetchNeighbors();
          }}
        />
      )}
    </aside></>
  );
}

function NeighborRow({
  rel,
  centerNode,
  otherNode,
  isAdmin,
  onDeleted,
}: {
  rel: TopologyRelation;
  centerNode: TopologyNode;
  otherNode: TopologyNode | undefined;
  isAdmin: boolean;
  onDeleted: () => void;
}) {
  const { confirmAction, alertAction, dialog } = useDialogs();
  const { tr } = useI18n();
  const centerID = centerNode.id;
  const sourceNode = rel.src_id === centerID ? centerNode : otherNode;
  const destinationNode = rel.dst_id === centerID ? centerNode : otherNode;
  const clusterMembership = rel.type === 'member_of' &&
    sourceNode?.type === 'device' && destinationNode?.type === 'cluster';
  const synced = ['edge_enrollment', 'kubernetes', 'network_discovery', 'apm'].includes(String(rel.props?.source ?? ''));
  const managed = clusterMembership || synced;
  const outgoing = rel.src_id === centerID;
  const arrow = outgoing ? '→' : '←';
  const otherID = outgoing ? rel.dst_id : rel.src_id;
  const handleDelete = async () => {
    if (!(await confirmAction(tr('删除这条关系？', 'Delete this relation?')))) return;
    try {
      await deleteRelation(rel.id);
      onDeleted();
    } catch (e) {
      (await alertAction(e instanceof ApiError ? e.message : (e as Error).message));
    }
  };
  return (
    <>{dialog}<li className="rounded-md border border-zinc-800/60 bg-zinc-900/30 px-2.5 py-1.5">
      <div className="flex items-center gap-2 text-[11px]">
        <span className="font-mono text-zinc-500">{arrow}</span>
        <span className="rounded bg-zinc-800 px-1 py-0.5 font-mono text-zinc-300">{rel.type}</span>
        <span className="min-w-0 flex-1 truncate text-zinc-200">
          {otherNode?.name ?? `node #${otherID}`}
          {otherNode && (
            <span className="ml-1 text-zinc-500">[{otherNode.type}]</span>
          )}
        </span>
        {managed && (
          <span className="shrink-0 text-zinc-500" title={clusterMembership
            ? tr('请在集群内管理成员关系。', 'Manage membership from the cluster page.')
            : rel.props?.source === 'apm'
              ? tr('此关联由遥测上报自动维护，随上报变化更新。', 'This link is maintained automatically as telemetry reports change.')
              : tr('此关系由系统自动同步，请从对应资源管理页面操作。', 'This relation is synced automatically. Manage it from its resource page.')}>
            {tr('托管', 'Managed')}
          </span>
        )}
        {isAdmin && !managed && otherNode && (
          <Button variant="dangerGhost" size="sm"
            type="button"
            onClick={handleDelete}
            className="p-0.5"
            aria-label={tr('删除关系', 'Delete relation')}
          >
            <Trash2 size={11} />
          </Button>
        )}
      </div>
    </li></>
  );
}

function CreateNodeModal({
  onClose,
  onCreated,
  defaultType = 'service',
}: {
  onClose: () => void;
  onCreated: () => void;
  defaultType?: string;
}) {
  const { tr, locale } = useI18n();
  const [type, setType] = useState(isDomainManagedNodeType(defaultType) ? 'service' : defaultType);
  const [name, setName] = useState('');
  const [propsText, setPropsText] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [nodeTypes, setNodeTypes] = useState<NodeType[]>([]);
  // The 类型 picker is a dropdown sourced from the NodeType registry +
  // a "+ 新建类型" pseudo-option that swaps the bottom of the modal
  // into a mini type-register form. Lets the operator both add a node
  // AND introduce a new kind in one flow.
  const [registeringType, setRegisteringType] = useState(false);
  const [newTypeName, setNewTypeName] = useState('');
  const [newTypeDisplay, setNewTypeDisplay] = useState('');
  const [newTypeTier, setNewTypeTier] = useState(99);

  useEffect(() => {
    listNodeTypes().then((r) => setNodeTypes(r.items ?? [])).catch(() => undefined);
  }, []);

  const handleCreate = async () => {
    setErr(null);
    let props: Record<string, unknown> | undefined;
    if (propsText.trim()) {
      try {
        const parsed = JSON.parse(propsText);
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
          props = parsed as Record<string, unknown>;
        } else {
          setErr(tr('属性必须是 JSON 对象（{ ... }）', 'Props must be a JSON object'));
          return;
        }
      } catch {
        setErr(tr('属性 JSON 解析失败', 'Props JSON parse failed'));
        return;
      }
    }
    setBusy(true);
    try {
      // If the operator is registering a new type in the same flow,
      // register it first so the node create with type=X succeeds
      // even on a strict client.
      let effectiveType = type.trim();
      if (registeringType) {
        const nm = newTypeName.trim();
        if (!nm) {
          setErr(tr('请先填新类型的 name', 'Fill in the new type name first'));
          setBusy(false);
          return;
        }
        if (!/^[a-z][a-z0-9_]*$/.test(nm)) {
          setErr(tr('类型 name 仅支持小写字母 / 数字 / 下划线', 'Type name only allows lowercase letters / digits / underscores'));
          setBusy(false);
          return;
        }
        await createNodeType({
          name: nm,
          display_name: newTypeDisplay.trim() || nm,
          tier: newTypeTier,
        });
        effectiveType = nm;
      }
      await createNode({ type: effectiveType, name: name.trim(), props });
      onCreated();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('新建节点', 'New node')}
      footer={
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={handleCreate} disabled={busy || !name.trim()}>
            {tr('创建', 'Create')}
          </Button>
        </div>
      }
    >
      <div className="space-y-3 px-5 py-4 text-xs">
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 px-2 py-1.5 text-red-300">
            {err}
          </div>
        )}
        <Field label={tr('类型', 'Type')}>
          <Select label={tr('类型', 'Type')}
            value={registeringType ? '__new__' : type}
            onValueChange={(selectedValue) => {
              if (selectedValue === '__new__') {
                setRegisteringType(true);
              } else {
                setRegisteringType(false);
                setType(selectedValue);
              }
            }}
            className="w-full"
          >
            {nodeTypes.filter((nt) => !isDomainManagedNodeType(nt.name)).map((nt) => (
              <option key={nt.name} value={nt.name}>
                {localizedTypeLabel(nt, locale)}（{nt.name}）
              </option>
            ))}
            <option value="__new__">{tr('+ 新建类型…', '+ New type…')}</option>
          </Select>
        </Field>
        {registeringType && (
          <div className="space-y-2 rounded-md border border-indigo-500/30 bg-indigo-500/5 px-3 py-2">
            <div className="text-[11px] font-medium text-indigo-300">{tr('新建节点类型', 'New node type')}</div>
            <Field label={tr('name（snake_case，AIOps 用）', 'name (snake_case, AIOps key)')}>
              <Input
                value={newTypeName}
                onChange={(e) => setNewTypeName(e.target.value)}
                placeholder={tr('如 vm', 'e.g. vm')}
                className="w-full font-mono"
              />
            </Field>
            <Field label={tr('display_name（chip 上显示的中文 / i18n 标签）', 'display_name (chip label, any language)')}>
              <Input
                value={newTypeDisplay}
                onChange={(e) => setNewTypeDisplay(e.target.value)}
                placeholder={tr('如 虚拟机', 'e.g. VM')}
                className="w-full"
              />
            </Field>
            <Field
              label={tr('层级（0=顶层应用，4=机架；不知道填 99 = 单独成行）', 'Tier (0=top app, 4=rack; 99 = standalone row)')}
            >
              <Input
                type="number"
                value={newTypeTier}
                onChange={(e) => setNewTypeTier(Number(e.target.value))}
                min={0}
                max={99}
                className="w-24"
              />
            </Field>
          </div>
        )}
        <Field label={tr('名称', 'Name')}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={tr('比如 order-api', 'e.g. order-api')}
            className="w-full"
          />
        </Field>
        <Field
          label={tr('属性（可选 JSON 对象）', 'Props (optional JSON object)')}
          hint={tr('例如 {"owner_team": "pay", "region": "cn-hz"}', 'e.g. {"owner_team": "pay", "region": "cn-hz"}')}
        >
          <Textarea
            value={propsText}
            onChange={(e) => setPropsText(e.target.value)}
            rows={4}
            className="w-full font-mono"
          />
        </Field>
      </div>
    </Modal>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <Label className="mb-1 block text-[11px] font-medium text-zinc-400">{label}</Label>
      {children}
      {hint && <div className="mt-0.5 text-[10px] text-zinc-600">{hint}</div>}
    </div>
  );
}

// AddRelationModal — admin clicks "添加" in the node drawer, picks
// the other endpoint by id (with a small lookahead-search input) and
// a relation type from the type registry, optionally fills props.
function AddRelationModal({
  centerNode,
  onClose,
  onCreated,
}: {
  centerNode: TopologyNode;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { tr, locale } = useI18n();
  const [direction, setDirection] = useState<'outgoing' | 'incoming'>('outgoing');
  const [search, setSearch] = useState('');
  const [candidates, setCandidates] = useState<TopologyNode[]>([]);
  const [otherID, setOtherID] = useState<number | null>(null);
  const [relTypeName, setRelTypeName] = useState('depends_on');
  const [relTypes, setRelTypes] = useState<RelationType[]>([]);
  const [propsText, setPropsText] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    listRelationTypes().then((r) => setRelTypes(r.items ?? [])).catch(() => undefined);
  }, []);

  useEffect(() => {
    const handle = setTimeout(() => {
      listNodes({ q: search, limit: 20 })
        .then((r) => setCandidates((r.items ?? []).filter((n) => n.id !== centerNode.id &&
          !(isReportedService(centerNode) && n.type === 'device') &&
          !(centerNode.type === 'device' && isReportedService(n)))))
        .catch(() => undefined);
    }, 200);
    return () => clearTimeout(handle);
  }, [search, centerNode]);

  const handleCreate = async () => {
    setErr(null);
    if (!otherID) {
      setErr(tr('请选一个对端节点', 'Pick the other endpoint'));
      return;
    }
    let props: Record<string, unknown> | undefined;
    if (propsText.trim()) {
      try {
        const parsed = JSON.parse(propsText);
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
          props = parsed as Record<string, unknown>;
        } else {
          setErr(tr('属性必须是 JSON 对象', 'Props must be a JSON object'));
          return;
        }
      } catch {
        setErr(tr('属性 JSON 解析失败', 'Props JSON parse failed'));
        return;
      }
    }
    setBusy(true);
    try {
      const srcID = direction === 'outgoing' ? centerNode.id : otherID;
      const dstID = direction === 'outgoing' ? otherID : centerNode.id;
      await createRelation({ src_id: srcID, dst_id: dstID, type: relTypeName, props });
      onCreated();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr(`为 "${centerNode.name}" 添加关系`, `Add relation for "${centerNode.name}"`)}
      size="lg"
      footer={
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={handleCreate} disabled={busy || !otherID}>
            {tr('创建关系', 'Create relation')}
          </Button>
        </div>
      }
    >
      <div className="space-y-3 px-5 py-4 text-xs">
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 px-2 py-1.5 text-red-300">
            {err}
          </div>
        )}
        <Field label={tr('方向', 'Direction')}>
          <div className="flex gap-1.5">
            <FilterChip active={direction === 'outgoing'} onClick={() => setDirection('outgoing')}>
              {tr('本节点 → 对端', 'This → other')}
            </FilterChip>
            <FilterChip active={direction === 'incoming'} onClick={() => setDirection('incoming')}>
              {tr('对端 → 本节点', 'Other → this')}
            </FilterChip>
          </div>
        </Field>
        <Field label={tr('关系类型', 'Relation type')}>
          <Select label={tr('关系类型', 'Relation type')}
            value={relTypeName}
            onValueChange={(selectedValue) => setRelTypeName(selectedValue)}
            className="w-full"
          >
            {relTypes.map((rt) => (
              <option key={rt.name} value={rt.name}>
                {rt.name}
                {(rt.display_name || rt.display_name_en) ? ` (${localizedTypeLabel(rt, locale)})` : ''}
                {rt.builtin ? ' · builtin' : ''}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={tr('对端节点（按 name 搜索）', 'Other node (search by name)')}>
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={tr('开始输入...', 'Start typing...')}
            className="w-full"
          />
          <div className="mt-1.5 max-h-48 overflow-auto rounded-md border border-zinc-800 bg-zinc-900/40">
            {candidates.length === 0 ? (
              <div className="px-2.5 py-3 text-center text-[11px] text-zinc-600">{tr('没结果', 'No results')}</div>
            ) : (
              candidates.map((n) => (
                <button
                  key={n.id}
                  type="button"
                  onClick={() => setOtherID(n.id)}
                  className={cn(
                    'flex w-full items-center justify-between gap-2 border-b border-zinc-800/60 px-2.5 py-1.5 text-left text-[11px] last:border-b-0 hover:bg-zinc-800/40',
                    otherID === n.id && 'bg-indigo-500/10',
                  )}
                >
                  <span className="truncate text-zinc-200">{n.name}</span>
                  <span className="text-zinc-500">
                    <span className="font-mono">{n.type}</span> #{n.id}
                  </span>
                </button>
              ))
            )}
          </div>
        </Field>
        <Field label={tr('属性（可选 JSON）', 'Props (optional JSON)')}>
          <Textarea
            value={propsText}
            onChange={(e) => setPropsText(e.target.value)}
            rows={3}
            className="w-full font-mono"
          />
        </Field>
      </div>
    </Modal>
  );
}

// ---------- NodeTypesPanel -------------------------------------------------
//
// Lives at the top of the 类型管理 tab. 5 builtin rows seeded by
// topology.Migrate (chip label sources of truth); operators can
// register custom kinds with their own display_name + tier so chips
// stay WYSIWYG without losing i18n on the builtin set.
function NodeTypesPanel({ isAdmin }: { isAdmin: boolean }) {
  const { confirmAction, alertAction, dialog } = useDialogs();
  const { tr, locale } = useI18n();
  const [items, setItems] = useState<NodeType[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const fetch = useCallback(async () => {
    setLoading(true);
    try {
      const r = await listNodeTypes();
      setItems(r.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetch();
  }, [fetch]);

  const handleDelete = async (nt: NodeType) => {
    if (!(await confirmAction(tr(
      `确认删除自定义类型 "${nt.name}"？需先把该类型下所有节点删完。`,
      `Delete custom type "${nt.name}"? Delete all its nodes first.`,
    )))) return;
    try {
      await deleteNodeType(nt.name);
      fetch();
    } catch (e) {
      (await alertAction(e instanceof ApiError ? e.message : (e as Error).message));
    }
  };

  return (
    <>{dialog}<div className="border-b border-zinc-800/60">
      <div className="flex items-center justify-between gap-2 px-6 py-3">
        <div className="text-xs text-zinc-400">
          {tr(
            '节点类型 — chip 标签和 tier 布局的来源。5 个 builtin 不可删；自定义类型可加可删（前提是无节点引用）。',
            'Node types — source of chip labels and tier layout. 5 builtins are immutable; custom types are addable/removable (when not referenced).',
          )}
        </div>
        <div className="flex items-center gap-1.5">
          <Button onClick={fetch} disabled={loading}>
            <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
            {tr('刷新', 'Refresh')}
          </Button>
          {isAdmin && (
            <Button variant="primary" onClick={() => setCreating(true)}>
              <Plus size={12} />
              {tr('注册节点类型', 'Register node type')}
            </Button>
          )}
        </div>
      </div>
      <div className="px-6 pb-4">
        {err && (
          <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}
        {loading && items.length === 0 ? (
          <div className="text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : (
          <div className="grid grid-cols-1 gap-2 lg:grid-cols-2">
            {items.map((nt) => (
              <Card key={nt.name}>
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="flex items-center gap-1.5">
                      <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-xs text-zinc-100">
                        {nt.name}
                      </span>
                      {nt.builtin && (
                        <span className="rounded bg-indigo-500/15 px-1.5 py-0.5 text-[10px] text-indigo-300">
                          builtin
                        </span>
                      )}
                    </div>
                    <div className="mt-1 text-sm text-zinc-100">{localizedTypeLabel(nt, locale)}</div>
                  </div>
                  {isAdmin && !nt.builtin && (
                    <Button variant="dangerGhost" size="sm"
                      type="button"
                      onClick={() => handleDelete(nt)}
                      className="p-1"
                      aria-label={tr('删除', 'Delete')}
                    >
                      <Trash2 size={12} />
                    </Button>
                  )}
                </div>
                <div className="mt-2 text-[11px] text-zinc-500">
                  {tr(`tier ${nt.tier === 99 ? '99（自定义层）' : nt.tier}`, `tier ${nt.tier === 99 ? '99 (custom)' : nt.tier}`)}
                </div>
                {nt.description && (
                  <div className="mt-2 text-[11px] text-zinc-400">{nt.description}</div>
                )}
              </Card>
            ))}
          </div>
        )}
      </div>
      {creating && (
        <CreateNodeTypeModal
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            fetch();
          }}
        />
      )}
    </div></>
  );
}

function CreateNodeTypeModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [displayNameEN, setDisplayNameEN] = useState('');
  const [tier, setTier] = useState(99);
  const [desc, setDesc] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const handleCreate = async () => {
    setErr(null);
    if (!name.trim()) {
      setErr(tr('需要 name', 'name required'));
      return;
    }
    if (!/^[a-z][a-z0-9_]*$/.test(name.trim())) {
      setErr(tr('name 仅支持小写字母 / 数字 / 下划线', 'name only allows lowercase letters / digits / underscores'));
      return;
    }
    setBusy(true);
    try {
      await createNodeType({
        name: name.trim(),
        display_name: displayName.trim() || name.trim(),
        display_name_en: displayNameEN.trim() || undefined,
        tier,
        description: desc.trim() || undefined,
      });
      onCreated();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('注册节点类型', 'Register node type')}
      footer={
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={handleCreate} disabled={busy}>
            {tr('注册', 'Register')}
          </Button>
        </div>
      }
    >
      <div className="space-y-3 px-5 py-4 text-xs">
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 px-2 py-1.5 text-red-300">
            {err}
          </div>
        )}
        <Field label={tr('name（snake_case，AIOps 用作 key）', 'name (snake_case, AIOps key)')}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={tr('如 vm / datacenter', 'e.g. vm / datacenter')}
            className="w-full font-mono"
          />
        </Field>
        <Field label={tr('display_name（中文标签）', 'display_name (Chinese label)')}>
          <Input
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            placeholder={tr('如 虚拟机 / 数据中心', 'e.g. 虚拟机 / 数据中心')}
            className="w-full"
          />
        </Field>
        <Field label={tr('display_name_en（可选；en-US locale 下用）', 'display_name_en (optional; shown in en-US locale)')}>
          <Input
            value={displayNameEN}
            onChange={(e) => setDisplayNameEN(e.target.value)}
            placeholder={tr('如 VM / Datacenter', 'e.g. VM / Datacenter')}
            className="w-full"
          />
        </Field>
        <Field
          label={tr(
            'tier（图谱布局：0 应用 / 1 服务 / 2 集群 / 3 设备 / 4 机架 / 99 单独一行）',
            'tier (layout: 0 app / 1 service / 2 cluster / 3 device / 4 rack / 99 standalone row)',
          )}
        >
          <Input
            type="number"
            value={tier}
            onChange={(e) => setTier(Number(e.target.value))}
            min={0}
            max={99}
            className="w-24"
          />
        </Field>
        <Field label={tr('description（可选）', 'description (optional)')}>
          <Textarea
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
            rows={2}
            className="w-full"
          />
        </Field>
      </div>
    </Modal>
  );
}

// ---------- RelationTypesTab -----------------------------------------------

function RelationTypesTab({ isAdmin }: { isAdmin: boolean }) {
  const { tr } = useI18n();
  const [items, setItems] = useState<RelationType[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const fetch = useCallback(async () => {
    setLoading(true);
    try {
      const r = await listRelationTypes();
      setItems(r.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetch();
  }, [fetch]);

  return (
    <div className="flex flex-col">
      <div className="flex items-center justify-between gap-2 border-b border-zinc-800/60 px-6 py-3">
        <div className="text-xs text-zinc-500">
          {tr(
            '关系类型决定 AIOps 怎么推理。6 个 builtin 不可删，自定义需声明 direction + semantics_tag。',
            'Relation types drive AIOps reasoning. 6 builtins are immutable; custom types must declare direction + semantics_tag.',
          )}
        </div>
        <div className="flex items-center gap-1.5">
          <Button onClick={fetch} disabled={loading}>
            <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
            {tr('刷新', 'Refresh')}
          </Button>
          {isAdmin && (
            <Button variant="primary" onClick={() => setCreating(true)}>
              <Plus size={12} />
              {tr('注册类型', 'Register type')}
            </Button>
          )}
        </div>
      </div>
      <div className="px-6 py-4">
        {err && (
          <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}
        {loading && items.length === 0 ? (
          <div className="text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : items.length === 0 ? (
          <EmptyState title={tr('没有关系类型', 'No relation types')} hint={tr('正常情况下 6 个 builtin 会自动种入', 'Normally 6 builtin types auto-seed')} />
        ) : (
          <div className="grid grid-cols-1 gap-2 lg:grid-cols-2">
            {items.map((rt) => (
              <RelationTypeCard key={rt.name} rt={rt} isAdmin={isAdmin} onChanged={fetch} />
            ))}
          </div>
        )}
      </div>
      {creating && (
        <CreateRelationTypeModal
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            fetch();
          }}
        />
      )}
    </div>
  );
}

function RelationTypeCard({
  rt,
  isAdmin,
  onChanged,
}: {
  rt: RelationType;
  isAdmin: boolean;
  onChanged: () => void;
}) {
  const { confirmAction, alertAction, dialog } = useDialogs();
  const { tr, locale } = useI18n();
  const handleDelete = async () => {
    if (!(await confirmAction(tr(`确认删除自定义关系类型 "${rt.name}"？`, `Delete custom relation type "${rt.name}"?`)))) return;
    try {
      await deleteRelationType(rt.name);
      onChanged();
    } catch (e) {
      (await alertAction(e instanceof ApiError ? e.message : (e as Error).message));
    }
  };
  return (
    <>{dialog}<Card>
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-1.5">
            <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-xs text-zinc-100">
              {rt.name}
            </span>
            {rt.builtin && (
              <span className="rounded bg-indigo-500/15 px-1.5 py-0.5 text-[10px] text-indigo-300">
                builtin
              </span>
            )}
            {rt.propagates_failure && (
              <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] text-amber-300">
                {tr('传播故障', 'Propagates failure')}
              </span>
            )}
          </div>
          {(rt.display_name || rt.display_name_en) && (
            <div className="mt-1 text-sm text-zinc-100">{localizedTypeLabel(rt, locale)}</div>
          )}
        </div>
        {isAdmin && !rt.builtin && (
          <Button variant="dangerGhost" size="sm"
            type="button"
            onClick={handleDelete}
            className="p-1"
            aria-label={tr('删除', 'Delete')}
          >
            <Trash2 size={12} />
          </Button>
        )}
      </div>
      <div className="mt-2 grid grid-cols-2 gap-1.5 text-[11px]">
        <Meta label={tr('方向', 'Direction')} value={rt.direction} mono />
        <Meta label={tr('语义', 'Semantics')} value={rt.semantics_tag} mono />
      </div>
      {rt.description && (
        <div className="mt-2 text-[11px] text-zinc-400">{rt.description}</div>
      )}
    </Card></>
  );
}

function Meta({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="rounded border border-zinc-800/60 px-1.5 py-1">
      <div className="text-[10px] text-zinc-500">{label}</div>
      <div className={cn('text-zinc-200', mono && 'font-mono text-[11px]')}>{value}</div>
    </div>
  );
}

function CreateRelationTypeModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [displayNameEN, setDisplayNameEN] = useState('');
  const [propagates, setPropagates] = useState(false);
  const [direction, setDirection] = useState<RelationDirection>('src_to_dst');
  const [tag, setTag] = useState<SemanticsTag>('annotation');
  const [desc, setDesc] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const handleCreate = async () => {
    setErr(null);
    if (!name.trim()) {
      setErr(tr('需要 name', 'name required'));
      return;
    }
    if (!/^[a-z][a-z0-9_]*$/.test(name.trim())) {
      setErr(tr('name 仅支持小写字母 / 数字 / 下划线（蛇形 snake_case）', 'name only allows lowercase letters / digits / underscores (snake_case)'));
      return;
    }
    setBusy(true);
    try {
      await createRelationType({
        name: name.trim(),
        display_name: displayName.trim() || undefined,
        display_name_en: displayNameEN.trim() || undefined,
        propagates_failure: propagates,
        direction,
        semantics_tag: tag,
        description: desc.trim() || undefined,
      });
      onCreated();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('注册新关系类型', 'Register new relation type')}
      size="lg"
      footer={
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={handleCreate} disabled={busy}>
            {tr('注册', 'Register')}
          </Button>
        </div>
      }
    >
      <div className="space-y-3 px-5 py-4 text-xs">
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 px-2 py-1.5 text-red-300">
            {err}
          </div>
        )}
        <Field label={tr('name（snake_case，全局唯一）', 'name (snake_case, globally unique)')}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={tr('如 shares_storage_with', 'e.g. shares_storage_with')}
            className="w-full font-mono"
          />
        </Field>
        <Field label={tr('display_name（中文标签）', 'display_name (Chinese label)')}>
          <Input
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            placeholder={tr('如 共享存储', 'e.g. 共享存储')}
            className="w-full"
          />
        </Field>
        <Field label={tr('display_name_en（可选；en-US locale 下用）', 'display_name_en (optional; shown in en-US locale)')}>
          <Input
            value={displayNameEN}
            onChange={(e) => setDisplayNameEN(e.target.value)}
            placeholder={tr('如 Shared storage', 'e.g. Shared storage')}
            className="w-full"
          />
        </Field>
        <Field
          label={tr('direction（故障 / 影响沿哪个方向传）', 'direction (which way failure flows)')}
          hint={RELATION_DIRECTIONS.find((d) => d.value === direction)?.hint}
        >
          <Select label={tr('direction（故障 / 影响沿哪个方向传）', 'direction (which way failure flows)')}
            value={direction}
            onValueChange={(selectedValue) => setDirection(selectedValue as RelationDirection)}
            className="w-full"
          >
            {RELATION_DIRECTIONS.map((d) => (
              <option key={d.value} value={d.value}>
                {d.label} — {d.hint}
              </option>
            ))}
          </Select>
        </Field>
        <Field
          label={tr('semantics_tag（AIOps 推理大类）', 'semantics_tag (AIOps reasoning bucket)')}
          hint={SEMANTICS_TAGS.find((t) => t.value === tag)?.hint}
        >
          <Select label={tr('semantics_tag（AIOps 推理大类）', 'semantics_tag (AIOps reasoning bucket)')}
            value={tag}
            onValueChange={(selectedValue) => setTag(selectedValue as SemanticsTag)}
            className="w-full"
          >
            {SEMANTICS_TAGS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label} — {t.hint}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="propagates_failure">
          <Label className="flex items-center gap-2 text-[11px] text-zinc-300">
            <Checkbox

              checked={propagates}
              onCheckedChange={(checkedValue) => setPropagates(checkedValue)}
              className="h-3.5 w-3.5"
            />
            {tr(
              '勾选：故障会沿这条关系传播（AIOps 影响面计算会走这条边）',
              'Check: failure propagates along this relation (AIOps blast-radius walks it)',
            )}
          </Label>
        </Field>
        <Field label={tr('description（可选）', 'description (optional)')}>
          <Textarea
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
            rows={2}
            className="w-full"
          />
        </Field>
      </div>
    </Modal>
  );
}

// ---------- GraphTab ------------------------------------------------------
//
// Loads ALL nodes + relations into memory and hands them to the react-
// flow component. Fine for ≤2k nodes (the working assumption for v1);
// when we cross that we'll add server-side viewport pagination.

function GraphTab({ isAdmin }: { isAdmin: boolean }) {
  const { tr, locale } = useI18n();
  const [nodes, setNodes] = useState<TopologyNode[]>([]);
  const [relations, setRelations] = useState<TopologyRelation[]>([]);
  const [relationTypes, setRelationTypes] = useState<RelationType[]>([]);
  const [nodeTypes, setNodeTypes] = useState<NodeType[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [typeFilter, setTypeFilter] = useState('');
  const [selected, setSelected] = useState<TopologyNode | null>(null);
  // Track which create-modal to show. The chip filter doubles as a
  // pre-fill hint for the modal's type field — if you're looking at
  // 服务, clicking 新建 should propose creating a service.
  const [creatingType, setCreatingType] = useState<string | null>(null);
  const canCreateSelectedType = !isDomainManagedNodeType(typeFilter);
  // Show ALL nodes by default — including orphans (no relations yet) —
  // so a freshly-registered fleet doesn't look empty. Orphans only get
  // pruned when the operator focuses an app (appFocus): the BFS reachable
  // set naturally drops anything not connected to that app. The manual
  // toggle below still lets the operator hide orphans globally if the
  // unwired-device clutter bothers them.
  const [hideOrphans, setHideOrphans] = useState(false);
  // Per-relation-type visibility. Initialised lazily once the relation
  // type catalogue loads (see effect below) — null means "show all".
  const [visibleRelTypes, setVisibleRelTypes] = useState<Set<string> | null>(null);
  // Focus filter: when set to an app's node id, BFS from there and
  // restrict the graph to nodes reachable via any relation. Lets an
  // operator zoom from "everything in the tenant" to "everything
  // touching order-system" with one click — the equivalent of
  // selecting a service map in Datadog or filtering by app tag.
  const [appFocus, setAppFocus] = useState<number | null>(null);

  const fetchAll = useCallback(async () => {
    setLoading(true);
    try {
      const [n, r, rt, nt] = await Promise.all([
        listNodes({ type: typeFilter || undefined, limit: 2000 }),
        listRelations({ limit: 5000 }),
        listRelationTypes(),
        listNodeTypes(),
      ]);
      setNodes(n.items ?? []);
      setRelations(r.items ?? []);
      setRelationTypes(rt.items ?? []);
      setNodeTypes(nt.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [typeFilter]);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  // First time relation types load, default to ALL visible (the user's
  // mental model: "I see everything until I hide something"). After
  // that, preserve the operator's choices across refreshes.
  useEffect(() => {
    if (visibleRelTypes === null && relationTypes.length > 0) {
      setVisibleRelTypes(new Set(relationTypes.map((rt) => rt.name)));
    }
  }, [relationTypes, visibleRelTypes]);

  // App focus: BFS reachable set from the selected app node, walking
  // ALL relations as an undirected graph (operator-defined scope, not
  // just propagating types — they may want to see the whole footprint
  // including monitors/annotation edges). Returns null when no focus
  // is set, meaning "show everything".
  const focusedNodeIDs = useMemo<Set<number> | null>(() => {
    if (appFocus == null) return null;
    const reachable = new Set<number>([appFocus]);
    const queue = [appFocus];
    while (queue.length) {
      const cur = queue.shift()!;
      for (const r of relations) {
        const other = r.src_id === cur ? r.dst_id : r.dst_id === cur ? r.src_id : null;
        if (other != null && !reachable.has(other)) {
          reachable.add(other);
          queue.push(other);
        }
      }
    }
    return reachable;
  }, [appFocus, relations]);

  // displayedNodes folds the app focus on top of the node-type chip.
  const displayedNodes = useMemo(() => {
    if (!focusedNodeIDs) return nodes;
    return nodes.filter((n) => focusedNodeIDs.has(n.id));
  }, [nodes, focusedNodeIDs]);

  // visibleRelations folds three filters: app focus, node-type chip,
  // and relation-type checkboxes. The Graph component re-applies the
  // same logic internally; we compute here so the "N 关系" count in
  // the toolbar matches what's actually drawn.
  const visibleRelations = useMemo(() => {
    const nodeIDs = new Set(displayedNodes.map((n) => n.id));
    return relations.filter((r) => {
      if (!nodeIDs.has(r.src_id) || !nodeIDs.has(r.dst_id)) return false;
      if (visibleRelTypes && !visibleRelTypes.has(r.type)) return false;
      return true;
    });
  }, [relations, displayedNodes, visibleRelTypes]);

  // App list for the focus dropdown — derived from the loaded nodes,
  // sorted by name so the order is stable across refreshes.
  const appNodes = useMemo(
    () =>
      nodes
        .filter((n) => n.type === 'app')
        .sort((a, b) => a.name.localeCompare(b.name)),
    [nodes],
  );

  const typeChips = useMemo(
    () => buildTypeChips(nodes, nodeTypes, locale),
    [nodes, nodeTypes, locale],
  );

  return (
    <div className="flex flex-1 overflow-hidden">
      <div className="flex flex-1 flex-col overflow-hidden">
        <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800/60 px-6 py-3">
          {/* App focus dropdown comes first — it's the most coarse
              filter ("which application am I looking at?") and lives
              left of the type chips so the operator's eye lands on
              it before they start narrowing further. */}
          {appNodes.length > 0 && (
            <FilterField label={tr('聚焦应用', 'Focus app')} className="max-w-sm shrink-0">
              <Select
                value={appFocus ?? ''}
                onValueChange={(selectedValue) => setAppFocus(selectedValue ? Number(selectedValue) : null)}

              >
                <option value="">{tr('全部应用', 'All apps')}</option>
                {appNodes.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </Select>
            </FilterField>
          )}
          <div className="flex flex-wrap items-center gap-1.5">
            {typeChips.map((c) => (
              <FilterChip
                key={c.value || 'all'}
                active={typeFilter === c.value}
                onClick={() => setTypeFilter(c.value)}
              >
                {c.label === '__ALL__' ? tr('全部', 'All') : c.label}
              </FilterChip>
            ))}
          </div>
          <div className="ml-2 text-[11px] text-zinc-600">
            {tr(
              `${displayedNodes.length} 节点 · ${visibleRelations.length} 关系`,
              `${displayedNodes.length} nodes · ${visibleRelations.length} relations`,
            )}
            {appFocus != null && (
              <span className="ml-1 text-indigo-400">
                {tr(
                  ` · 聚焦 ${appNodes.find((a) => a.id === appFocus)?.name ?? ''}`,
                  ` · focused on ${appNodes.find((a) => a.id === appFocus)?.name ?? ''}`,
                )}
              </span>
            )}
          </div>
          <Label className="ml-2 flex cursor-pointer items-center gap-1 text-[11px] text-zinc-400 hover:text-zinc-200">
            <Checkbox

              checked={hideOrphans}
              onCheckedChange={(checkedValue) => setHideOrphans(checkedValue)}
              className="h-3 w-3"
            />
            {tr('隐藏孤立节点', 'Hide orphan nodes')}
          </Label>
          <RelationTypeFilter
            relationTypes={relationTypes}
            visible={visibleRelTypes ?? new Set(relationTypes.map((r) => r.name))}
            onChange={setVisibleRelTypes}
          />
          <div className="ml-auto flex items-center gap-1.5">
            <Button onClick={fetchAll} disabled={loading}>
              <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
              {tr('刷新', 'Refresh')}
            </Button>
            {isAdmin && canCreateSelectedType && (
              <Button
                variant="primary"
                onClick={() => setCreatingType(typeFilter || 'service')}
              >
                <Plus size={12} />
                {creatingTypeLabel(typeFilter, tr)}
              </Button>
            )}
          </div>
        </div>
        <div className="flex-1 overflow-hidden">
          {err && (
            <div className="m-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {err}
            </div>
          )}
          {loading && nodes.length === 0 ? (
            <div className="p-6 text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : nodes.length === 0 ? (
            <EmptyState
              title={tr('还没有节点', 'No nodes yet')}
              hint={isAdmin
                ? tr('录入第一个开始', 'Add your first node to get started')
                : tr('联系管理员录入节点', 'Ask an admin to add nodes')}
              action={isAdmin && canCreateSelectedType ? (
                <Button variant="primary" onClick={() => setCreatingType(typeFilter || 'service')}>
                  <Plus size={12} /> {creatingTypeLabel(typeFilter, tr)}
                </Button>
              ) : undefined}
            />
          ) : displayedNodes.length === 0 ? (
            <EmptyState
              title={tr('此应用下还没有节点', 'No nodes under this app')}
              hint={tr(
                '给应用挂 member_of / depends_on 关系后再查看',
                'Add member_of or depends_on relations to this app first',
              )}
              action={
                <Button onClick={() => setAppFocus(null)}>
                  {tr('查看全图', 'View full graph')}
                </Button>
              }
            />
          ) : (
            <TopologyGraph
              nodes={displayedNodes}
              relations={visibleRelations}
              relationTypes={relationTypes}
              selectedID={selected?.id ?? null}
              hideOrphans={hideOrphans}
              visibleRelationTypes={visibleRelTypes ?? undefined}
              onSelect={setSelected}
            />
          )}
        </div>
      </div>
      {selected && (
        <NodeDetailDrawer
          node={selected}
          isAdmin={isAdmin}
          onClose={() => setSelected(null)}
          onChanged={fetchAll}
        />
      )}
      {creatingType && (
        <CreateNodeModal
          defaultType={creatingType}
          onClose={() => setCreatingType(null)}
          onCreated={() => {
            setCreatingType(null);
            fetchAll();
          }}
        />
      )}
    </div>
  );
}

// RelationTypeFilter — a popover-style multi-toggle that lets the
// operator hide noise (annotation / observation) or focus on the
// failure-propagation subset (hard_dep / runtime_dep / traffic).
// We render it as a button that opens a small panel of checkboxes
// per relation type, grouped by semantics_tag for legibility.
function RelationTypeFilter({
  relationTypes,
  visible,
  onChange,
}: {
  relationTypes: RelationType[];
  visible: Set<string>;
  onChange(s: Set<string>): void;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const total = relationTypes.length;
  const visibleCount = relationTypes.filter((rt) => visible.has(rt.name)).length;
  // Toggle one rt's visibility; never let the set become null (we
  // distinguish "explicit-all" from "uninitialised" upstream).
  const toggle = (name: string) => {
    const next = new Set(visible);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    onChange(next);
  };
  const selectAll = () => onChange(new Set(relationTypes.map((rt) => rt.name)));
  const propagatingOnly = () =>
    onChange(new Set(relationTypes.filter((rt) => rt.propagates_failure).map((rt) => rt.name)));
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger className="rounded-md border border-border bg-card px-2 py-1 text-xs text-text-muted">
      {tr('关系类型', 'Relations')} {visibleCount === total ? tr('全部', 'All') : `${visibleCount}/${total}`}
    </PopoverTrigger>
    <PopoverContent className="w-72" aria-label={tr('关系类型', 'Relations')}>
            <div className="mb-2 flex items-center justify-between text-[11px] text-zinc-500">
              <span>{tr('选择要画的边类型', 'Select edges to draw')}</span>
              <div className="flex items-center gap-1.5">
                <Button variant="outline" size="sm"
                  type="button"
                  onClick={selectAll}
                  className="px-1.5 py-0.5"
                >
                  {tr('全选', 'All')}
                </Button>
                <Button variant="outline" size="sm"
                  type="button"
                  onClick={propagatingOnly}
                  className="px-1.5 py-0.5"
                >
                  {tr('只看传故障的', 'Propagating only')}
                </Button>
              </div>
            </div>
            <ul className="max-h-64 space-y-0.5 overflow-auto">
              {relationTypes.map((rt) => (
                <li key={rt.name}>
                  <Label className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-[11px] hover:bg-zinc-900">
                    <Checkbox

                      checked={visible.has(rt.name)}
                      onCheckedChange={() => toggle(rt.name)}
                      className="h-3 w-3"
                    />
                    <span className="rounded bg-zinc-800 px-1 py-0.5 font-mono text-zinc-300">
                      {rt.name}
                    </span>
                    {rt.propagates_failure && (
                      <span className="text-[10px] text-amber-400">{tr('传故障', 'propagates')}</span>
                    )}
                    <span className="ml-auto text-zinc-500">{rt.semantics_tag}</span>
                  </Label>
                </li>
              ))}
            </ul>    </PopoverContent>
  </Popover>;
}

// creatingTypeLabel picks a Chinese button label keyed on the active
// chip — operators in the 服务 view see "+新建服务" so the action is
// unambiguous, while the 全部 view defaults to "+新建节点".
function creatingTypeLabel(
  typeFilter: string,
  tr: (zh: string, en: string) => string,
): string {
  switch (typeFilter) {
    case 'device':
      return tr('新建设备', 'New device');
    case 'service':
      return tr('新建服务', 'New service');
    case 'cluster':
      return tr('新建集群', 'New cluster');
    case 'app':
      return tr('新建应用', 'New app');
    case 'rack':
      return tr('新建机架', 'New rack');
    default:
      return tr('新建节点', 'New node');
  }
}
