import { lazy, Suspense, useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Cloud, Cpu, Wrench, RefreshCw, Play, Search, Package } from 'lucide-react';
import { cn } from '@/lib/cn';
import { listSkills, localizedSkill, type SkillClass, type SkillScope, type SkillSummary } from '@/api/skills';
import { ApiError } from '@/api/client';
import { Modal } from '@/components/Modal';
import { useI18n } from '@/i18n/locale';
import { useAuth } from '@/store/auth';
import { PageHeader } from '@/components/ui';

// Lazy-load the install/uninstall surface so the default Catalog tab
// doesn't pull in the marketplace bundle until the operator actually
// switches tabs.
const InstallTab = lazy(() => import('@/pages/settings/Marketplace'));

type ScopeFilter = '' | SkillScope;
type Tab = 'catalog' | 'install';

export default function SkillsPage() {
  const { tr } = useI18n();
  // Tab is URL-driven (?tab=install). HLD-017: the install surface is now
  // a visible, admin-only toggle (re-surfaced 2026-06-21 for the cloud
  // skill marketplace). Non-admins only ever see the catalog.
  const [searchParams, setSearchParams] = useSearchParams();
  const isAdmin = useAuth((s) => s.role) === 'admin';
  const tab: Tab = searchParams.get('tab') === 'install' && isAdmin ? 'install' : 'catalog';

  const setTab = (t: Tab) => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (t === 'install') next.set('tab', 'install');
        else next.delete('tab');
        return next;
      },
      { replace: true }
    );
  };

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={tr('技能', 'Skills')}
        subtitle={tr('LLM 当前可见的能力，以及安装 / 管理外部技能', 'Capabilities the LLM can use — plus installing / managing external skills')}
        extra={
          isAdmin ? (
            <div className="-mb-4 flex items-center gap-1">
              <TabButton active={tab === 'catalog'} onClick={() => setTab('catalog')} icon={<Wrench size={14} />} label={tr('技能目录', 'Catalog')} />
              <TabButton active={tab === 'install'} onClick={() => setTab('install')} icon={<Package size={14} />} label={tr('技能市场', 'Marketplace')} />
            </div>
          ) : undefined
        }
      />
      {tab === 'install' ? (
        <div className="flex-1 overflow-auto px-6 py-4">
          <Suspense fallback={<div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>}>
            <InstallTab />
          </Suspense>
        </div>
      ) : (
        <CatalogTab />
      )}
    </main>
  );
}

function TabButton({ active, onClick, icon, label }: { active: boolean; onClick: () => void; icon: ReactNode; label: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 border-b-2 px-3 py-2 text-[13px] font-medium transition-colors',
        active
          ? 'border-indigo-500 text-zinc-100'
          : 'border-transparent text-zinc-500 hover:text-zinc-300'
      )}
    >
      {icon}
      {label}
    </button>
  );
}

function CatalogTab() {
  const { tr } = useI18n();
  const [items, setItems] = useState<SkillSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState<string>('');
  const [scope, setScope] = useState<ScopeFilter>('');
  // 详情弹窗：点击行打开（参照知识库文档的阅读弹窗）。技能由代码 /
  // 技能包定义，注册表在内存——只读查看，无编辑路径。
  const [viewing, setViewing] = useState<SkillSummary | null>(null);

  const fetchSkills = useCallback(async (silent = false) => {
    if (silent) setRefreshing(true);
    else setLoading(true);
    try {
      const r = await listSkills();
      setItems((r.items ?? []).map(localizedSkill));
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    fetchSkills();
  }, [fetchSkills]);

  const categories = useMemo(() => {
    const set = new Set<string>();
    for (const s of items) {
      if (s.category) set.add(s.category);
    }
    return Array.from(set).sort();
  }, [items]);

  // Scope defaults to 'host' on the backend when omitted (skillcore.EffectiveScope),
  // so for filter purposes we treat undefined as 'host'.
  const effectiveScope = (s: SkillSummary): SkillScope => s.scope ?? 'host';

  const counts = useMemo(() => {
    let edge = 0;
    let manager = 0;
    for (const s of items) {
      if (effectiveScope(s) === 'manager') manager++;
      else edge++;
    }
    return { edge, manager, total: items.length };
  }, [items]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return items.filter((s) => {
      if (category && s.category !== category) return false;
      if (scope && effectiveScope(s) !== scope) return false;
      if (!q) return true;
      return (
        s.name.toLowerCase().includes(q) ||
        s.key.toLowerCase().includes(q) ||
        (s.description ?? '').toLowerCase().includes(q)
      );
    });
  }, [items, query, category, scope]);

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
        <div className="border-b border-zinc-800 px-6 py-3">
          <div className="flex flex-wrap items-center gap-3">
            <label className="relative block w-64">
              <span className="sr-only">{tr('搜索', 'Search')}</span>
              <Search size={12} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500" />
              <input
                type="search"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={tr('搜索 name / key / description', 'Search name / key / description')}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950/40 py-1.5 pl-8 pr-2 text-xs text-zinc-200 placeholder:text-zinc-500 focus:border-zinc-700 focus:outline-none"
              />
            </label>
            <div className="inline-flex items-center gap-1.5 text-xs text-zinc-400">
              <span className="text-zinc-500">{tr('运行位置', 'Runs on')}</span>
              <div className="flex flex-wrap gap-1">
                <CategoryChip
                  active={scope === ''}
                  label={`${tr('全部', 'All')} ${counts.total}`}
                  onClick={() => setScope('')}
                />
                <CategoryChip
                  active={scope === 'host'}
                  label={`🖥️ ${tr('设备端', 'Device')} ${counts.edge}`}
                  onClick={() => setScope('host')}
                />
                <CategoryChip
                  active={scope === 'manager'}
                  label={`☁️ ${tr('云端', 'Cloud')} ${counts.manager}`}
                  onClick={() => setScope('manager')}
                />
              </div>
            </div>
            {categories.length > 0 && (
              <div className="inline-flex items-center gap-1.5 text-xs text-zinc-400">
                <span className="text-zinc-500">{tr('分类', 'Category')}</span>
                <div className="flex flex-wrap gap-1">
                  <CategoryChip
                    active={category === ''}
                    label={tr('全部', 'All')}
                    onClick={() => setCategory('')}
                  />
                  {categories.map((c) => (
                    <CategoryChip
                      key={c}
                      active={category === c}
                      label={c}
                      onClick={() => setCategory(c)}
                    />
                  ))}
                </div>
              </div>
            )}
            <div className="ml-auto flex items-center gap-2">
              <span className="text-xs text-zinc-500">
                {tr(`${items.length} 个 · 匹配 ${filtered.length}`, `${items.length} total · ${filtered.length} matched`)}
              </span>
              <button
                type="button"
                onClick={() => fetchSkills(true)}
                disabled={loading || refreshing}
                className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-40"
              >
                <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
                {tr('刷新', 'Refresh')}
              </button>
            </div>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto px-6 py-6">
          {err && (
            <div className="mb-4 rounded-lg border border-red-500/40 bg-red-500/5 px-4 py-3 text-sm text-red-300">
              {tr('加载失败：', 'Load failed: ')}{err}
            </div>
          )}
          {loading ? (
            <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : filtered.length === 0 ? (
            <EmptyState hasItems={items.length > 0} />
          ) : (
            <div className="overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/30">
              <table className="w-full text-sm">
                <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
                  <tr>
                    <th className="px-4 py-2.5 text-left">{tr('名称', 'Name')}</th>
                    <th className="px-4 py-2.5 text-left">{tr('运行位置', 'Runs on')}</th>
                    <th className="px-4 py-2.5 text-left">{tr('类别', 'Class')}</th>
                    <th className="px-4 py-2.5 text-left">{tr('分类', 'Category')}</th>
                    <th className="px-4 py-2.5 text-left">{tr('描述', 'Description')}</th>
                    <th className="px-4 py-2.5 text-right">{tr('操作', 'Actions')}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-zinc-800/40">
                  {filtered.map((skill) => (
                    <SkillRow key={skill.key} skill={skill} onView={() => setViewing(skill)} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
        {viewing && <SkillDetailModal skill={viewing} onClose={() => setViewing(null)} />}
      </div>
  );
}

function CategoryChip({
  active,
  label,
  onClick,
}: {
  active: boolean;
  label: string;
  onClick(): void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'rounded-md border px-2 py-0.5 text-[11px] transition-colors',
        active
          ? 'border-zinc-600 bg-zinc-800 text-zinc-100'
          : 'border-zinc-800 bg-zinc-900/50 text-zinc-400 hover:bg-zinc-800/60 hover:text-zinc-200'
      )}
    >
      {label}
    </button>
  );
}

function SkillRow({ skill, onView }: { skill: SkillSummary; onView(): void }) {
  const { tr } = useI18n();
  return (
    <tr className="cursor-pointer transition-colors hover:bg-zinc-900/40" onClick={onView}>
      <td className="whitespace-nowrap px-4 py-2.5">
        <div className="flex items-center gap-1.5">
          <span className="font-medium text-zinc-100">{skill.name}</span>
          {skill.source && skill.source !== 'builtin' && (
            <span
              className="rounded bg-violet-900/40 px-1 text-[9px] font-medium text-violet-300"
              title={tr(`来自市场安装（${skill.source}）`, `installed from marketplace (${skill.source})`)}
            >
              {tr('已安装', 'installed')}
            </span>
          )}
        </div>
        <div className="mt-0.5 font-mono text-[11px] text-zinc-500" title={skill.key}>
          {skill.key}
        </div>
      </td>
      <td className="whitespace-nowrap px-4 py-2.5">
        <ScopeBadge value={skill.scope ?? 'host'} />
      </td>
      <td className="whitespace-nowrap px-4 py-2.5">
        <ClassBadge value={skill.class} />
      </td>
      <td className="whitespace-nowrap px-4 py-2.5">
        {skill.category ? (
          <span className="rounded-md border border-zinc-800 bg-zinc-950/40 px-1.5 py-0.5 text-[10px] text-zinc-400">
            {skill.category}
          </span>
        ) : (
          <span className="text-zinc-600">—</span>
        )}
      </td>
      {/* Description column is the truncate-absorber — w-full + max-w-0
          forces it to take all remaining horizontal space and clamp
          long copy, so identity columns (name / key / badges) never
          shrink below their natural width. */}
      <td className="w-full max-w-0 px-4 py-2.5 text-xs text-zinc-400">
        <div className="line-clamp-2">{skill.description || '—'}</div>
      </td>
      <td className="whitespace-nowrap px-4 py-2.5 text-right">
        {skill.inventory_only ? (
          <span
            title={tr(
              '此能力主要由 AI 助手在 chat 中调用，参数 schema 太复杂没法手动填表',
              'Mainly invoked by the AI assistant in chat — schema too complex for a manual form',
            )}
            className="text-[11px] text-zinc-500"
          >
            {tr('仅 AI 调用', 'AI only')}
          </span>
        ) : (
          <Link
            to={`/skills/${encodeURIComponent(skill.key)}`}
            onClick={(e) => e.stopPropagation()}
            className="inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800"
          >
            <Play size={11} /> {tr('执行', 'Run')}
          </Link>
        )}
      </td>
    </tr>
  );
}

// ScopeBadge tells the user where this skill physically runs. "host"
// means a tunnel RPC into the device agent (probe_*, tail_file, etc);
// "manager" means in-process on the cloud side (web_search). The same
// info gates the Run page form (manager skills hide the device picker).
export function ScopeBadge({ value }: { value: SkillScope }) {
  const { tr } = useI18n();
  const isHost = value === 'host';
  return (
    <span
      title={isHost
        ? tr('在设备上执行（需要选择设备）', 'Runs on the device (requires device selection)')
        : tr('在云端执行（无需设备）', 'Runs on the cloud (no device required)')}
      className={cn(
        'inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 text-[10px] font-medium ring-1 ring-inset',
        isHost
          ? 'bg-sky-500/10 text-sky-300 ring-sky-500/30'
          : 'bg-violet-500/10 text-violet-300 ring-violet-500/30',
      )}
    >
      {isHost ? <Cpu size={10} /> : <Cloud size={10} />}
      {isHost ? tr('设备端', 'Device') : tr('云端', 'Cloud')}
    </span>
  );
}

export function ClassBadge({ value }: { value: SkillClass }) {
  const styles: Record<SkillClass, string> = {
    safe: 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30',
    mutating: 'bg-amber-500/10 text-amber-300 ring-amber-500/30',
    dangerous: 'bg-red-500/15 text-red-300 ring-red-500/40',
  };
  return (
    <span
      className={cn(
        'rounded-md px-1.5 py-0.5 text-[10px] font-medium ring-1 ring-inset',
        styles[value],
      )}
    >
      {value}
    </span>
  );
}

// SkillDetailModal — 只读详情弹窗，参照知识库文档的阅读弹窗形态：
// 顶部元信息条（运行位置 / 类别 / 分类 / key），正文完整描述 + 参数表。
// 技能由代码或技能包定义、启动时注册进内存 registry，没有可编辑的
// 存储——所以这里只有查看，没有编辑入口。
function SkillDetailModal({ skill, onClose }: { skill: SkillSummary; onClose(): void }) {
  const { tr } = useI18n();
  return (
    <Modal open onClose={onClose} size="lg" resizable title={skill.name}>
      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <ScopeBadge value={skill.scope ?? 'host'} />
          <ClassBadge value={skill.class} />
          {skill.category && (
            <span className="rounded-md border border-zinc-800 bg-zinc-950/40 px-1.5 py-0.5 text-[10px] text-zinc-400">
              {skill.category}
            </span>
          )}
          <span className="ml-auto font-mono text-[11px] text-zinc-500">{skill.key}</span>
        </div>

        <section>
          <div className="mb-1.5 text-[11px] uppercase tracking-wider text-zinc-500">
            {tr('描述（LLM 工具描述原文）', 'Description (raw LLM tool description)')}
          </div>
          <div className="whitespace-pre-wrap rounded-md border border-zinc-800 bg-zinc-950/40 p-3 text-xs leading-relaxed text-zinc-300">
            {skill.description || '—'}
          </div>
        </section>

        <section>
          <div className="mb-1.5 text-[11px] uppercase tracking-wider text-zinc-500">
            {tr('参数', 'Parameters')}
          </div>
          {skill.params.length === 0 ? (
            <div className="text-xs text-zinc-500">
              {skill.inventory_only
                ? tr('参数为原始 JSON Schema（由 AI 在 chat 中调用，无手动表单）', 'Params are raw JSON Schema (invoked by the AI in chat; no manual form)')
                : tr('无参数', 'No parameters')}
            </div>
          ) : (
            <div className="overflow-hidden rounded-md border border-zinc-800">
              <table className="w-full text-xs">
                <thead className="border-b border-zinc-800 bg-zinc-950/40 text-[10px] uppercase tracking-wider text-zinc-500">
                  <tr>
                    <th className="px-3 py-1.5 text-left">{tr('名称', 'Name')}</th>
                    <th className="px-3 py-1.5 text-left">{tr('类型', 'Type')}</th>
                    <th className="px-3 py-1.5 text-left">{tr('必填', 'Required')}</th>
                    <th className="px-3 py-1.5 text-left">{tr('默认值', 'Default')}</th>
                    <th className="px-3 py-1.5 text-left">{tr('说明', 'Description')}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-zinc-800/60">
                  {skill.params.map((p) => (
                    <tr key={p.name}>
                      <td className="whitespace-nowrap px-3 py-1.5 font-mono text-zinc-200">{p.name}</td>
                      <td className="whitespace-nowrap px-3 py-1.5 text-zinc-400">
                        {p.type}
                        {p.enum && p.enum.length > 0 && (
                          <span className="ml-1 text-zinc-500">({p.enum.join(' | ')})</span>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-3 py-1.5 text-zinc-400">{p.required ? tr('是', 'Yes') : '—'}</td>
                      <td className="whitespace-nowrap px-3 py-1.5 font-mono text-zinc-400">
                        {p.default === undefined || p.default === null ? '—' : String(p.default)}
                      </td>
                      <td className="px-3 py-1.5 text-zinc-400">{p.desc || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>

        {skill.result_preview && (
          <section>
            <div className="mb-1.5 text-[11px] uppercase tracking-wider text-zinc-500">
              {tr('返回示意', 'Result preview')}
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950/40 p-3 font-mono text-[11px] text-zinc-400">
              {skill.result_preview}
            </div>
          </section>
        )}

        <div className="flex items-center justify-between gap-3 border-t border-zinc-800 pt-3">
          <span className="text-[11px] text-zinc-500">
            {tr('技能由代码 / 技能包定义，不可在线编辑', 'Skills are defined in code / skill packs and cannot be edited online')}
          </span>
          {skill.inventory_only ? (
            <span className="text-[11px] text-zinc-500">{tr('仅 AI 调用', 'AI only')}</span>
          ) : (
            <Link
              to={`/skills/${encodeURIComponent(skill.key)}`}
              className="inline-flex items-center gap-1.5 rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white"
            >
              <Play size={11} /> {tr('执行', 'Run')}
            </Link>
          )}
        </div>
      </div>
    </Modal>
  );
}

function EmptyState({ hasItems }: { hasItems: boolean }) {
  const { tr } = useI18n();
  return (
    <div className="flex h-60 flex-col items-center justify-center gap-2 text-zinc-500">
      <Wrench size={28} className="text-zinc-600" />
      <div className="text-sm">{hasItems ? tr('没有匹配的技能', 'No matching skills') : tr('还没有 skill 注册', 'No skills registered yet')}</div>
      <div className="text-[11px] text-zinc-600">
        {hasItems ? tr('换个关键字或清除分类筛选', 'Try a different keyword or clear filters') : tr('设备注册自己的 capability 后会出现在这里', 'Skills appear here once a device registers its capabilities')}
      </div>
    </div>
  );
}
