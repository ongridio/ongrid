import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { PageHeader } from '@/components/ui/PageHeader';
import { Select } from '@/components/ui/Select';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';

import { describeError } from '../api/http';
import { listCITypes, type CIType } from '../api/citype';
import {
  createDCIMNode,
  deleteDCIMNode,
  getDCIMTree,
  mountDevice,
  moveDevice,
  rackHistory,
  recalcRackUnits,
  roomRacks,
  unmountDevice,
  updateDCIMNode,
  type DCIMNode,
  type DCIMType,
  type RackHistory,
} from '../api/dcim';
import { listInstances, searchHierarchy, type SearchRow } from '../api/instance';
import { DataTable, type Column } from '../components/DataTable';
import { Drawer } from '../components/Drawer';
import { Tree, type TreeNode } from '../components/Tree';
import { useToast } from '../components/Toast';
import { displayValue } from '../lib/valuetype';

// 数据中心 — the physical containment tree, and the units inside each rack.
//
// THE TREE IS FOUR ORDINARY MODELS. `dcim_region` → `dcim_idc` →
// `dcim_server_room` → `dcim_rack` are instances like any other, connected by
// ordinary relations, which is why this page can exist without the model layer
// knowing anything about racks. It also means a deployment with none of those
// models gets an empty tree and a link to create them, rather than a 404.
//
// THE RACK IS WHERE THE ARITHMETIC LIVES, and the arithmetic is the server's:
// `free_u_count` is recomputed in the transaction that changes a rack's contents,
// so this page never adds and subtracts units itself. The one button that does
// recompute asks the server to, which is what it is for.

type Selection = { kind: 'room'; node: DCIMNode } | { kind: 'rack'; node: DCIMNode; room: DCIMNode } | null;

export default function DCIMPage() {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [tree, setTree] = useState<{
    nodes: TreeNode[];
    label: (node: DCIMNode) => string;
    raw: Map<string, DCIMNode>;
    /** Every node's id, so the tree opens showing its shape. */
    allIDs: string[];
    /** child id → parent id, so a rack can name the room the endpoint needs. */
    parentOf: Map<string, string>;
    /** model id → the attribute the tree labels that model by. */
    type2name: Record<string, string>;
  }>({ nodes: [], label: () => '', raw: new Map(), allIDs: [], parentOf: new Map(), type2name: {} });
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [selection, setSelection] = useState<Selection>(null);
  const [racks, setRacks] = useState<SearchRow[]>([]);
  const [racksLoading, setRacksLoading] = useState(false);
  const [rackQuery, setRackQuery] = useState('');
  const [history, setHistory] = useState<RackHistory | null>(null);
  const [creating, setCreating] = useState<{ type: DCIMType; parent?: DCIMNode } | null>(null);
  const [openRack, setOpenRack] = useState<{ rack: DCIMNode; roomID: number } | null>(null);
  const [renaming, setRenaming] = useState<{ type: DCIMType; node: DCIMNode; labelAttr: string } | null>(null);

  const load = useCallback(
    async (quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        const got = await getDCIMTree();
        const raw = new Map<string, DCIMNode>();
        const labelOf = (node: DCIMNode): string => {
          // `type2name` IS THE TREE'S LABELLING CONTRACT: each model may name its
          // own attribute, so the tree cannot assume `name` exists.
          const attribute = got.type2name[String(node._type)] ?? 'name';
          const value = node[attribute];
          const fallback = node.name ?? node.alias ?? node.code;
          return displayValue(value ?? fallback ?? `#${node._id}`);
        };
        const parentOf = new Map<string, string>();
        const convert = (node: DCIMNode): TreeNode => {
          raw.set(String(node._id), node);
          const children = (node.children ?? []).map((child) => {
            parentOf.set(String(child._id), String(node._id));
            return convert(child);
          });
          return {
            id: String(node._id),
            label: labelOf(node),
            trailing:
              node.rack_count !== undefined ? (
                <Chip dense>{tr(`${node.rack_count} 个机柜`, `${node.rack_count} racks`)}</Chip>
              ) : undefined,
            children,
            hasChildren: children.length > 0 || node.ci_type === 'dcim_region' || node.ci_type === 'dcim_idc',
          };
        };
        // THE WHOLE TREE OPENS BY DEFAULT. A data centre is a handful of regions
        // and rooms; collapsing it means every visit starts with clicks that
        // reveal nothing new.
        setTree({
          nodes: got.result.map(convert),
          label: labelOf,
          raw,
          allIDs: [...raw.keys()],
          parentOf,
          type2name: got.type2name ?? {},
        });
      } catch (error) {
        setErr(describeError(error, tr('无法读取数据中心', 'Could not load the data centre')));
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [tr],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const loadRacks = useCallback(
    async (room: DCIMNode, q?: string) => {
      setRacksLoading(true);
      try {
        const got = await roomRacks(room._id, q);
        setRacks(got.result ?? []);
      } catch (error) {
        toast.error(describeError(error, tr('无法读取机柜', 'Could not load the racks')));
        setRacks([]);
      } finally {
        setRacksLoading(false);
      }
    },
    [toast, tr],
  );

  const loadHistory = useCallback(
    async (rackID?: number) => {
      try {
        setHistory(await rackHistory({ rack_id: rackID, count: 20 }));
      } catch {
        setHistory(null);
      }
    },
    [],
  );

  useEffect(() => {
    if (selection?.kind === 'room') {
      void loadRacks(selection.node, rackQuery);
      void loadHistory();
    }
    if (selection?.kind === 'rack') {
      void loadHistory(selection.node._id);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selection]);

  const removeNode = async (node: DCIMNode) => {
    const kind = node.ci_type.replace(/^dcim_/, '');
    const ok = await confirmAction(
      tr(
        `删除「${tree.label(node)}」？按机柜删除会把里面的设备一起下架。`,
        `Delete “${tree.label(node)}”? Deleting a rack unmounts the devices inside it.`,
      ),
    );
    if (!ok) return;
    try {
      await deleteDCIMNode(kind as DCIMType, node._id);
      toast.success(tr('已删除', 'Deleted'));
      setSelection(null);
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const recalc = async () => {
    try {
      await recalcRackUnits();
      toast.success(tr('已重算机柜容量', 'Rack capacities recomputed'));
      if (selection?.kind === 'room') await loadRacks(selection.node, rackQuery);
    } catch (error) {
      toast.error(describeError(error, tr('重算失败', 'Could not recompute')));
    }
  };

  const rackColumns: Column<SearchRow>[] = [
    {
      key: 'name',
      header: tr('机柜', 'Rack'),
      width: 'w-full',
      cell: (row) => <span className="truncate font-medium text-text">{displayValue(row.name ?? row.rack_name ?? row._id)}</span>,
    },
    {
      key: 'units',
      header: tr('高度', 'Height'),
      cell: (row) => <span className="whitespace-nowrap text-xs text-text-muted">{displayValue(row.u_count)}U</span>,
    },
    {
      key: 'free',
      header: tr('剩余', 'Free'),
      cell: (row) => {
        const free = Number(row.free_u_count);
        const total = Number(row.u_count);
        // A RACK AT OR BELOW ZERO IS OVERSUBSCRIBED, which is a state the server
        // can produce (a device taller than the rack) and the console has to make
        // visible rather than clamp to zero.
        const tone = !Number.isFinite(free) ? 'default' : free <= 0 ? 'danger' : free / (total || 1) < 0.2 ? 'warning' : 'success';
        return (
          <Chip tone={tone as 'default' | 'danger' | 'warning' | 'success'}>
            {Number.isFinite(free) ? `${free}U` : '—'}
          </Chip>
        );
      },
    },
    {
      key: 'devices',
      header: tr('设备', 'Devices'),
      hideable: true,
      cell: (row) => <span className="text-xs text-text-muted">{displayValue(row.device_count ?? '—')}</span>,
    },
    {
      key: 'overlap',
      header: tr('重叠', 'Overlap'),
      hideable: true,
      cell: (row) => (row.is_overlap ? <Chip tone="danger">{tr('有', 'Yes')}</Chip> : <span className="text-text-faint">—</span>),
    },
  ];

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('数据中心', 'Data centre')}
        subtitle={tr(
          '区域 → 数据中心 → 机房 → 机柜。机柜里放设备，容量由服务端在同一个事务里重算。',
          'Region → data centre → room → rack. Devices live in racks, and the capacity is recomputed server-side in the same transaction.',
        )}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              type="button"
              onClick={() => void load(true)}
              disabled={loading || refreshing}
              className="inline-flex items-center gap-1.5"
            >
              <RefreshCw size={12} className={refreshing ? 'animate-spin' : undefined} aria-hidden="true" />
              {tr('刷新', 'Refresh')}
            </Button>
            <Button variant="outline" size="sm" type="button" onClick={() => void recalc()} className="inline-flex items-center gap-1.5">
              {tr('重算容量', 'Recompute')}
            </Button>
            <Button
              variant="primary"
              size="sm"
              type="button"
              onClick={() => setCreating({ type: 'region' })}
              className="inline-flex items-center gap-1.5 font-medium text-accent-fg"
            >
              <Plus size={12} aria-hidden="true" />
              {tr('新建', 'New')}
            </Button>
          </div>
        }
      />

      <div className="grid min-h-0 w-full min-w-0 flex-1 grid-cols-1 items-start gap-4 overflow-y-auto p-6 lg:grid-cols-[18rem_minmax(0,1fr)]">
        <Card className="min-w-0">
          <div className="mb-2 flex items-center justify-between gap-2">
            <h2 className="text-sm font-medium text-text">{tr('存放位置', 'Locations')}</h2>
            <Button variant="subtle" size="sm" type="button" onClick={() => void load(true)} aria-label={tr('刷新树', 'Reload the tree')}>
              <RefreshCw size={12} aria-hidden="true" />
            </Button>
          </div>
          {err && (
            <div role="alert" className="mb-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
              {err}
            </div>
          )}
          {loading ? (
            <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
          ) : tree.nodes.length === 0 ? (
            <p className="py-6 text-center text-xs text-text-muted">
              {tr('还没有区域。先建一个区域，再往下建数据中心和机房。', 'No regions yet. Create one, then the data centres and rooms under it.')}
            </p>
          ) : (
            <Tree
              nodes={tree.nodes}
              label={tr('数据中心树', 'Data centre tree')}
              defaultExpanded={tree.allIDs}
              selectedId={selection?.kind === 'room' ? String(selection.node._id) : undefined}
              onSelect={(node) => {
                const raw = tree.raw.get(node.id);
                if (!raw) return;
                if (raw.ci_type === 'dcim_server_room') {
                  setSelection({ kind: 'room', node: raw });
                  setOpenRack(null);
                } else if (raw.ci_type === 'dcim_rack') {
                  // A RACK IN THE TREE STILL NEEDS ITS ROOM: the device endpoints
                  // are addressed by room and rack, because a rack id alone does
                  // not say which room's copy of it is meant.
                  setOpenRack({ rack: raw, roomID: resolveRoom(tree.parentOf, tree.raw, node.id) });
                  void loadHistory(raw._id);
                }
              }}
            />
          )}
        </Card>

        <div className="flex min-w-0 flex-col gap-4">
          {selection?.kind === 'room' ? (
            <>
              <Card className="min-w-0">
                <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <h2 className="truncate text-sm font-medium text-text">{tree.label(selection.node)}</h2>
                    <Chip>{tr(`${racks.length} 个机柜`, `${racks.length} racks`)}</Chip>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <Input
                      type="search"
                      aria-label={tr('筛选机柜', 'Filter racks')}
                      placeholder={tr('筛选机柜', 'Filter racks')}
                      value={rackQuery}
                      onChange={(event) => {
                        setRackQuery(event.target.value);
                        void loadRacks(selection.node, event.target.value);
                      }}
                      className="w-40"
                    />
                    <Button variant="outline" size="sm" type="button" onClick={() => setCreating({ type: 'rack', parent: selection.node })}>
                      {tr('新建机柜', 'New rack')}
                    </Button>
                    <Button
                      variant="subtle"
                      size="sm"
                      type="button"
                      onClick={() =>
                        setRenaming({
                          type: 'server_room',
                          node: selection.node,
                          labelAttr: tree.type2name[String(selection.node._type)] ?? 'name',
                        })
                      }
                      aria-label={tr(`重命名 ${tree.label(selection.node)}`, `Rename ${tree.label(selection.node)}`)}
                      className="inline-flex items-center gap-1"
                    >
                      <Pencil size={12} aria-hidden="true" />
                      {tr('重命名', 'Rename')}
                    </Button>
                    <Button
                      variant="dangerGhost"
                      size="sm"
                      type="button"
                      onClick={() => void removeNode(selection.node)}
                      aria-label={tr(`删除 ${tree.label(selection.node)}`, `Delete ${tree.label(selection.node)}`)}
                    >
                      <Trash2 size={12} aria-hidden="true" />
                    </Button>
                  </div>
                </div>
                <DataTable
                  columns={rackColumns}
                  rows={racks}
                  rowKey={(row) => row._id}
                  loading={racksLoading}
                  storageKey="dcim-racks"
                  empty={tr('这个机房还没有机柜', 'This room has no racks yet')}
                  actions={(row) => (
                    <Button
                      variant="subtle"
                      size="sm"
                      type="button"
                      onClick={() =>
                        // A RACK ROW IS NOT IN THE TREE — the tree stops at rooms
                        // and reports rack counts — so the node is built from the
                        // row, and the room is the one whose table this is.
                        setOpenRack({
                          rack: tree.raw.get(String(row._id)) ?? {
                            ...row,
                            ci_type: row.ci_type || 'dcim_rack',
                            ci_type_alias: row.ci_type_alias ?? 'rack',
                          },
                          roomID: selection.node._id,
                        })
                      }
                      aria-label={tr(`打开机柜 ${displayValue(row.name ?? row._id)}`, `Open rack ${displayValue(row.name ?? row._id)}`)}
                    >
                      {tr('设备', 'Devices')}
                    </Button>
                  )}
                />
              </Card>

              <Card className="min-w-0">
                <h2 className="mb-2 text-sm font-medium text-text">{tr('最近操作', 'Recent operations')}</h2>
                <HistoryTable history={history} label={tr('还没有操作记录', 'No operations yet')} />
              </Card>
            </>
          ) : (
            <Card className="min-w-0">
              <p className="py-10 text-center text-xs text-text-muted">
                {tr('选一个机房查看它的机柜。', 'Pick a room to see its racks.')}
              </p>
              <p className="text-center text-[11px] text-text-faint">
                {tr('机柜里放设备在机柜抽屉里做。', 'Adding a device to a rack happens in the rack’s drawer.')}
              </p>
            </Card>
          )}
        </div>
      </div>

      {creating && (
        <CreateNodeModal
          type={creating.type}
          parent={creating.parent}
          onClose={() => setCreating(null)}
          onSaved={async (message) => {
            setCreating(null);
            toast.success(message);
            await load(true);
          }}
        />
      )}

      {openRack && (
        <RackDrawer
          rack={openRack.rack}
          roomID={openRack.roomID}
          labelAttr={tree.type2name[String(openRack.rack._type)] ?? 'name'}
          onClose={() => setOpenRack(null)}
          onChanged={async () => {
            if (selection?.kind === 'room') await loadRacks(selection.node, rackQuery);
            await loadHistory(openRack.rack._id);
          }}
        />
      )}

      {renaming && (
        <RenameNodeModal
          type={renaming.type}
          node={renaming.node}
          labelAttr={renaming.labelAttr}
          onClose={() => setRenaming(null)}
          onSaved={async (message) => {
            setRenaming(null);
            toast.success(message);
            await load(true);
          }}
        />
      )}

      {dialog}
    </div>
  );
}

function HistoryTable({ history, label }: { history: RackHistory | null; label: string }) {
  if (!history || history.result.length === 0) {
    return <p className="py-4 text-center text-xs text-text-muted">{label}</p>;
  }
  return (
    <ul className="divide-y divide-border">
      {history.result.slice(0, 10).map((record) => {
        // THE RECORD NAMES THE INSTANCE AND THE LOG CARRIES ITS LABEL: a rename
        // does not rewrite history, and a deleted device still leaves a trace.
        const device = record.ci_id ? history.id2ci?.[String(record.ci_id)] : undefined;
        const named = device?.name ?? device?.hostname ?? device?.alias;
        return (
          <li key={record._id} className="flex flex-wrap items-center gap-2 py-1.5 text-xs">
            <Chip tone={record.operate_type === 'remove' ? 'warning' : record.operate_type === 'move' ? 'info' : 'success'}>
              {record.operate_type}
            </Chip>
            <span className="min-w-0 flex-1 truncate text-text-muted">
              {named ? displayValue(named) : record.ci_id ? `#${record.ci_id}` : '—'}
            </span>
            <span className="whitespace-nowrap text-[11px] text-text-faint">{record.created_at}</span>
          </li>
        );
      })}
    </ul>
  );
}

/** Walks the containment up to the room a rack lives in. */
function resolveRoom(parentOf: Map<string, string>, raw: Map<string, DCIMNode>, rackID: string): number {
  let current = parentOf.get(rackID);
  for (let guard = 0; guard < 16 && current; guard += 1) {
    const node = raw.get(current);
    if (!node) break;
    if (node.ci_type === 'dcim_server_room') return node._id;
    current = parentOf.get(current);
  }
  // A RACK WITH NO ROOM IN THE TREE CANNOT BE ADDRESSED BY THE DEVICE
  // ENDPOINTS, so the rack itself is used — the server will refuse it and say
  // why, which is better than the page guessing.
  return Number(rackID);
}

// --- create a node ---

const TYPE_ORDER: DCIMType[] = ['region', 'idc', 'server_room', 'rack'];

function CreateNodeModal({
  type,
  parent,
  onClose,
  onSaved,
}: {
  type: DCIMType;
  parent?: DCIMNode;
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [chosen, setChosen] = useState<DCIMType>(type);
  const [name, setName] = useState('');
  const [code, setCode] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    if (!name.trim()) {
      setErr(tr('名称不能为空', 'A name is required'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      await createDCIMNode(chosen, {
        parent_id: parent?._id,
        // THE VALUES ARE THE MODEL'S ATTRIBUTES, and every one of these four
        // models is keyed by `name`.
        values: { name: name.trim(), ...(code.trim() ? { code: code.trim() } : {}) },
      });
      await onSaved(tr('已创建', 'Created'));
    } catch (error) {
      setErr(describeError(error, tr('创建失败', 'Could not create it')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('新建存放位置', 'New location')}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting}>
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      {err && (
        <div role="alert" className="mb-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}
      <div className="flex flex-col gap-3">
        <div>
          <Label htmlFor="cmdb-dcim-type">{tr('层级', 'Level')}</Label>
          <Select
            id="cmdb-dcim-type"
            label={tr('层级', 'Level')}
            value={chosen}
            onValueChange={(next) => setChosen(next as DCIMType)}
            // A CHILD IS ALWAYS ONE LEVEL BELOW ITS PARENT, so the picker is
            // narrowed rather than letting the operator build an impossible
            // nesting the server would refuse.
            options={TYPE_ORDER.filter((candidate) => (parent ? TYPE_ORDER.indexOf(candidate) === TYPE_ORDER.indexOf(parent.ci_type.replace('dcim_', '') as DCIMType) + 1 : true)).map(
              (candidate) => ({
                value: candidate,
                label: tr(
                  { region: '区域', idc: '数据中心', server_room: '机房', rack: '机柜' }[candidate],
                  { region: 'Region', idc: 'Data centre', server_room: 'Room', rack: 'Rack' }[candidate],
                ),
              }),
            )}
            className="mt-1 w-48"
          />
          {parent && (
            <p className="mt-1 text-[11px] text-text-faint">
              {tr(`将建在「${displayValue(parent.name ?? parent._id)}」下面`, `Created under “${displayValue(parent.name ?? parent._id)}”`)}
            </p>
          )}
        </div>
        <div>
          <Label htmlFor="cmdb-dcim-name">{tr('名称', 'Name')}</Label>
          <Input id="cmdb-dcim-name" value={name} onChange={(event) => setName(event.target.value)} className="mt-1 w-full" />
        </div>
        <div>
          <Label htmlFor="cmdb-dcim-code">{tr('编码（可选）', 'Code (optional)')}</Label>
          <Input id="cmdb-dcim-code" value={code} onChange={(event) => setCode(event.target.value)} className="mt-1 w-full font-mono" />
        </div>
      </div>
    </Modal>
  );
}

// --- one rack's contents ---

function RackDrawer({
  rack,
  roomID,
  labelAttr,
  onClose,
  onChanged,
}: {
  rack: DCIMNode;
  roomID: number;
  labelAttr: string;
  onClose(): void;
  onChanged(): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [devices, setDevices] = useState<SearchRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [models, setModels] = useState<CIType[]>([]);
  const [mountOpen, setMountOpen] = useState(false);
  const [moving, setMoving] = useState<SearchRow | null>(null);
  const [renameOpen, setRenameOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      // THE WALK GIVES THE DEVICES WITH THEIR VALUES, so a label needs no second
      // request — the same trick the hierarchy tree uses.
      const got = await searchHierarchy({ rootIDs: [rack._id], levels: [1], count: 200 });
      setDevices(got.result ?? []);
    } catch (error) {
      toast.error(describeError(error, tr('无法读取机柜内容', 'Could not load the rack’s contents')));
      setDevices([]);
    } finally {
      setLoading(false);
    }
  }, [rack._id, toast, tr]);

  useEffect(() => {
    void load();
    void listCITypes()
      .then(setModels)
      .catch(() => setModels([]));
  }, [load]);

  const unmount = async (row: SearchRow) => {
    const ok = await confirmAction(
      tr(`把「${displayValue(row.name ?? row._id)}」从机柜上取下来？`, `Unmount “${displayValue(row.name ?? row._id)}” from the rack?`),
    );
    if (!ok) return;
    try {
      await unmountDevice(roomID, rack._id, row._id);
      toast.success(tr('已下架', 'Unmounted'));
      await load();
      await onChanged();
    } catch (error) {
      toast.error(describeError(error, tr('下架失败', 'Could not unmount it')));
    }
  };

  return (
    <Drawer
      open
      onClose={onClose}
      size="lg"
      title={displayValue(rack.name ?? rack._id)}
      description={tr(`${devices.length} 台设备`, `${devices.length} devices`)}
      footer={
        <div className="flex flex-wrap justify-end gap-2">
          <Button
            variant="subtle"
            type="button"
            onClick={() => setRenameOpen(true)}
            aria-label={tr('重命名机柜', 'Rename the rack')}
            className="inline-flex items-center gap-1.5"
          >
            <Pencil size={12} aria-hidden="true" />
            {tr('重命名', 'Rename')}
          </Button>
          <Button variant="outline" type="button" onClick={onClose}>
            {tr('关闭', 'Close')}
          </Button>
          <Button variant="primary" type="button" onClick={() => setMountOpen(true)} className="inline-flex items-center gap-1.5">
            <Plus size={12} aria-hidden="true" />
            {tr('上架设备', 'Mount a device')}
          </Button>
        </div>
      }
    >
      {loading ? (
        <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
      ) : devices.length === 0 ? (
        <p className="py-6 text-center text-xs text-text-muted">{tr('这个机柜是空的', 'This rack is empty')}</p>
      ) : (
        <ul className="divide-y divide-border">
          {devices.map((device) => (
            <li key={device._id} className="flex items-center gap-2 py-2 text-xs">
              <span className="min-w-0 flex-1 truncate">{displayValue(device.name ?? device.hostname ?? device._id)}</span>
              <Chip tone="info">{device.ci_type_alias || device.ci_type}</Chip>
              <Button
                variant="subtle"
                size="sm"
                type="button"
                onClick={() => setMoving(device)}
                aria-label={tr(`移动 ${displayValue(device.name ?? device._id)}`, `Move ${displayValue(device.name ?? device._id)}`)}
              >
                {tr('移动', 'Move')}
              </Button>
              <Button
                variant="dangerGhost"
                size="sm"
                type="button"
                onClick={() => void unmount(device)}
                aria-label={tr(`下架 ${displayValue(device.name ?? device._id)}`, `Unmount ${displayValue(device.name ?? device._id)}`)}
              >
                {tr('下架', 'Unmount')}
              </Button>
            </li>
          ))}
        </ul>
      )}

      {mountOpen && (
        <MountModal
          rack={rack}
          roomID={roomID}
          models={models}
          onClose={() => setMountOpen(false)}
          onMounted={async (message) => {
            setMountOpen(false);
            toast.success(message);
            await load();
            await onChanged();
          }}
        />
      )}

      {moving && (
        <MoveDeviceModal
          roomID={roomID}
          rack={rack}
          device={moving}
          onClose={() => setMoving(null)}
          onMoved={async (message) => {
            setMoving(null);
            toast.success(message);
            await load();
            await onChanged();
          }}
        />
      )}

      {renameOpen && (
        <RenameNodeModal
          type="rack"
          node={rack}
          labelAttr={labelAttr}
          onClose={() => setRenameOpen(false)}
          onSaved={async (message) => {
            setRenameOpen(false);
            toast.success(message);
            await onChanged();
            onClose();
          }}
        />
      )}

      {dialog}
    </Drawer>
  );
}

// --- rename a DCIM node ---

function RenameNodeModal({
  type,
  node,
  labelAttr,
  onClose,
  onSaved,
}: {
  type: DCIMType;
  node: DCIMNode;
  labelAttr: string;
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState(displayValue(node[labelAttr] ?? node.name ?? ''));
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    if (!name.trim()) {
      setErr(tr('名称不能为空', 'A name is required'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      await updateDCIMNode(type, node._id, { [labelAttr]: name.trim() });
      await onSaved(tr('已重命名', 'Renamed'));
    } catch (error) {
      setErr(describeError(error, tr('重命名失败', 'Could not rename it')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('重命名', 'Rename')}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting}>
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      {err && (
        <div role="alert" className="mb-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}
      <Label htmlFor="dcim-rename">{tr('新名称', 'New name')}</Label>
      <Input id="dcim-rename" aria-label={tr('新名称', 'New name')} value={name} onChange={(event) => setName(event.target.value)} className="mt-1 w-full" />
    </Modal>
  );
}

// --- move a device ---

function MoveDeviceModal({
  roomID,
  rack,
  device,
  onClose,
  onMoved,
}: {
  roomID: number;
  rack: DCIMNode;
  device: SearchRow;
  onClose(): void;
  onMoved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [racks, setRacks] = useState<SearchRow[]>([]);
  const [target, setTarget] = useState(String(rack._id));
  const startValue = Number(device.u_start);
  const [start, setStart] = useState(Number.isFinite(startValue) && startValue > 0 ? String(startValue) : '');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    void roomRacks(roomID)
      .then((got) => setRacks(got.result ?? []))
      .catch(() => setRacks([]));
  }, [roomID]);

  const submit = async () => {
    const startUnit = Number(start);
    if (!Number.isInteger(startUnit) || startUnit <= 0) {
      setErr(tr('目标起始 U 位必须是正整数', 'The target start unit must be a positive whole number'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      await moveDevice(roomID, rack._id, device._id, {
        to_u_start: startUnit,
        ...(Number(target) !== rack._id ? { to_rack_id: Number(target) } : {}),
      });
      await onMoved(tr('已移动', 'Moved'));
    } catch (error) {
      setErr(describeError(error, tr('移动失败', 'Could not move it')));
    } finally {
      setSubmitting(false);
    }
  };

  const options = [
    { value: String(rack._id), label: displayValue(rack.name ?? rack._id) },
    ...racks
      .filter((row) => row._id !== rack._id)
      .map((row) => ({ value: String(row._id), label: displayValue(row.name ?? row._id) })),
  ];

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('移动设备', 'Move a device')}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting}>
            {submitting ? tr('移动中…', 'Moving…') : tr('移动', 'Move')}
          </Button>
        </div>
      }
    >
      {err && (
        <div role="alert" className="mb-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}
      <div className="flex flex-col gap-3">
        <div>
          <Label htmlFor="dcim-move-rack">{tr('目标机柜', 'Target rack')}</Label>
          <Select
            id="dcim-move-rack"
            label={tr('目标机柜', 'Target rack')}
            value={target}
            onValueChange={setTarget}
            options={options}
            className="mt-1 w-full"
          />
        </div>
        <div>
          <Label htmlFor="dcim-move-start">{tr('目标起始 U 位', 'Target start unit')}</Label>
          <Input
            id="dcim-move-start"
            type="number"
            min={1}
            value={start}
            onChange={(event) => setStart(event.target.value)}
            className="mt-1 w-32"
          />
        </div>
      </div>
    </Modal>
  );
}

function MountModal({
  rack,
  roomID,
  models,
  onClose,
  onMounted,
}: {
  rack: DCIMNode;
  roomID: number;
  models: CIType[];
  onClose(): void;
  onMounted(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [typeRef, setTypeRef] = useState(models[0]?.name ?? '');
  const [instances, setInstances] = useState<Array<{ id: number; label: string }>>([]);
  const [deviceID, setDeviceID] = useState('');
  const [start, setStart] = useState('1');
  const [units, setUnits] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!typeRef) return;
    const model = models.find((item) => item.name === typeRef);
    if (!model) return;
    let live = true;
    void listInstances(model.id, { count: 200 })
      .then((got) => {
        if (!live) return;
        setInstances(
          (got.items ?? []).map((item) => ({
            id: item.ci_id,
            label: displayValue(item.values?.[model.unique_key ?? ''] ?? item.values?.name ?? item.ci_id),
          })),
        );
      })
      .catch(() => live && setInstances([]));
    return () => {
      live = false;
    };
  }, [typeRef, models]);

  const submit = async () => {
    const device = Number(deviceID);
    const position = Number(start);
    if (!device) return setErr(tr('先选一台设备', 'Pick a device first'));
    if (!Number.isFinite(position) || position < 0) return setErr(tr('起始 U 位要先定', 'A start unit is required'));

    setSubmitting(true);
    setErr(null);
    try {
      const got = await mountDevice(roomID, rack._id, device, {
        u_start: position,
        // AN EMPTY BOX IS "DO NOT MENTION", not zero: the server reads a missing
        // height as "leave the device's own height alone", and zero as the
        // default. Sending nothing is what the operator means.
        ...(units.trim() === '' ? {} : { u_count: Number(units) }),
      });
      await onMounted(tr(`已上架，机柜剩余 ${got.free_u_count}U`, `Mounted; ${got.free_u_count}U free in the rack`));
    } catch (error) {
      // THE SERVER'S REFUSAL NAMES THE ARITHMETIC (too tall for the rack, the
      // unit is taken), which is exactly what the operator needs.
      setErr(describeError(error, tr('上架失败', 'Could not mount it')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('上架设备', 'Mount a device')}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting}>
            {submitting ? tr('上架中…', 'Mounting…') : tr('上架', 'Mount')}
          </Button>
        </div>
      }
    >
      {err && (
        <div role="alert" className="mb-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}
      <div className="flex flex-col gap-3">
        <div>
          <Label htmlFor="cmdb-mount-model">{tr('设备模型', 'Device model')}</Label>
          <Select
            id="cmdb-mount-model"
            label={tr('设备模型', 'Device model')}
            value={typeRef}
            onValueChange={setTypeRef}
            options={models.map((model) => ({ value: model.name, label: model.alias || model.name }))}
            searchable
            className="mt-1 w-full"
          />
        </div>
        <div>
          <Label htmlFor="cmdb-mount-device">{tr('设备', 'Device')}</Label>
          <Select
            id="cmdb-mount-device"
            label={tr('设备', 'Device')}
            value={deviceID}
            onValueChange={setDeviceID}
            options={instances.map((item) => ({ value: String(item.id), label: item.label }))}
            searchable
            className="mt-1 w-full"
          />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label htmlFor="cmdb-mount-start">{tr('起始 U 位', 'Start unit')}</Label>
            <Input
              id="cmdb-mount-start"
              type="number"
              min={1}
              value={start}
              onChange={(event) => setStart(event.target.value)}
              className="mt-1 w-full"
            />
          </div>
          <div>
            <Label htmlFor="cmdb-mount-units">{tr('高度（留空用设备自身）', 'Height (blank = the device’s)')}</Label>
            <Input
              id="cmdb-mount-units"
              type="number"
              min={1}
              value={units}
              onChange={(event) => setUnits(event.target.value)}
              className="mt-1 w-full"
            />
          </div>
        </div>
        <p className="text-[11px] text-text-faint">
          {tr(
            '容量由服务端在事务里重算：装不下、位置被占、或设备已在别的机柜里，都会被拒绝并说明原因。',
            'The server recomputes capacity in the transaction: too tall, position taken, or already in another rack are all refused with a reason.',
          )}
          <Link to="/cmdb/instances" className="ml-1 text-indigo-400 hover:text-indigo-300">
            {tr('找不到设备？', 'Missing a device?')}
          </Link>
        </p>
      </div>
    </Modal>
  );
}
