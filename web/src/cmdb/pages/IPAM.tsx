import { useCallback, useEffect, useMemo, useState } from 'react';
import { Pencil, Plus, Radar, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { PageHeader } from '@/components/ui/PageHeader';
import { PaginationFooter } from '@/components/ui/PaginationFooter';
import { Select } from '@/components/ui/Select';
import { Switch } from '@/components/ui/Switch';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/Tabs';
import { Textarea } from '@/components/ui/Textarea';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';

import {
  ADDRESS_ASSIGNED,
  ADDRESS_RESERVED,
  ADDRESS_UNASSIGNED,
  assignAddresses,
  createIPScope,
  createSubnet,
  deleteIPScope,
  deleteSubnet,
  getIPAMTree,
  getSubnet,
  ipamHistory,
  ipamScanHistory,
  ipamStats,
  isSubnet,
  listAddresses,
  moveSubnet,
  subnetHosts,
  updateSubnet,
  type IPAMHistoryRecord,
  type IPAMNode,
  type IPAMScanRecord,
  type SubnetDetail,
  type SubnetSummary,
} from '../api/ipam';
import { describeError } from '../api/http';
import type { SearchRow } from '../api/instance';
import { DataTable, type Column } from '../components/DataTable';
import { Tree, type TreeNode } from '../components/Tree';
import { useToast } from '../components/Toast';
import { displayValue } from '../lib/valuetype';

// IPAM — the address space, the ranges in it, and the addresses in them.
//
// THE TREE IS THREE ORDINARY MODELS. `ipam_scope` groups, `ipam_subnet` holds a
// range, `ipam_address` is an address — all instances like any other, joined by
// ordinary relations. The page therefore reads one tree endpoint rather than
// three lists, and a deployment with none of those models gets an empty tree
// and a link to create them rather than a 404.
//
// NOTHING HERE DOES ARITHMETIC. A subnet's host count is a function of its
// CIDR, and its assigned/used/free counts are recomputed in the transaction
// that changes its addresses. This page draws what the server reports and asks
// it to change things; the only numbers it adds up are the headline tiles,
// which the server also sums — see §6.m of the plan for why the totals are
// summed over LEAF subnets.
//
// THE SCAN RULE IS NOT AN ATTRIBUTE. It lives in its own table and is written
// through the subnet's own endpoint, which is why the form carries three fields
// that never appear in the subnet's value list.

type Tab = 'overview' | 'addresses' | 'scan' | 'log';

type Selection = { node: IPAMNode } | null;

type TreeState = {
  nodes: TreeNode[];
  raw: Map<string, IPAMNode>;
  /** child id → parent id, so a subnet edit can revalidate against its siblings. */
  parentOf: Map<string, string>;
  /** The nodes rendered open: the roots and every grouping. */
  openIDs: string[];
  label: (node: IPAMNode) => string;
};

const PAGE_SIZE = 50;

const EMPTY_TREE: TreeState = {
  nodes: [],
  raw: new Map(),
  parentOf: new Map(),
  openIDs: [],
  label: () => '',
};

export default function IPAMPage() {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [tree, setTree] = useState<TreeState>(EMPTY_TREE);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [selection, setSelection] = useState<Selection>(null);
  const [tab, setTab] = useState<Tab>('overview');

  const [summary, setSummary] = useState<SubnetSummary | null>(null);
  const [summaryLoading, setSummaryLoading] = useState(false);

  const [addresses, setAddresses] = useState<SearchRow[]>([]);
  const [addressTotal, setAddressTotal] = useState(0);
  const [addressPage, setAddressPage] = useState(0);
  const [addressLoading, setAddressLoading] = useState(false);

  const [scans, setScans] = useState<IPAMScanRecord[]>([]);
  const [scanTotal, setScanTotal] = useState(0);
  const [scanPage, setScanPage] = useState(0);
  const [scanDetail, setScanDetail] = useState<SubnetDetail | null>(null);
  const [scanDetailLoading, setScanDetailLoading] = useState(false);

  const [operations, setOperations] = useState<IPAMHistoryRecord[]>([]);
  const [operationTotal, setOperationTotal] = useState(0);
  const [operationPage, setOperationPage] = useState(0);

  const [tick, setTick] = useState(0);
  const bump = useCallback(() => setTick((value) => value + 1), []);

  const [creating, setCreating] = useState<{ parent?: IPAMNode } | null>(null);
  const [editing, setEditing] = useState<IPAMNode | null>(null);
  const [moving, setMoving] = useState<IPAMNode | null>(null);
  const [assigning, setAssigning] = useState<IPAMNode | null>(null);

  const selectedID = selection?.node._id;
  const selectedIsSubnet = isSubnet(selection?.node);

  // --- the tree ---

  const load = useCallback(
    async (quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        const got = await getIPAMTree();
        const raw = new Map<string, IPAMNode>();
        const parentOf = new Map<string, string>();
        const openIDs: string[] = [];

        // `type2name` IS THE LABELLING CONTRACT: each model names its own
        // attribute, so the tree cannot assume `name` exists.
        const labelOf = (node: IPAMNode): string => {
          const attribute = got.type2name[String(node._type)] ?? 'name';
          return displayValue(node[attribute] ?? node.name ?? `#${node._id}`);
        };

        const convert = (node: IPAMNode): TreeNode => {
          raw.set(String(node._id), node);
          const children = (node.children ?? []).map((child) => {
            parentOf.set(String(child._id), String(node._id));
            return convert(child);
          });
          return {
            id: String(node._id),
            label: labelOf(node),
            trailing: isSubnet(node) ? <Chip dense>{displayValue(node.cidr)}</Chip> : undefined,
            children,
            hasChildren: children.length > 0,
          };
        };

        const nodes = (got.result ?? []).map(convert);
        // ROOTS AND GROUPINGS OPEN, NESTED RANGES DO NOT. An address tree is
        // mostly ranges; opening every one of them on every visit turns a
        // navigator into a wall of rows. A grouping is opened because that is
        // what it is for.
        for (const node of raw.values()) {
          if (!parentOf.has(String(node._id)) || !isSubnet(node)) openIDs.push(String(node._id));
        }
        setTree({ nodes, raw, parentOf, openIDs, label: labelOf });

        // A NODE THE RELOAD TOOK AWAY must not stay selected — its detail
        // requests would 404 and the panels would describe something deleted.
        setSelection((current) => {
          if (!current) return null;
          const next = raw.get(String(current.node._id));
          return next ? { node: next } : null;
        });
      } catch (error) {
        setErr(describeError(error, tr('无法读取地址空间', 'Could not load the address space')));
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

  // --- the panels ---

  useEffect(() => {
    if (tab !== 'overview') return;
    let live = true;
    setSummaryLoading(true);
    void ipamStats(selectedID)
      .then((got) => live && setSummary(got))
      .catch((error) => {
        if (!live) return;
        setSummary(null);
        toast.error(describeError(error, tr('无法读取统计', 'Could not load the summary')));
      })
      .finally(() => live && setSummaryLoading(false));
    return () => {
      live = false;
    };
  }, [tab, selectedID, tick, toast, tr]);

  useEffect(() => {
    if (tab !== 'addresses' || !selectedID || !selectedIsSubnet) return;
    let live = true;
    setAddressLoading(true);
    void listAddresses(selectedID, { page: addressPage + 1, count: PAGE_SIZE })
      .then((got) => {
        if (!live) return;
        setAddresses(got.result ?? []);
        setAddressTotal(got.numfound ?? 0);
      })
      .catch((error) => {
        if (!live) return;
        setAddresses([]);
        toast.error(describeError(error, tr('无法读取地址', 'Could not load the addresses')));
      })
      .finally(() => live && setAddressLoading(false));
    return () => {
      live = false;
    };
  }, [tab, selectedID, selectedIsSubnet, addressPage, tick, toast, tr]);

  // THE RULE IS NOT IN THE TREE. The tree carries a node's labels and its
  // range; the scan rule lives in its own table and is only reported by the
  // subnet's detail endpoint, so the scan tab reads it rather than inventing
  // an "off" from a field the tree never sent.
  useEffect(() => {
    if (tab !== 'scan' || !selectedID || !selectedIsSubnet) {
      setScanDetail(null);
      return;
    }
    let live = true;
    setScanDetailLoading(true);
    void getSubnet(selectedID)
      .then((got) => live && setScanDetail(got))
      .catch(() => live && setScanDetail(null))
      .finally(() => live && setScanDetailLoading(false));
    return () => {
      live = false;
    };
  }, [tab, selectedID, selectedIsSubnet, tick]);

  useEffect(() => {
    if (tab !== 'scan') return;
    let live = true;
    void ipamScanHistory({
      ...(selectedIsSubnet ? { cidr: selection?.node.cidr } : {}),
      page: scanPage + 1,
      count: PAGE_SIZE,
    })
      .then((got) => {
        if (!live) return;
        setScans(got.result ?? []);
        setScanTotal(got.numfound ?? 0);
      })
      .catch(() => {
        if (!live) return;
        setScans([]);
        setScanTotal(0);
      });
    return () => {
      live = false;
    };
  }, [tab, selectedID, selectedIsSubnet, selection?.node.cidr, scanPage, tick]);

  useEffect(() => {
    if (tab !== 'log') return;
    let live = true;
    void ipamHistory({
      ...(selectedIsSubnet ? { cidr: selection?.node.cidr } : {}),
      page: operationPage + 1,
      count: PAGE_SIZE,
    })
      .then((got) => {
        if (!live) return;
        setOperations(got.result ?? []);
        setOperationTotal(got.numfound ?? 0);
      })
      .catch(() => {
        if (!live) return;
        setOperations([]);
        setOperationTotal(0);
      });
    return () => {
      live = false;
    };
  }, [tab, selectedID, selectedIsSubnet, selection?.node.cidr, operationPage, tick]);

  // A NEW SELECTION IS A NEW PAGE OF EVERYTHING.
  useEffect(() => {
    setAddressPage(0);
    setScanPage(0);
    setOperationPage(0);
  }, [selectedID]);

  // --- writes ---

  const removeNode = async (node: IPAMNode) => {
    const subnet = isSubnet(node);
    const ok = await confirmAction(
      subnet
        ? tr(
            `删除网段「${tree.label(node)}」？网段里还有地址时服务端会拒绝。`,
            `Delete the range “${tree.label(node)}”? The server refuses while it still holds addresses.`,
          )
        : tr(
            `删除分组「${tree.label(node)}」？分组里还有节点时服务端会拒绝。`,
            `Delete the group “${tree.label(node)}”? The server refuses while it still has children.`,
          ),
    );
    if (!ok) return;
    try {
      if (subnet) await deleteSubnet(node._id);
      else await deleteIPScope(node._id);
      toast.success(tr('已删除', 'Deleted'));
      setSelection(null);
      bump();
    } catch (error) {
      // A REFUSAL NAMES WHAT IS STILL INSIDE, which is the thing the operator
      // has to deal with before the delete can succeed.
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  // --- columns ---

  const summaryColumns: Column<Record<string, unknown>>[] = [
    {
      key: 'name',
      header: tr('子网', 'Subnet'),
      width: 'w-full',
      cell: (row) => (
        <div className="min-w-0">
          <div className="truncate font-medium text-text">{displayValue(row.name ?? row._id)}</div>
          <div className="truncate font-mono text-[11px] text-text-muted">{displayValue(row.cidr)}</div>
        </div>
      ),
    },
    {
      key: 'hosts',
      header: tr('容量', 'Hosts'),
      cell: (row) => <span className="whitespace-nowrap text-xs text-text-muted">{displayValue(row.hosts_count ?? '—')}</span>,
    },
    {
      key: 'assigned',
      header: tr('已分配', 'Assigned'),
      cell: (row) => <span className="whitespace-nowrap text-xs text-text-muted">{displayValue(row.assign_count ?? '—')}</span>,
    },
    {
      key: 'used',
      header: tr('在用', 'In use'),
      hideable: true,
      cell: (row) => <span className="whitespace-nowrap text-xs text-text-muted">{displayValue(row.used_count ?? '—')}</span>,
    },
    {
      key: 'free',
      header: tr('可用', 'Free'),
      cell: (row) => {
        const free = Number(row.free_count);
        const hosts = Number(row.hosts_count);
        // A RANGE CAN LEGITIMATELY REPORT A NEGATIVE FREE COUNT (more observed
        // addresses than the range holds), and clamping it would hide exactly
        // the state an operator has to fix.
        const tone = !Number.isFinite(free) ? 'default' : free <= 0 ? 'danger' : hosts > 0 && free / hosts < 0.15 ? 'warning' : 'success';
        return (
          <Chip tone={tone as 'default' | 'success' | 'warning' | 'danger'}>
            {Number.isFinite(free) ? free : '—'}
          </Chip>
        );
      },
    },
    {
      key: 'scan',
      header: tr('扫描', 'Scan'),
      hideable: true,
      cell: (row) =>
        row.scan_enabled ? <Chip tone="info">{tr('开', 'On')}</Chip> : <span className="text-xs text-text-faint">{tr('关', 'Off')}</span>,
    },
    {
      key: 'last_scan',
      header: tr('最近扫描', 'Last scan'),
      hideable: true,
      defaultHidden: true,
      cell: (row) => <span className="whitespace-nowrap text-[11px] text-text-faint">{displayValue(row.last_scan_time ?? '—')}</span>,
    },
  ];

  const addressColumns: Column<SearchRow>[] = [
    {
      key: 'ip',
      header: tr('地址', 'Address'),
      width: 'w-full',
      cell: (row) => <span className="font-mono text-xs text-text">{displayValue(row.ip ?? row._id)}</span>,
    },
    {
      key: 'status',
      header: tr('分配状态', 'State'),
      cell: (row) => {
        const view = assignStatusView(row.assign_status, tr);
        return <Chip tone={view.tone}>{view.label}</Chip>;
      },
    },
    {
      key: 'used',
      header: tr('在用', 'In use'),
      cell: (row) =>
        isTruthy(row.is_used) ? <Chip tone="info">{tr('在用', 'In use')}</Chip> : <span className="text-xs text-text-faint">—</span>,
    },
    {
      key: 'updated',
      header: tr('更新时间', 'Updated'),
      hideable: true,
      defaultHidden: true,
      cell: (row) => <span className="whitespace-nowrap text-[11px] text-text-faint">{displayValue(row._updated_at ?? '—')}</span>,
    },
  ];

  const scanColumns: Column<IPAMScanRecord>[] = [
    {
      key: 'created',
      header: tr('时间', 'Time'),
      width: 'w-full',
      cell: (row) => <span className="whitespace-nowrap text-xs text-text">{row.created_at}</span>,
    },
    {
      key: 'cidr',
      header: tr('网段', 'Range'),
      cell: (row) => <span className="font-mono text-xs text-text-muted">{displayValue(row.cidr ?? '—')}</span>,
    },
    {
      key: 'status',
      header: tr('状态', 'Status'),
      cell: (row) => (
        <Chip tone={row.status === 0 ? 'success' : 'warning'}>
          {row.status === 0 ? tr('完成', 'Completed') : `${row.status}`}
        </Chip>
      ),
    },
    {
      key: 'ips',
      header: tr('地址数', 'Addresses'),
      cell: (row) => <span className="text-xs text-text-muted">{displayValue(row.ip_num ?? '—')}</span>,
    },
    {
      key: 'exec',
      header: 'exec_id',
      hideable: true,
      defaultHidden: true,
      cell: (row) => <span className="font-mono text-[11px] text-text-faint">{displayValue(row.exec_id ?? '—')}</span>,
    },
  ];

  const operationColumns: Column<IPAMHistoryRecord>[] = [
    {
      key: 'created',
      header: tr('时间', 'Time'),
      cell: (row) => <span className="whitespace-nowrap text-xs text-text">{row.created_at}</span>,
    },
    {
      key: 'operate',
      header: tr('操作', 'Operation'),
      cell: (row) => <Chip tone={operationTone(row.operate_type)}>{operationLabel(row.operate_type, tr)}</Chip>,
    },
    {
      // THE RANGE AND ITS DETAIL SHARE ONE CELL. They are one thought — "what
      // this operation touched" — and two columns made both of them too narrow
      // to read at 768px, which is where an operator from a laptop actually is.
      key: 'target',
      header: tr('网段与说明', 'Range and detail'),
      width: 'w-full',
      cell: (row) => (
        <div className="min-w-0">
          <div className="whitespace-nowrap font-mono text-xs text-text">{displayValue(row.cidr ?? '—')}</div>
          {row.description ? (
            // `break-all` RATHER THAN `break-words`: only the former lowers the
            // column's min-content width, and a table that cannot fit otherwise
            // clips the value instead of wrapping it. The values here are
            // address lists and CIDR transitions — there is no prose to protect.
            <div className="mt-0.5 break-all text-[11px] text-text-muted">{displayValue(row.description)}</div>
          ) : null}
        </div>
      ),
    },
  ];

  const overviewRows = useMemo(() => summary?.subnets ?? [], [summary]);

  // Releasing or reassigning ONE address is the same batch call with one entry,
  // which is also why it carries the same operation log line.
  const setStatus = async (row: SearchRow, status: number) => {
    if (!selectedID) return;
    try {
      const got = await assignAddresses({
        subnet_id: selectedID,
        ips: [String(row.ip)],
        assign_status: status,
      });
      toast.success(
        tr(
          `已更新 ${displayValue(row.ip)}，剩余 ${got.free_count} 个可用`,
          `Updated ${displayValue(row.ip)}; ${got.free_count} free`,
        ),
      );
      bump();
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('更新失败', 'Could not update it')));
    }
  };

  const openSubnet = (row: Record<string, unknown>) => {
    const id = Number(row._id);
    const node = tree.raw.get(String(id));
    if (node) {
      setSelection({ node });
      setTab('addresses');
      return;
    }
    // A ROW WITH NO NODE IN THE TREE means the tree is stale; a reload puts it
    // back rather than the page inventing a node that cannot be edited.
    void load(true);
  };

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('IP 地址空间', 'IP address space')}
        subtitle={tr(
          '分组 → 网段 → 地址。计数由服务端在地址变更的同一个事务里重算。',
          'Groups → ranges → addresses. The counters are recomputed server-side in the transaction that changes an address.',
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
            <Button
              variant="primary"
              size="sm"
              type="button"
              onClick={() => setCreating({ parent: selection?.node })}
              className="inline-flex items-center gap-1.5 font-medium text-accent-fg"
            >
              <Plus size={12} aria-hidden="true" />
              {tr('新建', 'New')}
            </Button>
          </div>
        }
      />

      <div className="grid min-h-0 w-full min-w-0 flex-1 grid-cols-1 items-start gap-4 overflow-y-auto p-6 lg:grid-cols-[20rem_minmax(0,1fr)]">
        <Card className="min-w-0">
          <div className="mb-2 flex items-center justify-between gap-2">
            <h2 className="text-sm font-medium text-text">{tr('地址空间', 'Address space')}</h2>
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
              {tr(
                '还没有地址空间。先建一个分组或直接建一个网段。',
                'No address space yet. Create a group, or a range directly.',
              )}
            </p>
          ) : (
            <Tree
              nodes={tree.nodes}
              label={tr('地址空间树', 'Address space tree')}
              defaultExpanded={tree.openIDs}
              selectedId={selectedID !== undefined ? String(selectedID) : undefined}
              onSelect={(node) => {
                const raw = tree.raw.get(node.id);
                if (raw) setSelection({ node: raw });
              }}
            />
          )}
        </Card>

        <div className="flex min-w-0 flex-col gap-4">
          <Card className="min-w-0">
            <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <h2 className="truncate text-sm font-medium text-text">
                    {selection ? tree.label(selection.node) : tr('全部网段', 'All ranges')}
                  </h2>
                  {selectedIsSubnet && <Chip tone="info">{displayValue(selection?.node.cidr)}</Chip>}
                  {selection && !selectedIsSubnet && <Chip>{tr('分组', 'Group')}</Chip>}
                </div>
                <p className="mt-1 text-[11px] text-text-faint">
                  {selection
                    ? tr('统计与日志按这个节点筛选。', 'The summary and logs are scoped to this node.')
                    : tr('没有选中节点时，统计和日志覆盖全部网段。', 'With nothing selected, the summary and logs cover every range.')}
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {selection && (
                  <>
                    <Button
                      variant="outline"
                      size="sm"
                      type="button"
                      onClick={() => setCreating({ parent: selection.node })}
                    >
                      {tr('在此新建', 'New here')}
                    </Button>
                    {selectedIsSubnet && (
                      <Button
                        variant="outline"
                        size="sm"
                        type="button"
                        onClick={() => setEditing(selection.node)}
                        className="inline-flex items-center gap-1.5"
                      >
                        <Pencil size={12} aria-hidden="true" />
                        {tr('编辑', 'Edit')}
                      </Button>
                    )}
                    {selectedIsSubnet && (
                      <Button variant="outline" size="sm" type="button" onClick={() => setMoving(selection.node)}>
                        {tr('移动', 'Move')}
                      </Button>
                    )}
                    <Button
                      variant="dangerGhost"
                      size="sm"
                      type="button"
                      onClick={() => void removeNode(selection.node)}
                      aria-label={tr(`删除 ${tree.label(selection.node)}`, `Delete ${tree.label(selection.node)}`)}
                    >
                      <Trash2 size={12} aria-hidden="true" />
                    </Button>
                  </>
                )}
              </div>
            </div>

            <Tabs value={tab} onValueChange={(value) => setTab(value as Tab)}>
              <TabsList className="mb-3">
                <TabsTrigger value="overview">{tr('概览', 'Overview')}</TabsTrigger>
                <TabsTrigger value="addresses">
                  {tr('地址', 'Addresses')}
                  {summary && <span className="ml-1.5 text-[11px] text-text-faint">{summary.address_assign_num}</span>}
                </TabsTrigger>
                <TabsTrigger value="scan">{tr('扫描', 'Scanning')}</TabsTrigger>
                <TabsTrigger value="log">{tr('日志', 'Log')}</TabsTrigger>
              </TabsList>

              <TabsContent value="overview">
                {summaryLoading ? (
                  <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
                ) : (
                  <>
                    <div className="mb-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
                      <Tile label={tr('子网', 'Subnets')} value={summary?.subnet_num ?? 0} />
                      <Tile label={tr('地址容量', 'Capacity')} value={summary?.address_num ?? 0} />
                      <Tile label={tr('已分配', 'Assigned')} value={summary?.address_assign_num ?? 0} tone="success" />
                      <Tile label={tr('可用', 'Free')} value={summary?.address_free_num ?? 0} tone="info" />
                    </div>
                    <DataTable
                      columns={summaryColumns}
                      rows={overviewRows}
                      rowKey={(row) => String(row._id)}
                      loading={summaryLoading}
                      storageKey="ipam-subnets"
                      empty={tr('这个节点下还没有网段', 'No ranges under this node yet')}
                      actions={(row) => (
                        <Button
                          variant="subtle"
                          size="sm"
                          type="button"
                          onClick={() => openSubnet(row)}
                          aria-label={tr(`打开 ${displayValue(row.name ?? row._id)}`, `Open ${displayValue(row.name ?? row._id)}`)}
                        >
                          {tr('地址', 'Addresses')}
                        </Button>
                      )}
                    />
                  </>
                )}
              </TabsContent>

              <TabsContent value="addresses">
                {!selectedIsSubnet ? (
                  <p className="py-10 text-center text-xs text-text-muted">
                    {tr('选一个网段查看它的地址。', 'Pick a range to see its addresses.')}
                  </p>
                ) : (
                  <>
                    <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
                      <span className="text-xs text-text-muted">
                        {tr(`共 ${addressTotal} 个地址`, `${addressTotal} addresses`)}
                      </span>
                      <Button
                        variant="primary"
                        size="sm"
                        type="button"
                        onClick={() => selection && setAssigning(selection.node)}
                        className="inline-flex items-center gap-1.5 font-medium text-accent-fg"
                      >
                        <Plus size={12} aria-hidden="true" />
                        {tr('分配地址', 'Assign addresses')}
                      </Button>
                    </div>
                    <DataTable
                      columns={addressColumns}
                      rows={addresses}
                      rowKey={(row) => row._id}
                      loading={addressLoading}
                      storageKey="ipam-addresses"
                      empty={tr('这个网段还没有登记地址', 'No addresses registered in this range yet')}
                      actions={(row) => {
                        const status = Number(row.assign_status);
                        return (
                          <div className="flex items-center gap-1">
                            {status !== ADDRESS_UNASSIGNED && (
                              <Button
                                variant="dangerGhost"
                                size="sm"
                                type="button"
                                onClick={() => void setStatus(row, ADDRESS_UNASSIGNED)}
                                aria-label={tr(`回收 ${displayValue(row.ip)}`, `Release ${displayValue(row.ip)}`)}
                              >
                                {tr('回收', 'Release')}
                              </Button>
                            )}
                            {status === ADDRESS_UNASSIGNED && (
                              <Button
                                variant="subtle"
                                size="sm"
                                type="button"
                                onClick={() => void setStatus(row, ADDRESS_ASSIGNED)}
                                aria-label={tr(`重新分配 ${displayValue(row.ip)}`, `Reassign ${displayValue(row.ip)}`)}
                              >
                                {tr('分配', 'Assign')}
                              </Button>
                            )}
                            {status !== ADDRESS_RESERVED && status !== ADDRESS_UNASSIGNED && (
                              <Button
                                variant="subtle"
                                size="sm"
                                type="button"
                                onClick={() => void setStatus(row, ADDRESS_RESERVED)}
                                aria-label={tr(`保留 ${displayValue(row.ip)}`, `Reserve ${displayValue(row.ip)}`)}
                              >
                                {tr('保留', 'Reserve')}
                              </Button>
                            )}
                          </div>
                        );
                      }}
                    />
                    <PaginationFooter
                      page={addressPage}
                      pageSize={PAGE_SIZE}
                      shown={addresses.length}
                      total={addressTotal}
                      loading={addressLoading}
                      onPageChange={setAddressPage}
                      className="mt-3"
                    />
                  </>
                )}
              </TabsContent>

              <TabsContent value="scan">
                {selectedIsSubnet && selection?.node ? (
                  <ScanPanel
                    detail={scanDetail}
                    loading={scanDetailLoading}
                    scans={scans}
                    total={scanTotal}
                    page={scanPage}
                    onPage={setScanPage}
                    onEdit={() => setEditing(selection.node)}
                    tr={tr}
                  />
                ) : (
                  <>
                    <p className="mb-3 text-xs text-text-muted">
                      {tr(
                        '选一个网段查看并配置它的扫描规则；下面是全部扫描记录。',
                        'Pick a range to see and configure its scan rule; every scan record is listed below.',
                      )}
                    </p>
                    <DataTable
                      columns={scanColumns}
                      rows={scans}
                      rowKey={(row) => row._id}
                      storageKey="ipam-scans"
                      empty={tr('还没有扫描记录', 'No scan records yet')}
                    />
                    <PaginationFooter
                      page={scanPage}
                      pageSize={PAGE_SIZE}
                      shown={scans.length}
                      total={scanTotal}
                      onPageChange={setScanPage}
                      className="mt-3"
                    />
                  </>
                )}
              </TabsContent>

              <TabsContent value="log">
                <DataTable
                  columns={operationColumns}
                  rows={operations}
                  rowKey={(row) => row._id}
                  storageKey="ipam-operations"
                  empty={tr('还没有操作记录', 'No operations yet')}
                />
                <PaginationFooter
                  page={operationPage}
                  pageSize={PAGE_SIZE}
                  shown={operations.length}
                  total={operationTotal}
                  onPageChange={setOperationPage}
                  className="mt-3"
                />
              </TabsContent>
            </Tabs>
          </Card>
        </div>
      </div>

      {creating && (
        <NodeModal
          parent={creating.parent}
          onClose={() => setCreating(null)}
          onSaved={async (message) => {
            setCreating(null);
            toast.success(message);
            await load(true);
            bump();
          }}
        />
      )}

      {editing && (
        <NodeModal
          subnet={editing}
          parentID={tree.parentOf.get(String(editing._id))}
          onClose={() => setEditing(null)}
          onSaved={async (message) => {
            setEditing(null);
            toast.success(message);
            await load(true);
            bump();
          }}
        />
      )}

      {moving && (
        <MoveModal
          node={moving}
          nodes={[...tree.raw.values()]}
          onClose={() => setMoving(null)}
          onMoved={async (message) => {
            setMoving(null);
            toast.success(message);
            await load(true);
            bump();
          }}
        />
      )}

      {assigning && (
        <AssignModal
          subnet={assigning}
          onClose={() => setAssigning(null)}
          onAssigned={async (message) => {
            setAssigning(null);
            toast.success(message);
            bump();
            await load(true);
          }}
        />
      )}

      {dialog}
    </div>
  );
}

// --- tiles ---

function Tile({ label, value, tone }: { label: string; value: number; tone?: 'success' | 'info' }) {
  const color = tone === 'success' ? 'text-emerald-400' : tone === 'info' ? 'text-indigo-300' : 'text-text';
  return (
    // A NAME ON THE GROUP, because "254" beside three other numbers says
    // nothing to a screen reader — the label and its number are one reading.
    <div role="group" aria-label={label} className="min-w-0 rounded-md border border-border px-3 py-2">
      <div className="truncate text-[11px] text-text-faint">{label}</div>
      <div className={`mt-0.5 text-lg font-medium tabular-nums ${color}`}>{value}</div>
    </div>
  );
}

// --- the scan tab ---

function ScanPanel({
  detail,
  loading,
  scans,
  total,
  page,
  onPage,
  onEdit,
  tr,
}: {
  detail: SubnetDetail | null;
  loading: boolean;
  scans: IPAMScanRecord[];
  total: number;
  page: number;
  onPage(page: number): void;
  onEdit(): void;
  tr: (zh: string, en: string) => string;
}) {
  const enabled = Boolean(detail?.scan_enabled);
  return (
    <>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
        {loading ? (
          <span className="text-xs text-text-muted">{tr('加载中…', 'Loading…')}</span>
        ) : (
          <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs">
            <Radar size={13} aria-hidden="true" className={enabled ? 'text-indigo-300' : 'text-text-faint'} />
            <Chip tone={enabled ? 'info' : 'default'}>{enabled ? tr('扫描已开启', 'Scan on') : tr('扫描已关闭', 'Scan off')}</Chip>
            <span className="font-mono text-text-muted">{displayValue(detail?.cron || '—')}</span>
            <span className="text-text-faint">
              {tr('执行目标', 'Target')}: {displayValue(detail?.agent_id || tr('未绑定', 'unbound'))}
            </span>
            <span className="text-text-faint">
              {tr('最近扫描', 'Last scan')}: {displayValue(detail?.last_scan_time ?? '—')}
            </span>
          </div>
        )}
        <Button variant="outline" size="sm" type="button" onClick={onEdit}>
          {tr('编辑规则', 'Edit rule')}
        </Button>
      </div>
      <DataTable
        columns={[
          {
            key: 'created',
            header: tr('时间', 'Time'),
            width: 'w-full',
            cell: (row: IPAMScanRecord) => <span className="whitespace-nowrap text-xs text-text">{row.created_at}</span>,
          },
          {
            key: 'status',
            header: tr('状态', 'Status'),
            cell: (row: IPAMScanRecord) => (
              <Chip tone={row.status === 0 ? 'success' : 'warning'}>
                {row.status === 0 ? tr('完成', 'Completed') : `${row.status}`}
              </Chip>
            ),
          },
          {
            key: 'ips',
            header: tr('地址数', 'Addresses'),
            cell: (row: IPAMScanRecord) => <span className="text-xs text-text-muted">{displayValue(row.ip_num ?? '—')}</span>,
          },
          {
            key: 'exec',
            header: 'exec_id',
            hideable: true,
            defaultHidden: true,
            cell: (row: IPAMScanRecord) => <span className="font-mono text-[11px] text-text-faint">{displayValue(row.exec_id ?? '—')}</span>,
          },
        ]}
        rows={scans}
        rowKey={(row) => row._id}
        storageKey="ipam-subnet-scans"
        empty={tr('这个网段还没有扫描记录', 'This range has not been scanned yet')}
      />
      <PaginationFooter page={page} pageSize={PAGE_SIZE} shown={scans.length} total={total} onPageChange={onPage} className="mt-3" />
    </>
  );
}

// --- create / edit ---

function NodeModal({
  parent,
  subnet,
  parentID,
  onClose,
  onSaved,
}: {
  parent?: IPAMNode;
  /** Present when editing: the node to load and write back. */
  subnet?: IPAMNode;
  parentID?: string;
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const editing = Boolean(subnet);

  // A RANGE IS THE COMMON CASE ONCE SOMETHING IS SELECTED; a group is what you
  // make at the root to put ranges in. A grouping inside a range has no meaning,
  // so that option disappears.
  const [kind, setKind] = useState<'scope' | 'subnet'>(subnet || parent ? 'subnet' : 'scope');
  const [name, setName] = useState('');
  const [cidr, setCidr] = useState('');
  const [scanEnabled, setScanEnabled] = useState(false);
  const [agentID, setAgentID] = useState('');
  const [cron, setCron] = useState('');
  const [hasRule, setHasRule] = useState(false);
  const [loading, setLoading] = useState(editing);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!subnet) return;
    let live = true;
    setLoading(true);
    void getSubnet(subnet._id)
      .then((got: SubnetDetail) => {
        if (!live) return;
        setName(String(got.name ?? ''));
        setCidr(String(got.cidr ?? ''));
        setScanEnabled(Boolean(got.scan_enabled));
        setAgentID(String(got.agent_id ?? ''));
        setCron(String(got.cron ?? ''));
        // WHETHER A RULE EXISTS decides whether a save may disable it: a range
        // that never had one must not gain a disabled one on every rename.
        setHasRule(Boolean(got.agent_id || got.cron || got.scan_enabled));
      })
      .catch((error) => live && setErr(describeError(error, tr('无法读取网段', 'Could not load the range'))))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [subnet, tr]);

  const submit = async () => {
    if (loading) return;
    if (!name.trim()) {
      setErr(tr('名称不能为空', 'A name is required'));
      return;
    }
    if (kind === 'subnet' && !cidr.trim()) {
      setErr(tr('网段不能为空', 'A range is required'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      if (editing && subnet) {
        // THE CURRENT PARENT IS SENT SO THE RANGE IS CHECKED AGAINST ITS
        // SIBLINGS. It is not a move — the moving edge has its own endpoint.
        const parent = parentID ? Number(parentID) : undefined;
        await updateSubnet(subnet._id, {
          ...(parent ? { parent_id: parent } : {}),
          cidr: cidr.trim(),
          name: name.trim(),
          ...(scanEnabled || hasRule
            ? { scan_enabled: scanEnabled, agent_id: agentID.trim(), cron: cron.trim() }
            : {}),
        });
        await onSaved(tr('已保存', 'Saved'));
      } else if (kind === 'scope') {
        await createIPScope({ parent_id: parent?._id, name: name.trim() });
        await onSaved(tr('已创建分组', 'Group created'));
      } else {
        await createSubnet({
          parent_id: parent?._id,
          cidr: cidr.trim(),
          name: name.trim(),
          // A RANGE WITHOUT SCANNING SENDS NO SCAN FIELDS AT ALL, so the server
          // writes no rule — "off" and "never configured" are different rows.
          ...(scanEnabled ? { scan_enabled: true, agent_id: agentID.trim(), cron: cron.trim() } : {}),
        });
        await onSaved(tr('已创建网段', 'Range created'));
      }
    } catch (error) {
      setErr(describeError(error, tr('保存失败', 'Could not save it')));
    } finally {
      setSubmitting(false);
    }
  };

  const title = editing
    ? tr('编辑网段', 'Edit range')
    : kind === 'scope'
      ? tr('新建分组', 'New group')
      : tr('新建网段', 'New range');

  return (
    <Modal
      open
      onClose={onClose}
      title={title}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting || loading}>
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
      {loading ? (
        <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
      ) : (
        <div className="flex flex-col gap-3">
          {!editing && (
            <div>
              <Label htmlFor="cmdb-ipam-kind">{tr('类型', 'Kind')}</Label>
              <Select
                id="cmdb-ipam-kind"
                label={tr('类型', 'Kind')}
                value={kind}
                onValueChange={(next) => setKind(next as 'scope' | 'subnet')}
                options={
                  isSubnet(parent)
                    ? [{ value: 'subnet', label: tr('网段', 'Range') }]
                    : [
                        { value: 'subnet', label: tr('网段', 'Range') },
                        { value: 'scope', label: tr('分组（不含网段）', 'Group (no range)') },
                      ]
                }
                className="mt-1 w-56"
              />
              {parent && (
                <p className="mt-1 text-[11px] text-text-faint">
                  {tr(
                    `将建在「${displayValue(parent.name ?? parent._id)}」下面`,
                    `Created under “${displayValue(parent.name ?? parent._id)}”`,
                  )}
                </p>
              )}
            </div>
          )}

          <div>
            <Label htmlFor="cmdb-ipam-name">{tr('名称', 'Name')}</Label>
            <Input id="cmdb-ipam-name" value={name} onChange={(event) => setName(event.target.value)} className="mt-1 w-full" />
          </div>

          {(kind === 'subnet' || editing) && (
            <>
              <div>
                <Label htmlFor="cmdb-ipam-cidr">{tr('网段 (CIDR)', 'Range (CIDR)')}</Label>
                <Input
                  id="cmdb-ipam-cidr"
                  value={cidr}
                  onChange={(event) => setCidr(event.target.value)}
                  placeholder="10.0.0.0/24"
                  className="mt-1 w-full font-mono"
                />
                <p className="mt-1 text-[11px] text-text-faint">
                  {tr(
                    '只接受 IPv4。网段会与同级网段校验是否重叠，改动网段会重算容量与计数。',
                    'IPv4 only. The range is checked against its siblings, and changing it recomputes the capacity and the counters.',
                  )}
                </p>
              </div>

              <div className="rounded-md border border-border px-3 py-2">
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <div className="text-xs font-medium text-text">{tr('开启扫描', 'Enable scanning')}</div>
                    <div className="text-[11px] text-text-faint">
                      {tr(
                        '扫描规则单独存储；调度由自动发现的执行端承担。',
                        'The rule is stored on its own; the schedule is driven by the discovery executor.',
                      )}
                    </div>
                  </div>
                  <Switch checked={scanEnabled} onCheckedChange={setScanEnabled} aria-label={tr('开启扫描', 'Enable scanning')} />
                </div>
                {scanEnabled && (
                  <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <div>
                      <Label htmlFor="cmdb-ipam-cron">{tr('Cron', 'Cron')}</Label>
                      <Input
                        id="cmdb-ipam-cron"
                        value={cron}
                        onChange={(event) => setCron(event.target.value)}
                        placeholder="0 3 * * *"
                        className="mt-1 w-full font-mono"
                      />
                    </div>
                    <div>
                      <Label htmlFor="cmdb-ipam-agent">{tr('执行目标', 'Execution target')}</Label>
                      <Input
                        id="cmdb-ipam-agent"
                        value={agentID}
                        onChange={(event) => setAgentID(event.target.value)}
                        placeholder="0x0000"
                        className="mt-1 w-full font-mono"
                      />
                      <p className="mt-1 text-[11px] text-text-faint">
                        {tr('0x0000 表示 master；留空表示不限。', '0x0000 means the master; blank means unbound.')}
                      </p>
                    </div>
                  </div>
                )}
              </div>
            </>
          )}
        </div>
      )}
    </Modal>
  );
}

// --- move ---

function MoveModal({
  node,
  nodes,
  onClose,
  onMoved,
}: {
  node: IPAMNode;
  nodes: IPAMNode[];
  onClose(): void;
  onMoved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  // A SENTINEL, NOT THE EMPTY STRING: a select's value is an identity, and an
  // empty one is the "nothing chosen" state every select renders as a prompt.
  const ROOT = 'root';
  const [target, setTarget] = useState(ROOT);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const options = nodes
    .filter((candidate) => candidate._id !== node._id)
    .map((candidate) => ({
      value: String(candidate._id),
      label: `${displayValue(candidate.name ?? candidate._id)}${isSubnet(candidate) ? ` · ${displayValue(candidate.cidr)}` : ''}`,
    }));

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      await moveSubnet(node._id, target === ROOT ? undefined : Number(target));
      await onMoved(tr('已移动', 'Moved'));
    } catch (error) {
      // THE SERVER REFUSES A MOVE INTO ITS OWN DESCENDANT OR INTO A RANGE THAT
      // WOULD COLLIDE, and says which — a page that precomputed those checks
      // would duplicate the transaction's own answer.
      setErr(describeError(error, tr('移动失败', 'Could not move it')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('移动网段', 'Move the range')}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting}>
            {tr('移动', 'Move')}
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
        <p className="text-xs text-text-muted">
          {tr(
            `「${displayValue(node.name ?? node._id)}」当前在别处。选一个新的上级，或移到根。`,
            `“${displayValue(node.name ?? node._id)}” sits somewhere else. Pick a new parent, or move it to the root.`,
          )}
        </p>
        <div>
          <Label htmlFor="cmdb-ipam-move">{tr('新上级', 'New parent')}</Label>
          <Select
            id="cmdb-ipam-move"
            label={tr('新上级', 'New parent')}
            value={target}
            onValueChange={setTarget}
            options={[{ value: ROOT, label: tr('根（无上级）', 'Root (no parent)') }, ...options]}
            searchable
            className="mt-1 w-full"
          />
        </div>
      </div>
    </Modal>
  );
}

// --- assign ---

function AssignModal({
  subnet,
  onClose,
  onAssigned,
}: {
  subnet: IPAMNode;
  onClose(): void;
  onAssigned(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [ips, setIps] = useState('');
  const [status, setStatus] = useState(String(ADDRESS_ASSIGNED));
  const [hosts, setHosts] = useState<string[]>([]);
  const [hostsLoading, setHostsLoading] = useState(false);
  const [truncated, setTruncated] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // THE RANGE'S ADDRESSES, offered as a picker. It is the range's host list,
  // not its FREE list — the endpoint answers "what would this range hold",
  // which is the question a form still being filled in can ask. Assigning one
  // that is already assigned is an update, which is what the operator asked
  // for by clicking it.
  useEffect(() => {
    if (!subnet.cidr) return;
    let live = true;
    setHostsLoading(true);
    void subnetHosts(String(subnet.cidr), 512)
      .then((got) => {
        if (!live) return;
        setHosts(got.hosts ?? []);
        setTruncated(Boolean(got.truncated));
      })
      .catch(() => live && setHosts([]))
      .finally(() => live && setHostsLoading(false));
    return () => {
      live = false;
    };
  }, [subnet.cidr]);

  const parsed = useMemo(
    () =>
      Array.from(
        new Set(
          ips
            .split(/[\s,;]+/)
            .map((item) => item.trim())
            .filter(Boolean),
        ),
      ),
    [ips],
  );

  const append = (ip: string) => {
    setIps((current) => (current.trim() ? `${current.trim()}\n${ip}` : ip));
  };

  const submit = async () => {
    if (parsed.length === 0) {
      setErr(tr('先填至少一个地址', 'Enter at least one address'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      const got = await assignAddresses({
        subnet_id: subnet._id,
        ips: parsed,
        assign_status: Number(status),
      });
      await onAssigned(
        tr(
          `已提交 ${parsed.length} 个地址，剩余 ${got.free_count} 个可用`,
          `Submitted ${parsed.length} addresses; ${got.free_count} free`,
        ),
      );
    } catch (error) {
      // THE SERVER REFUSES AN ADDRESS OUTSIDE THE RANGE and names it, which is
      // the only way the operator learns which line of the batch was wrong.
      setErr(describeError(error, tr('分配失败', 'Could not assign them')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('分配地址', 'Assign addresses')}
      size="lg"
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting}>
            {submitting ? tr('提交中…', 'Submitting…') : tr(`提交 ${parsed.length} 个`, `Submit ${parsed.length}`)}
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
        <p className="text-xs text-text-muted">
          {tr(`网段 ${displayValue(subnet.cidr)}：`, `Range ${displayValue(subnet.cidr)}: `)}
          {tr(
            '网段外的地址会被服务端拒绝。',
            'the server refuses an address outside the range.',
          )}
        </p>
        <div>
          <Label htmlFor="cmdb-ipam-ips">{tr('地址（每行一个，也接受逗号或空格）', 'Addresses (one per line; commas or spaces work too)')}</Label>
          <Textarea
            id="cmdb-ipam-ips"
            value={ips}
            onChange={(event) => setIps(event.target.value)}
            rows={5}
            placeholder={'10.0.0.11\n10.0.0.12'}
            className="mt-1 w-full font-mono"
          />
        </div>
        <div>
          <Label htmlFor="cmdb-ipam-status">{tr('状态', 'State')}</Label>
          <Select
            id="cmdb-ipam-status"
            label={tr('状态', 'State')}
            value={status}
            onValueChange={setStatus}
            options={[
              { value: String(ADDRESS_ASSIGNED), label: tr('已分配', 'Assigned') },
              { value: String(ADDRESS_RESERVED), label: tr('保留', 'Reserved') },
              { value: String(ADDRESS_UNASSIGNED), label: tr('回收', 'Released') },
            ]}
            className="mt-1 w-48"
          />
        </div>

        <div>
          <div className="mb-1 flex items-center justify-between gap-2">
            <span className="text-xs font-medium text-text">{tr('网段内的地址', 'Addresses in the range')}</span>
            {truncated && (
              <span className="text-[11px] text-text-faint">
                {tr('只列出前 512 个', 'The first 512 only')}
              </span>
            )}
          </div>
          {hostsLoading ? (
            <p className="py-3 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
          ) : hosts.length === 0 ? (
            <p className="py-3 text-center text-xs text-text-muted">{tr('这个网段没有可用地址', 'This range has no usable addresses')}</p>
          ) : (
            <div className="flex max-h-40 flex-wrap gap-1 overflow-y-auto rounded-md border border-border p-2">
              {hosts.map((host) => (
                <button
                  key={host}
                  type="button"
                  onClick={() => append(host)}
                  className="rounded px-1.5 py-0.5 font-mono text-[11px] text-text-muted hover:bg-card hover:text-text"
                >
                  {host}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </Modal>
  );
}

// --- small helpers ---

function isTruthy(value: unknown): boolean {
  if (value === true) return true;
  if (typeof value === 'number') return value !== 0;
  if (typeof value === 'string') return value === '1' || value.toLowerCase() === 'true';
  return false;
}

function assignStatusView(
  raw: unknown,
  tr: (zh: string, en: string) => string,
): { label: string; tone: 'default' | 'success' | 'warning' | 'info' } {
  const status = Number(raw);
  if (status === ADDRESS_ASSIGNED) return { label: tr('已分配', 'Assigned'), tone: 'success' };
  if (status === ADDRESS_UNASSIGNED) return { label: tr('已回收', 'Released'), tone: 'default' };
  if (status === ADDRESS_RESERVED) return { label: tr('保留', 'Reserved'), tone: 'warning' };
  // AN UNKNOWN STATE IS SHOWN AS IT IS rather than mapped to one of the three:
  // a value this build does not know is something a deployment declared, and
  // guessing would report a state nobody stored.
  return { label: raw === undefined || raw === null ? '—' : String(raw), tone: 'info' };
}

function operationLabel(kind: string, tr: (zh: string, en: string) => string): string {
  switch (kind) {
    case '3':
      return tr('新建网段', 'Range created');
    case '4':
      return tr('修改网段', 'Range updated');
    case '5':
      return tr('删除网段', 'Range deleted');
    case '6':
      return tr('分配地址', 'Address assigned');
    case '7':
      return tr('回收地址', 'Address released');
    case '8':
      return tr('移动网段', 'Range moved');
    default:
      return kind;
  }
}

function operationTone(kind: string): 'success' | 'warning' | 'danger' | 'info' | 'default' {
  switch (kind) {
    case '3':
      return 'success';
    case '4':
    case '8':
      return 'info';
    case '5':
      return 'danger';
    case '6':
      return 'success';
    case '7':
      return 'warning';
    default:
      return 'default';
  }
}
