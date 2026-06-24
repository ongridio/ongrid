import { lazy, Suspense, useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Cloud, Cpu, Wrench, RefreshCw, Play, Search, Puzzle, ChevronDown, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/cn';
import { listSkills, localizedSkill, type SkillClass, type SkillScope, type SkillSummary } from '@/api/skills';
import { listFlowTools, type FlowToolMeta } from '@/api/flows';
import { createSession, listModels, type LLMProvider } from '@/api/chat';
import { ApiError } from '@/api/client';
import { Modal } from '@/components/Modal';
import { ChatInput, type ModelSelection } from '@/components/ChatInput';
import { useModelSelection } from '@/store/modelSelection';
import { useI18n } from '@/i18n/locale';
import { useAuth } from '@/store/auth';
import { toolGroupKey, groupTag, groupTitle, orderedGroupKeys } from '@/lib/toolSkill';
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
        subtitle={tr('LLM 当前可见的能力，以及安装 / 管理扩展', 'Capabilities the LLM can use — plus installing / managing extensions')}
        extra={
          isAdmin ? (
            <div className="-mb-4 flex items-center gap-1">
              <TabButton active={tab === 'catalog'} onClick={() => setTab('catalog')} icon={<Wrench size={14} />} label={tr('技能目录', 'Catalog')} />
              <TabButton active={tab === 'install'} onClick={() => setTab('install')} icon={<Puzzle size={14} />} label={tr('扩展', 'Extensions')} />
            </div>
          ) : undefined
        }
      />
      {tab === 'install' ? (
        <div className="flex-1 overflow-auto px-6 py-4">
          <InstallChatBar />
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

// InstallChatBar — conversational install entry on the Extensions tab. Mirrors
// the Home composer: it doesn't run the chat inline (so the human-approval card
// + streaming live in the real thread), it opens a fresh session pre-seeded with
// the user's source/intent so the agent's install_skill tool takes it from there.
function InstallChatBar() {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  // Same model wiring as the Home composer: load the catalog default and share
  // the global selection store, so the picker shows the default model (not
  // "未配置模型") and a change here is reflected on Home too.
  const [providers, setProviders] = useState<LLMProvider[]>([]);
  const storeModel = useModelSelection((s) => s.selected);
  const setStoreModel = useModelSelection((s) => s.setSelected);
  const [catalogDefault, setCatalogDefault] = useState<ModelSelection | null>(null);
  const selectedModel = storeModel ?? catalogDefault;
  useEffect(() => {
    let cancelled = false;
    listModels()
      .then((cat) => {
        if (cancelled) return;
        setProviders(cat.providers ?? []);
        if (cat.default && cat.default.provider) {
          setCatalogDefault({ provider: cat.default.provider, model: cat.default.model || '' });
        }
      })
      .catch(() => {
        if (!cancelled) setProviders([]);
      });
    return () => {
      cancelled = true;
    };
  }, []);
  const start = async (text: string) => {
    const v = text.trim();
    if (!v || busy) return;
    setBusy(true);
    try {
      const session = await createSession({ title: tr('安装扩展', 'Install extension'), agent_id: 'default' });
      const prompt = tr(
        `帮我安装一个扩展。来源或需求：${v}`,
        `Help me install an extension. Source or request: ${v}`,
      );
      navigate(`/chat/${session.id}`, { state: { initialPrompt: prompt } });
    } catch {
      setBusy(false);
    }
  };
  // Reuse the Home composer (ChatInput) so the conversational-install entry
  // looks and behaves like the main chat box — wide, single composer. It opens
  // a fresh thread pre-seeded with the request; the agent's install_skill tool
  // (+ the human-approval card) takes over there.
  return (
    <div className="mb-5">
      <ChatInput
        value={draft}
        onChange={setDraft}
        onSubmit={(p) => void start(p.text)}
        placeholder={tr(
          '贴个技能源（git / tarball / skills.sh），或描述你想要的能力，交给助手装…',
          'Paste a skill source (git / tarball / skills.sh), or describe a capability — let the assistant install it…',
        )}
        disabled={busy}
        providers={providers}
        selectedModel={selectedModel}
        onModelChange={(sel) => setStoreModel(sel)}
      />
    </div>
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

// mcpToSkill maps an MCP flow tool into a Skills-page inventory item. MCP
// carries no read/write signal, so class is shown gently (read→safe, else
// →mutating, not alarmist red). source="mcp" drives the tag.
function mcpToSkill(t: FlowToolMeta): SkillSummary {
  return {
    key: t.name,
    name: t.display_zh || t.name,
    description: t.description_zh || t.description || '',
    class: t.class === 'read' ? 'safe' : 'mutating',
    scope: 'manager',
    category: t.category,
    params: [],
    source: 'mcp',
    inventory_only: true,
  };
}

function CatalogTab() {
  const { tr, locale } = useI18n();
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
      // The Skills page is the LLM's full visible-capability inventory, so it
      // also pulls MCP tools (which live outside the skill registry) and shows
      // them as inventory items tagged "mcp", grouped under their server.
      const [sk, ft] = await Promise.all([
        listSkills(),
        listFlowTools().catch(() => ({ items: [] as FlowToolMeta[] })),
      ]);
      const base = (sk.items ?? []).map(localizedSkill);
      const mcp = (ft.items ?? []).filter((t) => t.name.startsWith('mcp__')).map(mcpToSkill);
      setItems([...base, ...mcp]);
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

  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const toggleGroup = (k: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      next.has(k) ? next.delete(k) : next.add(k);
      return next;
    });

  // The single canonical grouping (shared with the flow palette): built-in
  // tools → 6 curated skills, MCP → per-server, SKILL.md packs → per-pack.
  // Keyed by the WIRE name (s.key), not the localized display name.
  const skillKey = useCallback((s: SkillSummary) => toolGroupKey(s.key, s.source, s.category), []);
  const skills = useMemo(() => orderedGroupKeys(items.map(skillKey)), [items, skillKey]);

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
      if (category && skillKey(s) !== category) return false;
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
            {skills.length > 0 && (
              <div className="inline-flex items-center gap-1.5 text-xs text-zinc-400">
                <span className="text-zinc-500">{tr('技能', 'Skill')}</span>
                <div className="flex flex-wrap gap-1">
                  <CategoryChip
                    active={category === ''}
                    label={tr('全部', 'All')}
                    onClick={() => setCategory('')}
                  />
                  {skills.map((c) => (
                    <CategoryChip
                      key={c}
                      active={category === c}
                      label={groupTitle(c, locale === 'zh-CN', items.find((s) => skillKey(s) === c)?.name)}
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
                    <th className="px-4 py-2.5 text-left">{tr('描述', 'Description')}</th>
                    <th className="px-4 py-2.5 text-right">{tr('操作', 'Actions')}</th>
                  </tr>
                </thead>
                {orderedGroupKeys(filtered.map(skillKey)).map((gk) => {
                  const rows = filtered.filter((s) => skillKey(s) === gk);
                  const tag = groupTag(gk);
                  const isCollapsed = collapsed.has(gk);
                  return (
                    <tbody key={gk} className="divide-y divide-zinc-800/40">
                      <tr
                        className="cursor-pointer select-none border-t border-zinc-800/60 bg-zinc-950/50 hover:bg-zinc-900/50"
                        onClick={() => toggleGroup(gk)}
                      >
                        <td colSpan={5} className="px-3 py-2">
                          <div className="flex items-center gap-1.5">
                            {isCollapsed ? <ChevronRight size={13} className="text-zinc-500" /> : <ChevronDown size={13} className="text-zinc-500" />}
                            <span className="text-[13px] font-semibold text-zinc-200">
                              {groupTitle(gk, locale === 'zh-CN', rows[0]?.name)}
                            </span>
                            {tag === 'mcp' && <span className="rounded bg-sky-900/40 px-1 text-[9px] font-medium text-sky-300">mcp</span>}
                            {tag === 'ext' && <span className="rounded bg-violet-900/40 px-1 text-[9px] font-medium text-violet-300">{tr('扩展', 'extension')}</span>}
                            <span className="ml-1 text-[11px] text-zinc-500">{rows.length} {tr('个工具', 'tools')}</span>
                          </div>
                        </td>
                      </tr>
                      {!isCollapsed && rows.map((skill) => <SkillRow key={skill.key} skill={skill} onView={() => setViewing(skill)} />)}
                    </tbody>
                  );
                })}
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
      <td className="whitespace-nowrap px-4 py-1.5 pl-8">
        <span className="text-[13px] text-zinc-200">{skill.name}</span>
        <div className="font-mono text-[10px] text-zinc-600" title={skill.key}>
          {skill.key}
        </div>
      </td>
      <td className="whitespace-nowrap px-4 py-1.5">
        <ScopeBadge value={skill.scope ?? 'host'} />
      </td>
      <td className="whitespace-nowrap px-4 py-1.5">
        <ClassBadge value={skill.class} />
      </td>
      {/* Description column is the truncate-absorber — w-full + max-w-0
          forces it to take all remaining horizontal space and clamp
          long copy, so identity columns (name / key / badges) never
          shrink below their natural width. */}
      <td className="w-full max-w-0 px-4 py-1.5 text-[11px] text-zinc-500">
        <div className="line-clamp-1">{skill.description || '—'}</div>
      </td>
      <td className="whitespace-nowrap px-4 py-1.5 text-right">
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
