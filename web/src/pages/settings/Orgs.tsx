import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Building2,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  UserMinus,
  UserPlus,
} from 'lucide-react';
import { ApiError } from '@/api/client';
import {
  addOrgMember,
  createOrg,
  deleteOrg,
  listOrgMembers,
  listOrgs,
  removeOrgMember,
  setOrgMemberRole,
  updateOrg,
  type Org,
  type OrgMember,
  type OrgRole,
} from '@/api/orgs';
import { listUsers, type User } from '@/api/users';
import { Modal } from '@/components/Modal';
import { Button, Card, Chip, EmptyState, PageHeader } from '@/components/ui';
import { cn } from '@/lib/cn';
import { tr as trInline, useI18n } from '@/i18n/locale';

// /settings/orgs —— 组织 + 成员管理。任何登录用户可见。
//
// 布局：
//   左列    组织列表（搜索 + 卡片）+ 顶部「新建组织」
//   右列    成员表（角色 dropdown / 移除）+ 顶部组织信息（编辑 / 删除）
//
// 默认组织保护：name === '默认组织' 时 disable 删除按钮 + 提示文案。
const DEFAULT_ORG_NAME = '默认组织';
const ROLE_OPTIONS: OrgRole[] = ['org_admin', 'member', 'viewer'];

const ROLE_TONE: Record<OrgRole, 'info' | 'default' | 'accent'> = {
  org_admin: 'info',
  member: 'default',
  // 设计稿写 viewer = subtle；项目 Chip 没有 subtle 档，用 default 但视觉再淡一点不必要——
  // 直接 default，用户区分度足够。如果以后非要拉开，可以加 'subtle' tone。
  viewer: 'default',
};

// Role-tinted styling for the inline editable <select> in the member
// list. Mirrors what Chip tone="info" / default would render, so the
// row reads as a status pill while still being editable.
const ROLE_SELECT_CLASS: Record<OrgRole, string> = {
  org_admin: 'border-sky-500/40 bg-sky-500/10 text-sky-300',
  member: 'border-zinc-700 bg-zinc-800/60 text-zinc-300',
  viewer: 'border-zinc-700 bg-zinc-800/60 text-zinc-400',
};

const ROLE_LABEL_ZH: Record<OrgRole, string> = {
  org_admin: '组织管理员',
  member: '成员',
  viewer: '只读',
};

const ROLE_LABEL_EN: Record<OrgRole, string> = {
  org_admin: 'Org admin',
  member: 'Member',
  viewer: 'Viewer',
};

function roleLabel(r: OrgRole): string {
  return trInline(ROLE_LABEL_ZH[r], ROLE_LABEL_EN[r]);
}

const ROLE_LABEL = new Proxy({} as Record<OrgRole, string>, {
  get: (_t, key: string) => roleLabel(key as OrgRole),
});

export default function SettingsOrgs() {
  const { tr } = useI18n();
  const [orgs, setOrgs] = useState<Org[]>([]);
  const [orgsLoading, setOrgsLoading] = useState(false);
  const [orgsError, setOrgsError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [search, setSearch] = useState('');

  const [members, setMembers] = useState<OrgMember[]>([]);
  const [membersLoading, setMembersLoading] = useState(false);
  const [membersError, setMembersError] = useState<string | null>(null);

  const [createOrgOpen, setCreateOrgOpen] = useState(false);
  const [editOrg, setEditOrg] = useState<Org | null>(null);
  const [delOrg, setDelOrg] = useState<Org | null>(null);
  const [addMemberOpen, setAddMemberOpen] = useState(false);
  const [removeMember, setRemoveMember] = useState<OrgMember | null>(null);

  const [toast, setToast] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const refreshOrgs = useCallback(async () => {
    setOrgsLoading(true);
    setOrgsError(null);
    try {
      const resp = await listOrgs();
      const items = resp.items ?? [];
      setOrgs(items);
      // 默认选中第一个；当前选中的若被删了也回退到第一个
      setSelectedId((cur) => {
        if (cur && items.some((o) => o.id === cur)) return cur;
        return items[0]?.id ?? null;
      });
    } catch (e) {
      setOrgsError(errMsg(e));
    } finally {
      setOrgsLoading(false);
    }
  }, []);

  useEffect(() => {
    void refreshOrgs();
  }, [refreshOrgs]);

  const refreshMembers = useCallback(async (orgId: number) => {
    setMembersLoading(true);
    setMembersError(null);
    try {
      const resp = await listOrgMembers(orgId);
      setMembers(resp.items ?? []);
    } catch (e) {
      setMembersError(errMsg(e));
    } finally {
      setMembersLoading(false);
    }
  }, []);

  useEffect(() => {
    if (selectedId == null) {
      setMembers([]);
      return;
    }
    void refreshMembers(selectedId);
  }, [selectedId, refreshMembers]);

  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), 4000);
    return () => window.clearTimeout(t);
  }, [toast]);

  // depthOf：把 org 在 tree 中的深度算出来（顶级 = 0），列表 indent 用。
  // 树是 by parent_id 链接，环已被后端 cycle check 挡住。
  const depthOf = useMemo(() => {
    const m = new Map<number, Org>();
    for (const o of orgs) m.set(o.id, o);
    const cache = new Map<number, number>();
    const get = (id: number, hops = 0): number => {
      if (hops > 1024) return 0; // 防御
      const cached = cache.get(id);
      if (cached != null) return cached;
      const cur = m.get(id);
      if (!cur || cur.parent_id == null) {
        cache.set(id, 0);
        return 0;
      }
      const d = get(cur.parent_id, hops + 1) + 1;
      cache.set(id, d);
      return d;
    };
    return (id: number) => get(id);
  }, [orgs]);

  const filteredOrgs = useMemo(() => {
    // 排序：parent 在前、子在后；同层按 id asc。flattenOrgTree 已经做了
    // 完整 DFS，这里复用它得到稳定的列表顺序。
    const flat = flattenOrgTree(orgs);
    const idIndex = new Map<number, Org>();
    for (const o of orgs) idIndex.set(o.id, o);
    const ordered = flat.map((f) => idIndex.get(f.id)!).filter(Boolean);

    const q = search.trim().toLowerCase();
    if (!q) return ordered;
    return ordered.filter(
      (o) =>
        o.name.toLowerCase().includes(q) ||
        (o.description ?? '').toLowerCase().includes(q),
    );
  }, [orgs, search]);

  const selected = useMemo(
    () => orgs.find((o) => o.id === selectedId) ?? null,
    [orgs, selectedId],
  );

  const isDefaultOrg = (o: Org | null) => !!o && o.name === DEFAULT_ORG_NAME;

  // The seed org's name + description live in the DB as Chinese sentinels
  // (the name is matched verbatim by the backend, so it can't be changed).
  // Localize them at display time so English mode isn't stuck with Chinese;
  // a renamed org no longer matches DEFAULT_ORG_NAME and shows its own values.
  const orgDisplayName = (o: Org) =>
    isDefaultOrg(o) ? tr('默认组织', 'Default organization') : o.name;
  const orgDisplayDesc = (o: Org) =>
    isDefaultOrg(o)
      ? tr(
          '首次部署的默认组织，所有现有用户自动加入。可以保留或重命名。',
          'The default organization created on first deploy; all existing users join automatically. Keep it or rename it.',
        )
      : o.description;

  // ---------- handlers --------

  const onOrgCreated = (o: Org) => {
    setOrgs((cur) => [...cur, o].sort((a, b) => a.id - b.id));
    setSelectedId(o.id);
    setCreateOrgOpen(false);
    setToast({ kind: 'ok', text: tr(`已创建 ${o.name}`, `Created ${o.name}`) });
  };

  const onOrgUpdated = (o: Org) => {
    setOrgs((cur) => cur.map((x) => (x.id === o.id ? o : x)));
    setEditOrg(null);
    setToast({ kind: 'ok', text: tr(`已更新 ${o.name}`, `Updated ${o.name}`) });
  };

  const doDeleteOrg = async (o: Org) => {
    try {
      await deleteOrg(o.id);
      setOrgs((cur) => cur.filter((x) => x.id !== o.id));
      setDelOrg(null);
      if (selectedId === o.id) setSelectedId(null);
      setToast({ kind: 'ok', text: tr(`已删除 ${o.name}`, `Deleted ${o.name}`) });
    } catch (e) {
      setToast({ kind: 'err', text: errMsg(e) });
    }
  };

  const onMemberAdded = (m: OrgMember) => {
    setMembers((cur) => {
      // 同一 user_id 不重复插入
      if (cur.some((x) => x.user_id === m.user_id)) {
        return cur.map((x) => (x.user_id === m.user_id ? m : x));
      }
      return [...cur, m];
    });
    setAddMemberOpen(false);
    setToast({ kind: 'ok', text: tr(`已添加 ${m.email}`, `Added ${m.email}`) });
  };

  const changeRole = async (m: OrgMember, role: OrgRole) => {
    if (selectedId == null || m.role === role) return;
    try {
      await setOrgMemberRole(selectedId, m.user_id, role);
      // PATCH returns 204, so retain the row's identity/email fields and
      // update only the role after the server confirms the mutation.
      setMembers((cur) => cur.map((x) => (x.user_id === m.user_id ? { ...x, role } : x)));
      setToast({ kind: 'ok', text: `${m.email} → ${ROLE_LABEL[role]}` });
    } catch (e) {
      setToast({ kind: 'err', text: errMsg(e) });
    }
  };

  const doRemoveMember = async (m: OrgMember) => {
    if (selectedId == null) return;
    try {
      await removeOrgMember(selectedId, m.user_id);
      setMembers((cur) => cur.filter((x) => x.user_id !== m.user_id));
      setRemoveMember(null);
      setToast({ kind: 'ok', text: tr(`已移除 ${m.email}`, `Removed ${m.email}`) });
    } catch (e) {
      setToast({ kind: 'err', text: errMsg(e) });
    }
  };

  return (
    <div className="space-y-4 p-6">
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <Building2 size={14} className="text-zinc-400" />
            {tr('组织', 'Organizations')}
          </span>
        }
        subtitle={tr('管理组织与组织成员', 'Manage organizations and their members')}
        className="border-0 px-0 py-0"
        actions={
          <>
            <Button onClick={refreshOrgs} disabled={orgsLoading}>
              {orgsLoading ? <Loader2 size={11} className="animate-spin" /> : <RefreshCw size={11} />}
              {tr('刷新', 'Refresh')}
            </Button>
            <Button variant="primary" onClick={() => setCreateOrgOpen(true)}>
              <Plus size={12} />
              {tr('新建组织', 'New org')}
            </Button>
          </>
        }
      />

      {orgsError && (
        <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
          {orgsError}
        </div>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[240px_minmax(0,1fr)]">
        {/* 左：组织列表 */}
        <Card className="p-3">
          <div className="mb-2 flex items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-950/60 px-2">
            <Search size={12} className="text-zinc-500" />
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={tr('搜索组织', 'Search organizations')}
              className="flex-1 bg-transparent py-1.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:outline-none"
            />
          </div>

          {orgsLoading && orgs.length === 0 ? (
            <div className="flex h-24 items-center justify-center text-sm text-zinc-500">
              <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
            </div>
          ) : filteredOrgs.length === 0 ? (
            <EmptyState
              title={search ? tr('没有匹配的组织', 'No matching organizations') : tr('还没有组织', 'No organizations yet')}
              hint={search ? undefined : tr('点上方「新建组织」', 'Click "New org" above')}
              className="flex h-32 flex-col items-center justify-center gap-2 text-center"
            />
          ) : (
            <ul className="space-y-1">
              {filteredOrgs.map((o) => {
                const active = o.id === selectedId;
                const depth = depthOf(o.id);
                return (
                  <li key={o.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(o.id)}
                      style={{ paddingLeft: `${10 + depth * 14}px` }}
                      className={cn(
                        'group flex w-full items-start gap-2 rounded-md py-2 pr-2.5 text-left transition-colors',
                        active
                          ? 'bg-zinc-800 text-zinc-100'
                          : 'text-zinc-400 hover:bg-zinc-800/60 hover:text-zinc-100',
                      )}
                    >
                      <Building2
                        size={13}
                        className={cn(
                          'mt-0.5 shrink-0',
                          active ? 'text-accent' : 'text-zinc-500 group-hover:text-zinc-300',
                        )}
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-1.5">
                          <span className="truncate text-[13px] font-medium">{orgDisplayName(o)}</span>
                          {isDefaultOrg(o) && (
                            <Chip dense tone="info">
                              {tr('默认', 'Default')}
                            </Chip>
                          )}
                        </div>
                        {orgDisplayDesc(o) && (
                          <div className="mt-0.5 line-clamp-1 text-[11px] text-zinc-500">
                            {orgDisplayDesc(o)}
                          </div>
                        )}
                      </div>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </Card>

        {/* 右：成员管理 */}
        <Card className="p-0">
          {!selected ? (
            <EmptyState
              icon={Building2}
              title={tr('请选择一个组织', 'Please select an organization')}
              hint={tr('从左侧选中组织后，可查看 / 编辑成员', 'Pick an organization on the left to view / edit its members')}
              className="flex h-60 flex-col items-center justify-center gap-2 text-center"
            />
          ) : (
            <div>
              <div className="flex flex-wrap items-start justify-between gap-3 border-b border-zinc-800/60 px-4 py-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <h2 className="truncate text-sm font-semibold text-zinc-100">{orgDisplayName(selected)}</h2>
                    {isDefaultOrg(selected) && <Chip tone="info">{tr('默认组织', 'Default org')}</Chip>}
                  </div>
                  <div className="mt-0.5 text-[11px] text-zinc-500">
                    {orgDisplayDesc(selected) || <span className="text-zinc-600">{tr('（无描述）', '(no description)')}</span>}
                  </div>
                </div>
                <div className="flex shrink-0 flex-wrap items-center gap-1.5">
                  <Button onClick={() => setEditOrg(selected)}>
                    <Pencil size={11} />
                    {tr('编辑', 'Edit')}
                  </Button>
                  <Button
                    variant="danger"
                    onClick={() => setDelOrg(selected)}
                    disabled={isDefaultOrg(selected)}
                    title={isDefaultOrg(selected) ? tr('默认组织不能删除', 'The default org cannot be deleted') : tr('删除组织', 'Delete org')}
                  >
                    <Trash2 size={11} />
                    {tr('删除', 'Delete')}
                  </Button>
                </div>
              </div>

              <div className="flex items-center justify-between gap-2 border-b border-zinc-800/40 px-4 py-2.5">
                <div className="flex items-center gap-2 text-xs text-zinc-400">
                  {tr('成员', 'Members')} <span className="text-zinc-200">{members.length}</span>
                  {membersLoading && <Loader2 size={11} className="animate-spin text-zinc-500" />}
                </div>
                {/* Refresh button removed — the page-level 刷新 in the
                    header already re-fetches both orgs and members, so
                    a second refresh in the member section was just
                    visual clutter. */}
                <Button variant="primary" onClick={() => setAddMemberOpen(true)}>
                  <UserPlus size={12} />
                  {tr('添加成员', 'Add member')}
                </Button>
              </div>

              {membersError && (
                <div className="m-4 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
                  {membersError}
                </div>
              )}

              {membersLoading && members.length === 0 ? (
                <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
                  <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
                </div>
              ) : members.length === 0 ? (
                <EmptyState
                  title={tr('还没有成员', 'No members yet')}
                  hint={tr('点右上角「添加成员」从用户列表挑选', 'Use "Add member" in the top right to pick from the user list')}
                  className="flex h-40 flex-col items-center justify-center gap-2 text-center"
                />
              ) : (
                <MemberTable
                  members={members}
                  onRoleChange={changeRole}
                  onRemove={setRemoveMember}
                />
              )}
            </div>
          )}
        </Card>
      </div>

      <CreateOrgModal
        open={createOrgOpen}
        orgs={orgs}
        onClose={() => setCreateOrgOpen(false)}
        onCreated={onOrgCreated}
      />

      <EditOrgModal
        target={editOrg}
        orgs={orgs}
        onClose={() => setEditOrg(null)}
        onSaved={onOrgUpdated}
      />

      <Modal
        open={!!delOrg}
        onClose={() => setDelOrg(null)}
        title={delOrg ? tr(`删除 ${delOrg.name}?`, `Delete ${delOrg.name}?`) : tr('删除组织', 'Delete org')}
        size="sm"
        footer={
          <>
            <Button onClick={() => setDelOrg(null)}>{tr('取消', 'Cancel')}</Button>
            <Button variant="danger" onClick={() => delOrg && doDeleteOrg(delOrg)}>
              <Trash2 size={12} />
              {tr('确认删除', 'Delete')}
            </Button>
          </>
        }
      >
        <p className="text-sm text-zinc-300">
          {tr('删除组织会一并清除所有成员关系。该操作不可逆。', 'Deleting an organization also removes all of its memberships. This cannot be undone.')}
        </p>
      </Modal>

      <AddMemberModal
        open={addMemberOpen}
        orgId={selectedId}
        existingIds={members.map((m) => m.user_id)}
        onClose={() => setAddMemberOpen(false)}
        onAdded={onMemberAdded}
      />

      <Modal
        open={!!removeMember}
        onClose={() => setRemoveMember(null)}
        title={removeMember ? tr(`移除 ${removeMember.email}?`, `Remove ${removeMember.email}?`) : tr('移除成员', 'Remove member')}
        size="sm"
        footer={
          <>
            <Button onClick={() => setRemoveMember(null)}>{tr('取消', 'Cancel')}</Button>
            <Button variant="danger" onClick={() => removeMember && doRemoveMember(removeMember)}>
              <UserMinus size={12} />
              {tr('确认移除', 'Remove')}
            </Button>
          </>
        }
      >
        <p className="text-sm text-zinc-300">
          {tr('移除后，该成员失去对此组织的访问权；用户账号本身不受影响。', 'After removal the user loses access to this organization; their account itself is unaffected.')}
        </p>
      </Modal>

      {toast && (
        <div
          role="status"
          className={cn(
            'fixed bottom-6 right-6 z-50 max-w-sm rounded-lg px-4 py-2.5 text-sm shadow-2xl ring-1 ring-inset',
            toast.kind === 'ok'
              ? 'bg-emerald-500/15 text-emerald-200 ring-emerald-500/40'
              : 'bg-red-500/15 text-red-200 ring-red-500/40',
          )}
        >
          {toast.text}
        </div>
      )}
    </div>
  );
}

// ---------- members table ----------------------------------------------------

function MemberTable({
  members,
  onRoleChange,
  onRemove,
}: {
  members: OrgMember[];
  onRoleChange: (m: OrgMember, role: OrgRole) => void;
  onRemove: (m: OrgMember) => void;
}) {
  const { tr } = useI18n();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-zinc-800/60 text-left text-[11px] uppercase tracking-wide text-zinc-500">
            <th className="px-4 py-2.5 font-medium">{tr('姓名', 'Name')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('邮箱', 'Email')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('角色', 'Role')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('操作', 'Actions')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/40">
          {members.map((m) => (
            <tr key={m.user_id} className="hover:bg-zinc-900/40">
              <td className="px-4 py-2.5 text-zinc-100">
                {m.display_name || <span className="text-zinc-600">—</span>}
              </td>
              <td className="px-4 py-2.5 font-mono text-[12px] text-zinc-300">{m.email}</td>
              <td className="px-4 py-2.5">
                {/* Single editable role control — the standalone Chip
                    that previously sat next to the select was just a
                    duplicate read-out. Native <select> styled to match
                    the chip tone so it still reads as a status pill at
                    a glance. */}
                <select
                  value={m.role}
                  onChange={(e) => onRoleChange(m, e.target.value as OrgRole)}
                  className={cn(
                    'rounded-md border px-2 py-1 text-xs focus:outline-none',
                    ROLE_SELECT_CLASS[m.role],
                  )}
                >
                  {ROLE_OPTIONS.map((r) => (
                    <option key={r} value={r} className="bg-zinc-900 text-zinc-100">
                      {ROLE_LABEL[r]}
                    </option>
                  ))}
                </select>
              </td>
              <td className="px-3 py-2.5 text-right">
                <Button variant="danger" onClick={() => onRemove(m)} className="whitespace-nowrap">
                  <UserMinus size={11} />
                  {tr('移除', 'Remove')}
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---------- helpers (org tree) ----------------------------------------------

// flattenOrgTree: list 自上而下 + 缩进前缀，方便 dropdown 视觉表达层级。
// 父在前、子在后；root 们按 id asc。看似 O(N²) 但 org 数 < 100，无所谓。
type FlatOrg = { id: number; depth: number; label: string };
function flattenOrgTree(orgs: Org[]): FlatOrg[] {
  const byParent = new Map<number | null, Org[]>();
  for (const o of orgs) {
    const k = o.parent_id ?? null;
    const arr = byParent.get(k) ?? [];
    arr.push(o);
    byParent.set(k, arr);
  }
  const out: FlatOrg[] = [];
  const visit = (parent: number | null, depth: number) => {
    const kids = (byParent.get(parent) ?? []).slice().sort((a, b) => a.id - b.id);
    for (const k of kids) {
      out.push({
        id: k.id,
        depth,
        label: `${'  '.repeat(depth)}${depth > 0 ? '└ ' : ''}${k.name}`,
      });
      visit(k.id, depth + 1);
    }
  };
  visit(null, 0);
  return out;
}

// descendantIDs: 给定 org 的所有后代 id（含自己），用来 edit 模态时
// 从父 org 候选里排除——选自己或后代会触发 cycle。
function descendantIDs(orgs: Org[], rootId: number): Set<number> {
  const childrenOf = new Map<number, Org[]>();
  for (const o of orgs) {
    if (o.parent_id == null) continue;
    const arr = childrenOf.get(o.parent_id) ?? [];
    arr.push(o);
    childrenOf.set(o.parent_id, arr);
  }
  const out = new Set<number>([rootId]);
  const stack = [rootId];
  while (stack.length) {
    const top = stack.pop()!;
    for (const c of childrenOf.get(top) ?? []) {
      if (out.has(c.id)) continue;
      out.add(c.id);
      stack.push(c.id);
    }
  }
  return out;
}

// ---------- modals -----------------------------------------------------------

function CreateOrgModal({
  open,
  orgs,
  onClose,
  onCreated,
}: {
  open: boolean;
  orgs: Org[];
  onClose: () => void;
  onCreated: (o: Org) => void;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState('');
  const [desc, setDesc] = useState('');
  // 默认组织 自身是唯一允许的顶级 org（在 IAM 初始化时由 EnsureSeed 创建）。
  // 任何新建的 org 必须挂在某个父之下；缺省 parent 就是默认组织。
  const defaultOrgId = useMemo(
    () => orgs.find((o) => o.name === DEFAULT_ORG_NAME)?.id ?? null,
    [orgs],
  );
  const [parentId, setParentId] = useState<string>('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setName('');
      setDesc('');
      setParentId(defaultOrgId != null ? String(defaultOrgId) : '');
      setBusy(false);
      setErr(null);
    }
  }, [open, defaultOrgId]);

  // 候选父 org = 全部 org。"顶级"选项隐藏（不显示），强制挂载。
  const flat = useMemo(() => flattenOrgTree(orgs), [orgs]);

  const submit = async () => {
    if (!name.trim()) return;
    if (!parentId) {
      setErr(tr('请选择父组织（所有新组织必须挂在默认组织或其子组织下）', 'Pick a parent org (every new org must hang under the default org or one of its descendants)'));
      return;
    }
    setBusy(true);
    setErr(null);
    try {
      const o = await createOrg({
        name: name.trim(),
        description: desc.trim(),
        parent_id: Number(parentId),
      });
      onCreated(o);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={tr('新建组织', 'New organization')}
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={submit} disabled={busy || !name.trim()}>
            {busy ? <Loader2 size={12} className="animate-spin" /> : <Plus size={12} />}
            {tr('创建', 'Create')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label={tr('组织名', 'Org name')} required>
          <input className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={tr('父组织', 'Parent org')} required hint={tr('所有新建组织必须挂在默认组织或其子组织之下', 'Every new org must hang under the default org or one of its descendants')}>
          <select
            className={inputClass}
            value={parentId}
            onChange={(e) => setParentId(e.target.value)}
          >
            {flat.map((o) => (
              <option key={o.id} value={String(o.id)}>
                {o.label}
              </option>
            ))}
          </select>
        </Field>
        <Field label={tr('描述', 'Description')}>
          <textarea
            className={cn(inputClass, 'min-h-[72px] resize-y')}
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
          />
        </Field>
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}
      </div>
    </Modal>
  );
}

function EditOrgModal({
  target,
  orgs,
  onClose,
  onSaved,
}: {
  target: Org | null;
  orgs: Org[];
  onClose: () => void;
  onSaved: (o: Org) => void;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState('');
  const [desc, setDesc] = useState('');
  const [parentId, setParentId] = useState<string>('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (target) {
      setName(target.name);
      setDesc(target.description ?? '');
      setParentId(target.parent_id != null ? String(target.parent_id) : '');
      setBusy(false);
      setErr(null);
    }
  }, [target]);

  // 父 org 候选从 flat 列表里去掉自己 + 自己的所有后代（防 cycle）。
  const parentOptions = useMemo(() => {
    if (!target) return flattenOrgTree(orgs);
    const blocked = descendantIDs(orgs, target.id);
    return flattenOrgTree(orgs).filter((o) => !blocked.has(o.id));
  }, [orgs, target]);

  const isDefault = target?.name === DEFAULT_ORG_NAME;

  const submit = async () => {
    if (!target || !name.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      const nextParent = parentId ? Number(parentId) : null;
      const currentParent = target.parent_id ?? null;
      const parentChanged = nextParent !== currentParent;
      const o = await updateOrg(target.id, {
        name: name.trim(),
        description: desc.trim(),
        parent_id_set: parentChanged,
        parent_id: nextParent,
      });
      onSaved(o);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={!!target}
      onClose={onClose}
      title={target ? tr(`编辑 ${target.name}`, `Edit ${target.name}`) : tr('编辑组织', 'Edit organization')}
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={submit} disabled={busy || !name.trim()}>
            {busy ? <Loader2 size={12} className="animate-spin" /> : <Pencil size={12} />}
            {tr('保存', 'Save')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label={tr('组织名', 'Org name')} required hint={isDefault ? tr('默认组织名称建议保持不变', 'Recommended to keep the default org name unchanged') : undefined}>
          <input className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field
          label={tr('父组织', 'Parent org')}
          hint={
            target?.name === DEFAULT_ORG_NAME
              ? tr('默认组织是根组织，不可移动', 'The default org is the root and cannot be moved')
              : tr('所有组织必须挂在默认组织或其子组织下；自身和后代已自动排除', 'All orgs must hang under the default org or one of its descendants; self and descendants are excluded automatically')
          }
        >
          <select
            className={inputClass}
            value={parentId}
            disabled={target?.name === DEFAULT_ORG_NAME}
            onChange={(e) => setParentId(e.target.value)}
          >
            {target?.name === DEFAULT_ORG_NAME && (
              <option value="">{tr('（根组织）', '(root org)')}</option>
            )}
            {parentOptions.map((o) => (
              <option key={o.id} value={String(o.id)}>
                {o.label}
              </option>
            ))}
          </select>
        </Field>
        <Field label={tr('描述', 'Description')}>
          <textarea
            className={cn(inputClass, 'min-h-[72px] resize-y')}
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
          />
        </Field>
        {isDefault && (
          <div className="rounded-md border border-sky-500/40 bg-sky-500/10 px-3 py-2 text-xs text-sky-200">
            {tr('默认组织不能删除，只能改名 / 描述。', 'The default org cannot be deleted — only renamed or re-described.')}
          </div>
        )}
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}
      </div>
    </Modal>
  );
}

function AddMemberModal({
  open,
  orgId,
  existingIds,
  onClose,
  onAdded,
}: {
  open: boolean;
  orgId: number | null;
  existingIds: number[];
  onClose: () => void;
  onAdded: (m: OrgMember) => void;
}) {
  const { tr } = useI18n();
  const [users, setUsers] = useState<User[]>([]);
  const [usersLoading, setUsersLoading] = useState(false);
  const [usersError, setUsersError] = useState<string | null>(null);
  const [pickedId, setPickedId] = useState<number | null>(null);
  const [role, setRole] = useState<OrgRole>('member');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [search, setSearch] = useState('');

  useEffect(() => {
    if (!open) return;
    setPickedId(null);
    setRole('member');
    setBusy(false);
    setErr(null);
    setSearch('');
    setUsersLoading(true);
    setUsersError(null);
    listUsers()
      .then((resp) => setUsers(resp.items ?? []))
      .catch((e) => {
        // 普通用户大概率没有 /v1/users 权限（admin-only），给个友好提示。
        if (e instanceof ApiError && e.status === 403) {
          setUsersError(tr('当前账号无权读取用户列表，请联系管理员添加成员', 'Your account cannot read the user list — ask an admin to add members'));
        } else {
          setUsersError(errMsg(e));
        }
      })
      .finally(() => setUsersLoading(false));
  }, [open]);

  const candidates = useMemo(() => {
    const q = search.trim().toLowerCase();
    const ids = new Set(existingIds);
    return users
      .filter((u) => !ids.has(u.id) && u.status === 'active')
      .filter((u) => {
        if (!q) return true;
        return (
          u.email.toLowerCase().includes(q) ||
          (u.display_name ?? '').toLowerCase().includes(q)
        );
      });
  }, [users, existingIds, search]);

  const submit = async () => {
    if (orgId == null || pickedId == null) return;
    setBusy(true);
    setErr(null);
    try {
      const m = await addOrgMember(orgId, { user_id: pickedId, role });
      onAdded(m);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={tr('添加成员', 'Add member')}
      size="md"
      footer={
        <>
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={submit} disabled={busy || pickedId == null}>
            {busy ? <Loader2 size={12} className="animate-spin" /> : <UserPlus size={12} />}
            {tr('添加', 'Add')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="flex items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-950/60 px-2">
          <Search size={12} className="text-zinc-500" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={tr('搜索用户（姓名 / 邮箱）', 'Search users (name / email)')}
            className="flex-1 bg-transparent py-1.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:outline-none"
          />
        </div>

        <div className="max-h-64 overflow-y-auto rounded-md border border-zinc-800 bg-zinc-950/40">
          {usersLoading ? (
            <div className="flex h-24 items-center justify-center text-sm text-zinc-500">
              <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载用户…', 'Loading users…')}
            </div>
          ) : usersError ? (
            <div className="px-3 py-3 text-xs text-amber-300">{usersError}</div>
          ) : candidates.length === 0 ? (
            <div className="px-3 py-6 text-center text-xs text-zinc-500">
              {search ? tr('没有匹配的用户', 'No matching users') : tr('没有可加入的用户', 'No users available to add')}
            </div>
          ) : (
            <ul className="divide-y divide-zinc-800/40">
              {candidates.map((u) => {
                const active = u.id === pickedId;
                return (
                  <li key={u.id}>
                    <button
                      type="button"
                      onClick={() => setPickedId(u.id)}
                      className={cn(
                        'flex w-full items-center gap-2 px-3 py-2 text-left transition-colors',
                        active ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-300 hover:bg-zinc-900/60',
                      )}
                    >
                      <span className="flex-1">
                        <span className="text-sm">{u.display_name || u.email.split('@')[0]}</span>
                        <span className="ml-2 font-mono text-[11px] text-zinc-500">{u.email}</span>
                      </span>
                      {active && <Chip tone="info">{tr('已选', 'Selected')}</Chip>}
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>

        <Field label={tr('角色', 'Role')}>
          <select
            className={inputClass}
            value={role}
            onChange={(e) => setRole(e.target.value as OrgRole)}
          >
            {ROLE_OPTIONS.map((r) => (
              <option key={r} value={r}>
                {ROLE_LABEL[r]}
              </option>
            ))}
          </select>
        </Field>

        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}
      </div>
    </Modal>
  );
}

// ---------- helpers ----------------------------------------------------------

function Field({
  label,
  hint,
  required,
  children,
}: {
  label: string;
  hint?: string;
  required?: boolean;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <span className="mb-1 block text-[11px] text-zinc-400">
        {label}
        {required && <span className="ml-0.5 text-red-400">*</span>}
      </span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-zinc-500">{hint}</span>}
    </label>
  );
}

const inputClass =
  'w-full rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2 text-sm text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none disabled:cursor-not-allowed disabled:opacity-60';

function errMsg(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return trInline('操作失败', 'Operation failed');
}
