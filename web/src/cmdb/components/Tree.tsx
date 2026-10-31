import { useCallback, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react';
import { ChevronRight, Loader2 } from 'lucide-react';

import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

// A navigable tree.
//
// FIVE CMDB SURFACES ARE TREES — the service tree, the resource hierarchy, the
// IPAM address tree, the data-centre tree and the model groups — and they differ
// only in what a node draws and what it means. So the tree here knows about
// structure, expansion and the keyboard, and nothing about any of those five.
//
// KEYBOARD NAVIGATION IS NOT OPTIONAL. A hierarchy can be hundreds of nodes deep
// and wide; a tree that can only be driven by clicking a chevron is a tree a
// keyboard user cannot use at all. The WAI-ARIA pattern is implemented rather
// than approximated: roving tabindex, one tab stop for the whole tree, arrows to
// move, Right/Left to open and close.

export type TreeNode = {
  /** Stable identity, and the value `onSelect` reports. */
  id: string;
  label: ReactNode;
  /** Rows drawn beside the label — a count, a chip. */
  trailing?: ReactNode;
  /** Whether the node has children, before they are loaded. */
  hasChildren?: boolean;
  /** Children, when they are already here. */
  children?: TreeNode[];
  /** A node that cannot be opened or selected — a placeholder, a permission stub. */
  disabled?: boolean;

  /** Whatever the caller needs to carry with a node — a depth, a record. */
  data?: unknown;
};

type Props = {
  nodes: TreeNode[];
  selectedId?: string | null;
  onSelect?: (node: TreeNode) => void;
  /** Called when an unloaded node is opened, so the caller can fetch its children. */
  onExpand?: (node: TreeNode, expanded: boolean) => void;
  /** Ids of nodes whose children are being fetched. */
  loadingIds?: ReadonlySet<string>;
  /** Ids open at first render. */
  defaultExpanded?: string[];
  /** The label for the tree region, for a screen reader. */
  label: string;
  /** Shown when there are no nodes at all. */
  empty?: ReactNode;
  className?: string;
};

export function Tree({
  nodes,
  selectedId,
  onSelect,
  onExpand,
  loadingIds,
  defaultExpanded,
  label,
  empty,
  className,
}: Props) {
  const { tr } = useI18n();
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set(defaultExpanded ?? []));
  const [focusId, setFocusId] = useState<string | null>(nodes[0]?.id ?? null);
  const itemRefs = useRef(new Map<string, HTMLDivElement>());

  // THE VISIBLE ORDER IS WHAT THE ARROWS WALK. A node inside a closed parent is
  // not in the tab order and not in the arrow order, which is why this is
  // computed rather than read off the data.
  const visible = useMemo(() => {
    const out: Array<{ node: TreeNode; level: number; parentId: string | null; hasChildren: boolean }> = [];
    const walk = (items: TreeNode[], level: number, parentId: string | null) => {
      for (const node of items) {
        const hasChildren = Boolean(node.children?.length) || Boolean(node.hasChildren);
        out.push({ node, level, parentId, hasChildren });
        if (hasChildren && expanded.has(node.id) && node.children?.length) {
          walk(node.children, level + 1, node.id);
        }
      }
    };
    walk(nodes, 1, null);
    return out;
  }, [nodes, expanded]);

  const toggle = useCallback(
    (node: TreeNode, next: boolean) => {
      setExpanded((current) => {
        const copy = new Set(current);
        if (next) copy.add(node.id);
        else copy.delete(node.id);
        return copy;
      });
      onExpand?.(node, next);
    },
    [onExpand],
  );

  const moveFocus = useCallback(
    (delta: number) => {
      if (visible.length === 0) return;
      const index = visible.findIndex((entry) => entry.node.id === focusId);
      const next = Math.min(visible.length - 1, Math.max(0, (index < 0 ? 0 : index) + delta));
      const target = visible[next];
      setFocusId(target.node.id);
      itemRefs.current.get(target.node.id)?.focus();
    },
    [visible, focusId],
  );

  const onKeyDown = (event: ReactKeyboardEvent, entry: (typeof visible)[number]) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        moveFocus(1);
        break;
      case 'ArrowUp':
        event.preventDefault();
        moveFocus(-1);
        break;
      case 'ArrowRight':
        event.preventDefault();
        if (entry.hasChildren && !expanded.has(entry.node.id)) toggle(entry.node, true);
        else moveFocus(1);
        break;
      case 'ArrowLeft':
        event.preventDefault();
        if (entry.hasChildren && expanded.has(entry.node.id)) toggle(entry.node, false);
        else if (entry.parentId) {
          setFocusId(entry.parentId);
          itemRefs.current.get(entry.parentId)?.focus();
        }
        break;
      case 'Enter':
      case ' ':
        event.preventDefault();
        if (!entry.node.disabled) onSelect?.(entry.node);
        break;
      default:
        break;
    }
  };

  if (nodes.length === 0) {
    return (
      <div className="flex h-40 items-center justify-center text-xs text-text-muted">
        {empty ?? tr('没有内容', 'Nothing here')}
      </div>
    );
  }

  return (
    <div role="tree" aria-label={label} className={cn('cmdb-tree min-w-0 select-none', className)}>
      {visible.map((entry) => {
        const { node, level, hasChildren } = entry;
        const open = expanded.has(node.id);
        const selected = selectedId === node.id;
        const loading = loadingIds?.has(node.id) ?? false;
        return (
          <div
            key={node.id}
            ref={(element) => {
              if (element) itemRefs.current.set(node.id, element);
              else itemRefs.current.delete(node.id);
            }}
            role="treeitem"
            aria-level={level}
            aria-selected={selected}
            // ONLY A NODE THAT CAN OPEN REPORTS AN EXPANDED STATE: announcing
            // `aria-expanded="false"` on a leaf tells a screen reader there is
            // something under it that is closed.
            aria-expanded={hasChildren ? open : undefined}
            aria-disabled={node.disabled || undefined}
            tabIndex={focusId === node.id ? 0 : -1}
            onFocus={() => setFocusId(node.id)}
            onKeyDown={(event) => onKeyDown(event, entry)}
            onClick={() => {
              if (node.disabled) return;
              setFocusId(node.id);
              onSelect?.(node);
            }}
            onDoubleClick={() => {
              if (hasChildren && !node.disabled) toggle(node, !open);
            }}
            className={cn(
              'flex min-w-0 cursor-pointer items-center gap-1 rounded px-2 py-1 text-xs',
              selected ? 'bg-indigo-500/10 text-text' : 'text-text-muted hover:bg-card hover:text-text',
              node.disabled && 'cursor-not-allowed opacity-50',
            )}
            // THE INDENT IS PER LEVEL, which is what makes a deep hierarchy
            // readable; it is a style rather than a class so the level does not
            // have to be one of a fixed set.
            style={{ paddingLeft: `${(level - 1) * 14 + 4}px` }}
          >
            {hasChildren ? (
              <button
                type="button"
                tabIndex={-1}
                aria-hidden="true"
                onClick={(event) => {
                  event.stopPropagation();
                  if (!node.disabled) toggle(node, !open);
                }}
                className="shrink-0 rounded p-0.5 text-text-faint hover:text-text"
              >
                {loading ? (
                  <Loader2 size={12} className="animate-spin" />
                ) : (
                  <ChevronRight size={12} className={cn('transition-transform', open && 'rotate-90')} />
                )}
              </button>
            ) : (
              <span className="w-[18px] shrink-0" />
            )}
            <span className="min-w-0 flex-1 truncate">{node.label}</span>
            {node.trailing && <span className="shrink-0 text-text-faint">{node.trailing}</span>}
          </div>
        );
      })}
    </div>
  );
}
