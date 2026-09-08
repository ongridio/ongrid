import { Input, Label } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { Select } from '@/components/ui/Select';
import { useCallback, useEffect, useState } from 'react';
import {
  KeyRound,
  Loader2,
  MoreVertical,
  Pencil,
  Plus,
  Power,
  RefreshCw,
  Shield,
  Trash2,
  UserCog,
  UserPlus,
  Users as UsersIcon,
} from 'lucide-react';
import { ApiError } from '@/api/client';
import {
  createUser,
  deleteUser,
  listUsers,
  patchUser,
  setUserPassword,
  setUserRole,
  type CreateUserBody,
  type SystemRole,
  type User,
  type UserStatus,
} from '@/api/users';
import { Modal } from '@/components/Modal';
import { Button, Card, Chip, EmptyState, PageHeader } from '@/components/ui';
import { useMe } from '@/store/me';
import { cn } from '@/lib/cn';
import { tr as trInline, useI18n } from '@/i18n/locale';

// /settings/users —— admin-only 用户管理。
//
// 设计要点（2026-05 收敛）：
//   1. 系统现在只有 role 这一层（admin | user）；admin = 过去的 superuser；
//   2. 非 admin → EmptyState「需要管理员权限」，不发任何请求；
//   3. 行操作禁止作用在自己身上（删除 / 改角色 / 切状态），前端先拦
//      避免误操作，后端也会拦；
//   4. 创建用户成功后弹窗再显示一次密码方便复制 + 安全渠道转交。
export default function SettingsUsers() {
  const { tr } = useI18n();
  const { me, loading: meLoading } = useMe();
  const isAdmin = me?.role === 'admin';

  const [items, setItems] = useState<User[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [toast, setToast] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<User | null>(null);
  const [pwTarget, setPwTarget] = useState<User | null>(null);
  const [delTarget, setDelTarget] = useState<User | null>(null);
  // freshly-created password — one-shot modal copy
  const [oneShotPw, setOneShotPw] = useState<{ user: User; password: string } | null>(null);

  const refresh = useCallback(async () => {
    if (!isAdmin) return;
    setLoading(true);
    setError(null);
    try {
      const resp = await listUsers();
      setItems(resp.items ?? []);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [isAdmin]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), 4000);
    return () => window.clearTimeout(t);
  }, [toast]);

  const replaceRow = (u: User) =>
    setItems((cur) => {
      const idx = cur.findIndex((x) => x.id === u.id);
      if (idx === -1) return [u, ...cur];
      const next = cur.slice();
      next[idx] = u;
      return next;
    });

  const onCreated = (u: User, password: string) => {
    setItems((cur) => [u, ...cur]);
    setCreateOpen(false);
    setOneShotPw({ user: u, password });
  };

  const changeRole = async (u: User, next: SystemRole) => {
    if (u.role === next) return;
    try {
      const updated = await setUserRole(u.id, next);
      replaceRow(updated);
      setToast({
        kind: 'ok',
        text: tr(`${updated.email} 角色已改为 ${next}`, `${updated.email} role set to ${next}`),
      });
    } catch (e) {
      setToast({ kind: 'err', text: errMsg(e) });
    }
  };

  const toggleStatus = async (u: User) => {
    const status: UserStatus = u.status === 'active' ? 'disabled' : 'active';
    try {
      const next = await patchUser(u.id, { status });
      replaceRow(next);
      setToast({ kind: 'ok', text: status === 'active' ? tr(`${u.email} 已启用`, `${u.email} activated`) : tr(`${u.email} 已停用`, `${u.email} deactivated`) });
    } catch (e) {
      setToast({ kind: 'err', text: errMsg(e) });
    }
  };

  const doDelete = async (u: User) => {
    try {
      await deleteUser(u.id);
      setItems((cur) => cur.filter((x) => x.id !== u.id));
      setDelTarget(null);
      setToast({ kind: 'ok', text: tr(`已删除 ${u.email}`, `Deleted ${u.email}`) });
    } catch (e) {
      setToast({ kind: 'err', text: errMsg(e) });
    }
  };

  if (meLoading && !me) {
    return (
      <div className="flex h-40 items-center justify-center text-sm text-zinc-500">
        <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
      </div>
    );
  }

  if (!isAdmin) {
    return (
      <Card className="p-6">
        <EmptyState
          icon={Shield}
          title={tr('需要管理员权限', 'Admin permission required')}
          hint={tr('只有管理员（admin）才能管理系统用户。请联系管理员授予权限。', 'Only an admin can manage system users. Ask an admin to grant permission.')}
        />
      </Card>
    );
  }

  return (
    <div className="space-y-4 p-6">
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <UsersIcon size={14} className="text-zinc-400" />
            {tr('用户', 'Users')}
          </span>
        }
        subtitle={tr('管理系统用户与系统角色（管理员可见）', 'Manage system users and system role (admin only)')}
        className="border-0 px-0 py-0"
        actions={
          <>
            <Button onClick={refresh} disabled={loading}>
              {loading ? <Loader2 size={11} className="animate-spin" /> : <RefreshCw size={11} />}
              {tr('刷新', 'Refresh')}
            </Button>
            <Button variant="primary" onClick={() => setCreateOpen(true)}>
              <Plus size={12} />
              {tr('新建用户', 'New user')}
            </Button>
          </>
        }
      />

      {error && (
        <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
          {error}
        </div>
      )}

      <Card className="p-0">
        {loading && items.length === 0 ? (
          <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
            <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            title={tr('还没有用户', 'No users yet')}
            hint={tr('点右上角「新建用户」添加第一个', 'Click "New user" in the top right to add your first one')}
            className="flex h-40 flex-col items-center justify-center gap-2 text-center"
          />
        ) : (
          <UserTable
            items={items}
            meId={me?.id ?? -1}
            onEdit={setEditTarget}
            onPassword={setPwTarget}
            onChangeRole={changeRole}
            onToggleStatus={toggleStatus}
            onDelete={setDelTarget}
          />
        )}
      </Card>

      <CreateUserModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={onCreated}
      />

      <EditUserModal
        target={editTarget}
        onClose={() => setEditTarget(null)}
        onSaved={(u) => {
          replaceRow(u);
          setEditTarget(null);
          setToast({ kind: 'ok', text: tr(`已更新 ${u.email}`, `Updated ${u.email}`) });
        }}
      />

      <ResetPasswordModal
        target={pwTarget}
        onClose={() => setPwTarget(null)}
        onDone={(u) => {
          setPwTarget(null);
          setToast({ kind: 'ok', text: tr(`已重置 ${u.email} 的密码`, `Reset password for ${u.email}`) });
        }}
      />

      <Modal
        open={!!delTarget}
        onClose={() => setDelTarget(null)}
        title={delTarget ? tr(`删除 ${delTarget.email}?`, `Delete ${delTarget.email}?`) : tr('删除', 'Delete')}
        size="sm"
        footer={
          <>
            <Button onClick={() => setDelTarget(null)}>{tr('取消', 'Cancel')}</Button>
            <Button variant="danger" onClick={() => delTarget && doDelete(delTarget)}>
              <Trash2 size={12} />
              {tr('确认删除', 'Delete')}
            </Button>
          </>
        }
      >
        <p className="text-sm text-zinc-300">
          {tr('删除后用户立即无法登录，关联的组织成员关系会一并清除。该操作不可逆。', 'After deletion the user can no longer log in and all org memberships are cleared. This cannot be undone.')}
        </p>
      </Modal>

      <OneShotPasswordModal
        info={oneShotPw}
        onClose={() => setOneShotPw(null)}
      />

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

// ---------- table ------------------------------------------------------------

function UserTable({
  items,
  meId,
  onEdit,
  onPassword,
  onChangeRole,
  onToggleStatus,
  onDelete,
}: {
  items: User[];
  meId: number;
  onEdit: (u: User) => void;
  onPassword: (u: User) => void;
  onChangeRole: (u: User, next: SystemRole) => void;
  onToggleStatus: (u: User) => void;
  onDelete: (u: User) => void;
}) {
  const { tr } = useI18n();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-zinc-800/60 text-left text-[11px] uppercase tracking-wide text-zinc-500">
            <th className="px-4 py-2.5 font-medium">{tr('姓名', 'Name')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('邮箱', 'Email')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('手机', 'Phone')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('系统角色', 'System role')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('状态', 'Status')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('操作', 'Actions')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/40">
          {items.map((u) => {
            const isSelf = u.id === meId;
            return (
              <tr key={u.id} className="hover:bg-zinc-900/40">
                <td className="px-4 py-2.5 text-zinc-100">
                  {u.display_name || <span className="text-zinc-600">—</span>}
                  {isSelf && <Chip className="ml-2" tone="info">{tr('我', 'Me')}</Chip>}
                </td>
                <td className="px-4 py-2.5 font-mono text-[12px] text-zinc-300">{u.email}</td>
                <td className="px-4 py-2.5 text-zinc-400">
                  {u.phone || <span className="text-zinc-600">—</span>}
                </td>
                <td className="px-4 py-2.5">
                  {u.role === 'admin' ? (
                    <Chip tone="accent">
                      <Shield size={10} />
                      admin
                    </Chip>
                  ) : u.role === 'viewer' ? (
                    <Chip tone="default">viewer</Chip>
                  ) : (
                    <Chip tone="default">user</Chip>
                  )}
                </td>
                <td className="px-4 py-2.5">
                  <Chip tone={u.status === 'active' ? 'success' : 'warning'}>{u.status}</Chip>
                </td>
                <td className="px-4 py-2.5">
                  <div className="flex items-center gap-1">
                    <Hint content={tr('编辑姓名 / 手机', 'Edit name / phone')}><Button onClick={() => onEdit(u)} >
                      <Pencil size={11} />
                      {tr('编辑', 'Edit')}
                    </Button></Hint>
                    <RowActionsMenu
                      user={u}
                      isSelf={isSelf}
                      onPassword={() => onPassword(u)}
                      onChangeRole={(next) => onChangeRole(u, next)}
                      onToggleStatus={() => onToggleStatus(u)}
                      onDelete={() => onDelete(u)}
                    />
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ---------- row actions menu -----------------------------------------------
//
// Why a kebab menu rather than 5 inline buttons: a typical row has
// edit + reset password + (de)mote admin + (de)activate + delete.
// Five chips per row turn the table into a wall of buttons. Keep the
// most-used (edit) inline and tuck the rest under a single ⋮ button.

function RowActionsMenu({
  user,
  isSelf,
  onPassword,
  onChangeRole,
  onToggleStatus,
  onDelete,
}: {
  user: User;
  isSelf: boolean;
  onPassword(): void;
  onChangeRole(next: SystemRole): void;
  onToggleStatus(): void;
  onDelete(): void;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const run = (fn: () => void) => () => { setOpen(false); fn(); };
  return <DropdownMenu open={open} onOpenChange={setOpen}>
    <DropdownMenuTrigger aria-label={tr('更多操作', 'More actions')} className="rounded-md p-1 text-text-muted hover:bg-zinc-800">
      <MoreVertical size={14} />
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" className="w-44">
          <MenuItem onClick={run(onPassword)} icon={<KeyRound size={12} />}>
            {tr('重置密码', 'Reset password')}
          </MenuItem>
          {(['admin', 'user', 'viewer'] as SystemRole[]).map((r) => (
            <MenuItem
              key={r}
              onClick={run(() => onChangeRole(r))}
              disabled={isSelf || user.role === r}
              icon={<Shield size={12} />}
              title={isSelf ? tr('不能改自己的角色', 'Cannot change your own role') : undefined}
            >
              {tr(`设为 ${r}`, `Set role: ${r}`)}
            </MenuItem>
          ))}
          <MenuItem
            onClick={run(onToggleStatus)}
            disabled={isSelf}
            icon={<Power size={12} />}
            title={isSelf ? tr('不能停用自己', 'Cannot deactivate yourself') : undefined}
          >
            {user.status === 'active' ? tr('停用账号', 'Deactivate account') : tr('启用账号', 'Activate account')}
          </MenuItem>
          <div className="my-0.5 border-t border-zinc-800" />
          <MenuItem
            onClick={run(onDelete)}
            disabled={isSelf}
            danger
            icon={<Trash2 size={12} />}
            title={isSelf ? tr('不能删除自己', 'Cannot delete yourself') : undefined}
          >
            {tr('删除用户', 'Delete user')}
          </MenuItem>
    </DropdownMenuContent>
  </DropdownMenu>;
}

function MenuItem({
  onClick,
  disabled,
  danger,
  icon,
  title,
  children,
}: {
  onClick(): void;
  disabled?: boolean;
  danger?: boolean;
  icon?: React.ReactNode;
  title?: string;
  children: React.ReactNode;
}) {
  return (
    <Hint content={title}><DropdownMenuItem


      onClick={onClick}
      disabled={disabled}

      className={[
        'flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition',
        disabled
          ? 'cursor-not-allowed text-zinc-600'
          : danger
            ? 'text-red-300 hover:bg-red-500/10'
            : 'text-zinc-200 hover:bg-zinc-800',
      ].join(' ')}
    >
      {icon}
      {children}
    </DropdownMenuItem></Hint>
  );
}

// ---------- modals -----------------------------------------------------------

function CreateUserModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (u: User, password: string) => void;
}) {
  const { tr } = useI18n();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [phone, setPhone] = useState('');
  const [role, setRole] = useState<SystemRole>('user');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setEmail('');
      setPassword('');
      setDisplayName('');
      setPhone('');
      setRole('user');
      setErr(null);
      setBusy(false);
    }
  }, [open]);

  const canSubmit = email.trim() && password.trim() && displayName.trim() && !busy;

  const submit = async () => {
    if (!canSubmit) return;
    setErr(null);
    setBusy(true);
    try {
      const body: CreateUserBody = {
        email: email.trim(),
        password: password.trim(),
        display_name: displayName.trim(),
        phone: phone.trim() || undefined,
        role,
      };
      const u = await createUser(body);
      onCreated(u, body.password);
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
      title={tr('新建用户', 'New user')}
      size="md"
      footer={
        <>
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={submit} disabled={!canSubmit}>
            {busy ? <Loader2 size={12} className="animate-spin" /> : <UserPlus size={12} />}
            {tr('创建', 'Create')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label={tr('邮箱', 'Email')} required>
          <Input
            className={inputClass}
            value={email}
            onChange={(e) => {
              const next = e.target.value;
              setEmail(next);
              // Auto-suggest display name from the local-part of the
              // email, but only when the admin hasn't typed a name
              // themselves yet. Saves them retyping "zhao" when they
              // entered "zhao@acme.com"; also prevents the
              // sidebar / chat-author fallback from rendering the full
              // email address as the "name".
              if (!displayName.trim()) {
                const local = next.split('@')[0]?.trim() ?? '';
                if (local) setDisplayName(local);
              }
            }}
            placeholder="user@example.com"
          />
        </Field>
        <Field label={tr('初始密码', 'Initial password')} required hint={tr('管理员设置；创建后通过安全渠道告知该用户', 'You set this here; deliver it to the user through a secure channel')}>
          <Input className={inputClass} value={password} onChange={(e) => setPassword(e.target.value)} placeholder={tr('至少 8 位', '8+ chars')} />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={tr('显示名', 'Display name')} required hint={tr('UI 上展示的名称；空时会自动用邮箱前段', 'Shown in UI; auto-filled from email local-part when blank')}>
            <Input className={inputClass} value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
          </Field>
          <Field label={tr('手机', 'Phone')}>
            <Input className={inputClass} value={phone} onChange={(e) => setPhone(e.target.value)} />
          </Field>
        </div>
        <Field
          label={tr('系统角色', 'System role')}
          hint={tr(
            'admin 管平台（用户/组织/集成/告警/IM）；user 用全部功能（含 chat 跑 mutating 工具）；viewer 只读 + 受限 chat（LLM 只能调 read-only 工具）。',
            'admin manages the platform (users/orgs/integrations/alerts/IM); user has full functional use (including mutating tools in chat); viewer is read-only with restricted chat (LLM gets read-only tools).',
          )}
        >
          <Select label={tr('系统角色', 'System role')}
            className="w-full"
            value={role}
            onValueChange={(selectedValue) => setRole(selectedValue as SystemRole)}
          >
            <option value="user">user</option>
            <option value="viewer">viewer</option>
            <option value="admin">admin</option>
          </Select>
        </Field>
        {err && <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">{err}</div>}
      </div>
    </Modal>
  );
}

function EditUserModal({
  target,
  onClose,
  onSaved,
}: {
  target: User | null;
  onClose: () => void;
  onSaved: (u: User) => void;
}) {
  const { tr } = useI18n();
  const [displayName, setDisplayName] = useState('');
  const [phone, setPhone] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (target) {
      setDisplayName(target.display_name ?? '');
      setPhone(target.phone ?? '');
      setErr(null);
      setBusy(false);
    }
  }, [target]);

  const submit = async () => {
    if (!target) return;
    setBusy(true);
    setErr(null);
    try {
      const u = await patchUser(target.id, {
        display_name: displayName,
        phone,
      });
      onSaved(u);
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
      title={target ? tr(`编辑 ${target.email}`, `Edit ${target.email}`) : tr('编辑', 'Edit')}
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={submit} disabled={busy}>
            {busy ? <Loader2 size={12} className="animate-spin" /> : <UserCog size={12} />}
            {tr('保存', 'Save')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label={tr('显示名', 'Display name')}>
          <Input className={inputClass} value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </Field>
        <Field label={tr('手机', 'Phone')}>
          <Input className={inputClass} value={phone} onChange={(e) => setPhone(e.target.value)} />
        </Field>
        {err && <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">{err}</div>}
      </div>
    </Modal>
  );
}

function ResetPasswordModal({
  target,
  onClose,
  onDone,
}: {
  target: User | null;
  onClose: () => void;
  onDone: (u: User) => void;
}) {
  const { tr } = useI18n();
  const [pw, setPw] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (target) {
      setPw('');
      setErr(null);
      setBusy(false);
    }
  }, [target]);

  const submit = async () => {
    if (!target || !pw.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      await setUserPassword(target.id, pw.trim());
      onDone(target);
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
      title={target ? tr(`重置 ${target.email} 的密码`, `Reset password for ${target.email}`) : tr('重置密码', 'Reset password')}
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" onClick={submit} disabled={busy || !pw.trim()}>
            {busy ? <Loader2 size={12} className="animate-spin" /> : <KeyRound size={12} />}
            {tr('重置', 'Reset')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label={tr('新密码', 'New password')} required hint={tr('设置后请通过安全渠道告知用户', 'After setting, deliver it to the user through a secure channel')}>
          <Input className={inputClass} value={pw} onChange={(e) => setPw(e.target.value)} placeholder={tr('至少 8 位', '8+ chars')} />
        </Field>
        {err && <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">{err}</div>}
      </div>
    </Modal>
  );
}

function OneShotPasswordModal({
  info,
  onClose,
}: {
  info: { user: User; password: string } | null;
  onClose: () => void;
}) {
  const { tr } = useI18n();
  return (
    <Modal
      open={!!info}
      onClose={onClose}
      title={tr('用户已创建', 'User created')}
      size="sm"
      footer={<Button variant="primary" onClick={onClose}>{tr('我已记下', 'Noted')}</Button>}
    >
      {info && (
        <div className="space-y-3 text-sm text-zinc-300">
          <p>
            {tr('账号已创建：', 'Account created: ')}<span className="font-mono text-zinc-100">{info.user.email}</span>
          </p>
          <p className="text-[11px] text-zinc-500">{tr('刚刚设置的初始密码：', 'Initial password you set:')}</p>
          <pre className="select-all rounded-md border border-zinc-800 bg-zinc-950/80 px-3 py-2 font-mono text-zinc-100">
            {info.password}
          </pre>
          <p className="text-[11px] text-amber-300/80">
            {tr('请通过安全渠道告知该用户登录密码。建议提示对方首次登录后自行修改。', 'Deliver the password to the user through a secure channel. They can change it themselves after the first login.')}
          </p>
        </div>
      )}
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
    <Label className="block">
      <span className="mb-1 block text-[11px] text-zinc-400">
        {label}
        {required && <span className="ml-0.5 text-red-400">*</span>}
      </span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-zinc-500">{hint}</span>}
    </Label>
  );
}

const inputClass =
  "w-full";

function errMsg(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return trInline('操作失败', 'Operation failed');
}
