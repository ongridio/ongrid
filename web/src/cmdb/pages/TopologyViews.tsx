import { useCallback, useEffect, useRef, useState } from 'react';
import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { PageHeader } from '@/components/ui/PageHeader';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';
import { cn } from '@/lib/cn';

import { listCITypes, type CIType } from '../api/citype';
import { describeError } from '../api/http';
import {
  createTopologyGroup,
  deleteTopologyGroup,
  deleteTopologyView,
  getTopologyGraph,
  listTopologyGroups,
  listTopologyViews,
  type TopologyGraph,
  type TopologyGroup,
  type TopologyView,
} from '../api/topology';
import { TopoGraph } from '../components/topology/TopoGraph';
import { TopologySidebar } from '../components/topology/TopologySidebar';
import { TopologyViewDrawer } from '../components/topology/TopologyViewDrawer';
import { useToast } from '../components/Toast';

// 拓扑视图 — saved walks drawn as graphs.
//
// THE SIDEBAR IS THE SAVED CONFIGURATION and the canvas is the current estate:
// selecting a view assembles it from today's relations, so an edge added this
// morning appears without re-saving anything. That is the whole difference
// between a view and an exported picture.

export default function TopologyViewsPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [groups, setGroups] = useState<TopologyGroup[]>([]);
  const [views, setViews] = useState<TopologyView[]>([]);
  const [models, setModels] = useState<CIType[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [selectedID, setSelectedID] = useState<number | null>(null);

  const [graph, setGraph] = useState<TopologyGraph | null>(null);
  const [graphLoading, setGraphLoading] = useState(false);
  const [graphErr, setGraphErr] = useState<string | null>(null);

  const [drawer, setDrawer] = useState<TopologyView | 'new' | null>(null);
  const [groupOpen, setGroupOpen] = useState(false);
  const [groupName, setGroupName] = useState('');
  const [groupSaving, setGroupSaving] = useState(false);
  const [groupErr, setGroupErr] = useState<string | null>(null);

  const loadLists = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const [gotGroups, gotViews] = await Promise.all([listTopologyGroups(), listTopologyViews()]);
      setGroups(gotGroups);
      setViews(gotViews);
      setSelectedID((current) => {
        if (current != null && gotViews.some((view) => view.id === current)) return current;
        const firstGrouped = gotGroups.flatMap((group) => group.views)[0];
        return firstGrouped?.id ?? gotViews[0]?.id ?? null;
      });
    } catch (error) {
      setErr(describeError(error, tr('无法读取拓扑视图', 'Could not load the topology views')));
    } finally {
      setLoading(false);
    }
  }, [tr]);

  useEffect(() => {
    void loadLists();
  }, [loadLists]);

  useEffect(() => {
    if (!isAdmin) return;
    void listCITypes()
      .then(setModels)
      .catch(() => setModels([]));
  }, [isAdmin]);

  // A GRAPH BELONGS TO THE VIEW THAT ASKED FOR IT. The sequence guards against
  // a late response from a view the operator has already left; clearing the
  // previous graph before the request also stops a failed view from showing
  // its predecessor's picture under its own title.
  const graphSeq = useRef(0);

  const loadGraph = useCallback(
    async (id: number) => {
      const seq = ++graphSeq.current;
      setGraph(null);
      setGraphErr(null);
      setGraphLoading(true);
      try {
        const got = await getTopologyGraph(id);
        if (seq === graphSeq.current) setGraph(got);
      } catch (error) {
        if (seq === graphSeq.current) {
          setGraphErr(describeError(error, tr('无法装配拓扑图', 'Could not assemble the graph')));
        }
      } finally {
        if (seq === graphSeq.current) setGraphLoading(false);
      }
    },
    [tr],
  );

  useEffect(() => {
    if (selectedID == null) {
      graphSeq.current += 1;
      setGraph(null);
      setGraphErr(null);
      setGraphLoading(false);
      return;
    }
    void loadGraph(selectedID);
    return () => {
      // Invalidate this request on unmount or when the selection changes.
      graphSeq.current += 1;
    };
  }, [selectedID, loadGraph]);

  const selectedView = views.find((view) => view.id === selectedID) ?? null;
  const grouped = new Set(groups.flatMap((group) => group.views.map((view) => view.id)));
  const ungrouped = views.filter((view) => !grouped.has(view.id));

  const removeView = async (view: TopologyView) => {
    const ok = await confirmAction(
      tr(`删除拓扑视图「${view.name}」？`, `Delete the topology view “${view.name}”?`),
    );
    if (!ok) return;
    try {
      await deleteTopologyView(view.id);
      toast.success(tr('已删除', 'Deleted'));
      setSelectedID(null);
      await loadLists();
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const removeGroup = async (group: TopologyGroup) => {
    const ok = await confirmAction(
      tr(`删除分组「${group.name}」？分组里还有视图时会被拒绝。`,
        `Delete the group “${group.name}”? A group that still holds views is refused.`),
    );
    if (!ok) return;
    try {
      await deleteTopologyGroup(group.id);
      toast.success(tr('已删除', 'Deleted'));
      await loadLists();
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const createGroup = async () => {
    if (!groupName.trim()) {
      setGroupErr(tr('名称不能为空', 'A name is required'));
      return;
    }
    setGroupSaving(true);
    setGroupErr(null);
    try {
      await createTopologyGroup(groupName.trim());
      toast.success(tr('已创建分组', 'Group created'));
      setGroupOpen(false);
      setGroupName('');
      await loadLists();
    } catch (error) {
      setGroupErr(describeError(error, tr('创建失败', 'Could not create it')));
    } finally {
      setGroupSaving(false);
    }
  };

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('拓扑视图', 'Topology views')}
        subtitle={tr(
          '保存一次关系遍历：中心模型、根实例查询，以及向下 / 向上的层级。每次打开都从当前关系重新装配。',
          'Save a relation walk: a central model, a root query, and the levels below and above. Every open assembles it from the current relations.',
        )}
        actions={
          <Button
            variant="outline"
            size="sm"
            type="button"
            onClick={() => {
              if (selectedID != null) void loadGraph(selectedID);
              void loadLists();
            }}
            disabled={loading || graphLoading}
            className="inline-flex items-center gap-1.5"
          >
            <RefreshCw size={12} className={cn((loading || graphLoading) && 'animate-spin')} aria-hidden="true" />
            {tr('刷新', 'Refresh')}
          </Button>
        }
      />

      <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-4 overflow-y-auto p-6 lg:flex-row">
        <TopologySidebar
          groups={groups}
          ungrouped={ungrouped}
          selected={selectedID}
          isAdmin={isAdmin}
          onSelect={setSelectedID}
          onNewView={() => setDrawer('new')}
          onNewGroup={() => {
            setGroupName('');
            setGroupErr(null);
            setGroupOpen(true);
          }}
          onDeleteGroup={(group) => void removeGroup(group)}
        />

        <div className="flex min-w-0 flex-1 flex-col gap-3">
          {err && (
            <p role="alert" className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
              {err}
            </p>
          )}

          {selectedView == null ? (
            <Card>
              <EmptyState
                icon={Plus}
                title={tr('选择一个拓扑视图', 'Choose a topology view')}
                hint={tr('左侧列出已保存的视图；新建一个开始。', 'The left list holds the saved views. Create one to start.')}
              />
            </Card>
          ) : (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="min-w-0">
                  <h2 className="truncate text-sm font-semibold text-text">{selectedView.name}</h2>
                  <p className="mt-0.5 text-[11px] text-text-faint">
                    {tr(
                      `中心模型 #${selectedView.central_node_type} · 根查询 ${selectedView.central_node_instances || '全部'}`,
                      `Central model #${selectedView.central_node_type} · roots ${selectedView.central_node_instances || 'all'}`,
                    )}
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    variant="subtle"
                    size="sm"
                    type="button"
                    onClick={() => setDrawer(selectedView)}
                    disabled={!isAdmin}
                    aria-label={tr('编辑拓扑视图', 'Edit the topology view')}
                    className="inline-flex items-center gap-1"
                  >
                    <Pencil size={12} aria-hidden="true" />
                    {tr('编辑', 'Edit')}
                  </Button>
                  <Button
                    variant="dangerGhost"
                    size="sm"
                    type="button"
                    onClick={() => void removeView(selectedView)}
                    disabled={!isAdmin}
                    aria-label={tr('删除拓扑视图', 'Delete the topology view')}
                    className="inline-flex items-center gap-1"
                  >
                    <Trash2 size={12} aria-hidden="true" />
                    {tr('删除', 'Delete')}
                  </Button>
                </div>
              </div>

              {graphErr && (
                <p role="alert" className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
                  {graphErr}
                </p>
              )}

              <Card className="min-h-0 flex-1">
                {graphLoading ? (
                  <p className="p-6 text-xs text-text-muted">{tr('正在装配拓扑图…', 'Assembling the graph…')}</p>
                ) : graph == null || graph.nodes.length === 0 ? (
                  <EmptyState
                    title={tr('这个视图没有匹配的实例', 'This view matches no instance')}
                    hint={tr('检查根实例查询与路径层级是否与现有关系一致。',
                      'Check that the root query and the path levels agree with the relations that exist.')}
                  />
                ) : (
                  <div className="p-2">
                    <TopoGraph graph={graph} />
                    <p className="px-2 pb-1 text-[11px] text-text-faint">
                      {tr(`${graph.nodes.length} 个节点，${graph.links.length} 条边`,
                        `${graph.nodes.length} nodes, ${graph.links.length} links`)}
                    </p>
                  </div>
                )}
              </Card>
            </>
          )}
        </div>
      </div>

      {drawer && (
        <TopologyViewDrawer
          view={drawer === 'new' ? null : drawer}
          groups={groups}
          models={models}
          onClose={() => setDrawer(null)}
          onSaved={async (message) => {
            setDrawer(null);
            toast.success(message);
            await loadLists();
          }}
        />
      )}

      <Modal
        open={groupOpen}
        onClose={() => setGroupOpen(false)}
        title={tr('新建分组', 'New group')}
        footer={
          <div className="flex justify-end gap-2">
            <Button variant="outline" type="button" onClick={() => setGroupOpen(false)} disabled={groupSaving}>
              {tr('取消', 'Cancel')}
            </Button>
            <Button variant="primary" type="button" onClick={() => void createGroup()} disabled={groupSaving}>
              {groupSaving ? tr('创建中…', 'Creating…') : tr('创建', 'Create')}
            </Button>
          </div>
        }
      >
        {groupErr && (
          <p role="alert" className="mb-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
            {groupErr}
          </p>
        )}
        <Label htmlFor="topo-group-name">{tr('分组名称', 'Group name')}</Label>
        <Input
          id="topo-group-name"
          aria-label={tr('分组名称', 'Group name')}
          value={groupName}
          onChange={(event) => setGroupName(event.target.value)}
          className="mt-1 w-full"
        />
      </Modal>

      {dialog}
    </div>
  );
}
