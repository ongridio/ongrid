import { useCallback, useEffect, useMemo, useState } from 'react';

import { useI18n } from '@/i18n/locale';

import { listInstances, searchHierarchy, type SearchRow } from '../api/instance';
import type { CIType } from '../api/citype';
import { Tree, type TreeNode } from './Tree';
import { useToast } from './Toast';

// A tree of instances, expanded one hop at a time.
//
// ONE REQUEST PER EXPANSION, and that request is the hierarchy walk rather than
// the neighbour list: `level=1` on the walk returns the children WITH THEIR
// VALUES, so a label costs nothing extra. The neighbour endpoint returns ids
// only, which would make every expanded node an N+1.
//
// THE DEPTH CAP IS THE PAGE'S, NOT THE TREE'S. The walk endpoint has no idea how
// deep a deployment wants to go, and each extra level is another query on every
// expansion, so the cap lives here and the tree stops offering a chevron at it.

type Options = {
  /** The model whose instances are the roots. */
  rootModel?: CIType | null;
  /** How many levels to allow below the roots, inclusive (1–8). */
  maxLevel: number;
  /** Called when a node is picked, with the row the walk returned for it. */
  onSelect?(row: SearchRow | null): void;
};

type State = {
  /** The roots, plus whatever children have been loaded into them. */
  nodes: TreeNode[];
  /** id → the row the walk returned, so a pick can report the values. */
  byID: Map<number, SearchRow>;
};

export function InstanceTree({ rootModel, maxLevel, onSelect }: Options) {
  const { tr } = useI18n();
  const toast = useToast();

  const [state, setState] = useState<State>({ nodes: [], byID: new Map() });
  const [loadingRoots, setLoadingRoots] = useState(false);
  const [loadingIDs, setLoadingIDs] = useState<ReadonlySet<string>>(new Set());
  const [err, setErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);

  // The roots are the model's instances. They are not walked to — every instance
  // of the root model is a root by definition, which is what makes a hierarchy
  // page startable without asking for a root id first.
  const loadRoots = useCallback(async () => {
    if (!rootModel) return;
    setLoadingRoots(true);
    setErr(null);
    try {
      const got = await listInstances(rootModel.id, { page: 1, count: 100 });
      const rows = (got.items ?? []).map((item) => item.values ?? {});
      const nodes = rows.map((values, index) => toNode(got.items[index].ci_id, values, rootModel, 1, maxLevel));
      const byID = new Map<number, SearchRow>();
      (got.items ?? []).forEach((item, index) => {
        byID.set(item.ci_id, {
          _id: item.ci_id,
          _type: item.type_id,
          ci_id: item.ci_id,
          ci_type: rootModel.name,
          ci_type_alias: rootModel.alias,
          ...rows[index],
        });
      });
      setState({ nodes, byID });
    } catch (error) {
      setErr(error instanceof Error ? error.message : tr('无法读取顶层实例', 'Could not load the top level'));
    } finally {
      setLoadingRoots(false);
    }
  }, [rootModel, maxLevel, tr]);

  useEffect(() => {
    setState({ nodes: [], byID: new Map() });
    setSelected(null);
    void loadRoots();
  }, [loadRoots]);

  const expand = useCallback(
    async (node: TreeNode, expanded: boolean) => {
      if (!expanded) return;
      const depth = Number(node.data ?? 0);
      if (depth >= maxLevel) return;
      // Already loaded: re-expanding is free.
      if (node.children && node.children.length > 0) return;

      setLoadingIDs((current) => new Set(current).add(node.id));
      try {
        const got = await searchHierarchy({
          rootIDs: [Number(node.id)],
          // LEVEL 1 IS "ONE HOP BELOW THE ROOT", so the children carry their
          // values and no second request is needed for a label.
          levels: [1],
          count: 200,
        });
        const rows = got.result ?? [];
        const children = rows.map((row) => toNode(row._id, row, rootModel, depth + 1, maxLevel));
        setState((current) => {
          const byID = new Map(current.byID);
          rows.forEach((row) => byID.set(row._id, row));
          return { nodes: attachChildren(current.nodes, node.id, children), byID };
        });
      } catch (error) {
        toast.error(error instanceof Error ? error.message : tr('无法展开这一层', 'Could not expand this level'));
      } finally {
        // THE SPINNER MUST GO WHETHER OR NOT THE WALK WORKED, or a failed
        // expansion leaves a node that looks permanently busy.
        setLoadingIDs((current) => {
          const next = new Set(current);
          next.delete(node.id);
          return next;
        });
      }
    },
    [maxLevel, rootModel, toast, tr],
  );

  const empty = useMemo(() => {
    if (!rootModel) return tr('先选一个模型', 'Pick a model first');
    if (loadingRoots) return tr('加载中…', 'Loading…');
    return tr('这个模型还没有实例', 'This model has no instances yet');
  }, [rootModel, loadingRoots, tr]);

  if (err) {
    return (
      <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
        {err}
      </div>
    );
  }

  return (
    <Tree
      nodes={state.nodes}
      label={tr('资源层级', 'Resource hierarchy')}
      selectedId={selected}
      loadingIds={loadingIDs}
      empty={empty}
      onExpand={(node, expanded) => void expand(node, expanded)}
      onSelect={(node) => {
        setSelected(node.id);
        onSelect?.(state.byID.get(Number(node.id)) ?? null);
      }}
    />
  );
}

/** Builds one tree node, remembering its depth so the cap is enforceable. */
function toNode(id: number, row: Record<string, unknown> | SearchRow, model: CIType | null | undefined, depth: number, maxLevel: number): TreeNode {
  const unique = (row as SearchRow).unique;
  const title =
    (unique ? row[unique] : undefined) ??
    row[model?.unique_key ?? ''] ??
    row.name ??
    (typeof row === 'object' ? Object.values(row).find((value) => typeof value === 'string') : undefined);
  const label = typeof title === 'string' && title ? title : `#${id}`;
  return {
    id: String(id),
    label,
    // OPTIMISTIC ABOVE THE CAP: the walk has not been asked yet, so a chevron
    // means "there may be more here". Below the cap it would be a lie, and the
    // first expansion that comes back empty removes it.
    hasChildren: depth < maxLevel,
    data: depth,
  };
}

/** Replaces one node's children, leaving the rest of the tree alone. */
function attachChildren(nodes: TreeNode[], parentID: string, children: TreeNode[]): TreeNode[] {
  return nodes.map((node) => {
    if (node.id === parentID) return { ...node, children, hasChildren: children.length > 0 };
    if (node.children?.length) return { ...node, children: attachChildren(node.children, parentID, children) };
    return node;
  });
}
