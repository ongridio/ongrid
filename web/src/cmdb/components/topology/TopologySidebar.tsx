import { FolderPlus, Plus, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

import type { TopologyGroup, TopologyView } from '../../api/topology';

// The view list: folders and the views inside them, plus the ungrouped ones.
//
// A VIEW WITH NO FOLDER IS NOT HIDDEN. cantor put ungrouped views in a synthetic
// "other" group; the port renders them under a heading that says what they are,
// so a view cannot become unreachable by losing its folder.

export function TopologySidebar({
  groups,
  ungrouped,
  selected,
  isAdmin,
  onSelect,
  onNewView,
  onNewGroup,
  onDeleteGroup,
}: {
  groups: TopologyGroup[];
  ungrouped: TopologyView[];
  selected: number | null;
  isAdmin: boolean;
  onSelect(id: number): void;
  onNewView(): void;
  onNewGroup(): void;
  onDeleteGroup(group: TopologyGroup): void;
}) {
  const { tr } = useI18n();

  const viewButton = (view: TopologyView) => (
    <li key={view.id}>
      <button
        type="button"
        onClick={() => onSelect(view.id)}
        aria-current={selected === view.id ? 'true' : undefined}
        aria-label={tr(`查看拓扑视图 ${view.name}`, `View topology ${view.name}`)}
        className={cn(
          'w-full truncate rounded-md px-3 py-1.5 text-left text-xs focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500',
          selected === view.id ? 'bg-indigo-500/10 text-text' : 'text-text-muted hover:bg-card hover:text-text',
        )}
      >
        {view.name}
      </button>
    </li>
  );

  return (
    <aside className="flex w-full shrink-0 flex-col gap-2 lg:w-60">
      <div className="flex items-center gap-2">
        <Button
          variant="primary"
          size="sm"
          type="button"
          onClick={onNewView}
          disabled={!isAdmin}
          className="inline-flex flex-1 items-center justify-center gap-1.5"
        >
          <Plus size={12} aria-hidden="true" />
          {tr('新建视图', 'New view')}
        </Button>
        <Button
          variant="outline"
          size="sm"
          type="button"
          onClick={onNewGroup}
          disabled={!isAdmin}
          aria-label={tr('新建分组', 'New group')}
          className="inline-flex items-center gap-1"
        >
          <FolderPlus size={12} aria-hidden="true" />
        </Button>
      </div>

      <div className="min-h-0 overflow-y-auto rounded-lg border border-border bg-card p-2">
        {groups.length === 0 && ungrouped.length === 0 ? (
          <p className="px-2 py-3 text-xs text-text-faint">
            {tr('还没有拓扑视图。新建一个开始。', 'No topology view yet. Create one to start.')}
          </p>
        ) : (
          <>
            {groups.map((group) => (
              <section key={group.id} className="mb-2 last:mb-0">
                <div className="flex items-center gap-1 px-1">
                  <h2 className="min-w-0 flex-1 truncate text-[11px] font-semibold uppercase tracking-wide text-text-faint">
                    {group.name}
                  </h2>
                  {isAdmin && (
                    <button
                      type="button"
                      onClick={() => onDeleteGroup(group)}
                      aria-label={tr(`删除分组 ${group.name}`, `Delete group ${group.name}`)}
                      className="rounded p-0.5 text-text-faint hover:text-red-300 focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
                    >
                      <Trash2 size={11} aria-hidden="true" />
                    </button>
                  )}
                </div>
                <ul className="mt-0.5 flex flex-col gap-0.5">{group.views.map(viewButton)}</ul>
              </section>
            ))}
            {ungrouped.length > 0 && (
              <section>
                <h2 className="px-1 text-[11px] font-semibold uppercase tracking-wide text-text-faint">
                  {tr('未分组', 'Ungrouped')}
                </h2>
                <ul className="mt-0.5 flex flex-col gap-0.5">{ungrouped.map(viewButton)}</ul>
              </section>
            )}
          </>
        )}
      </div>
    </aside>
  );
}
