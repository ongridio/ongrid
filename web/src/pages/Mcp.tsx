import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Loader2,
  Pencil,
  Plug,
  PlugZap,
  Plus,
  RefreshCw,
  Server,
  ShieldCheck,
  Trash2,
  XCircle,
} from 'lucide-react';
import { useAuth } from '@/store/auth';
import { ApiError } from '@/api/client';
import {
  createMcpServer,
  deleteMcpServer,
  listMcpServers,
  parseToolsCache,
  testMcpServer,
  updateMcpServer,
  type McpServer,
  type McpServerInput,
  type McpTool,
} from '@/api/mcp';
import { listSecrets, type SecretView } from '@/api/secrets';
import { Modal } from '@/components/Modal';
import { Button, Card, Chip, EmptyState } from '@/components/ui';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

// MCP servers config page (HLD-018 P3). Registers external MCP servers
// whose tools join the Agent / Workflow toolbag. Tool calls require human
// approval by default; a "trusted" server skips the gate (read-only only).
//
// Admin-only for create / update / delete; non-admins see the list read-only.
// Style mirrors settings/Marketplace.tsx + settings/Secrets.tsx (Card /
// Button / Chip / EmptyState, zinc palette, tr() for every string).

const inputClass =
  'w-full rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2 text-sm text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none disabled:cursor-not-allowed disabled:opacity-60';

const NONE = '__none__';

function emptyInput(): McpServerInput {
  return {
    name: '',
    transport: 'http',
    endpoint: '',
    credential: '',
    header_template: '',
    trusted: false,
    enabled: true,
  };
}

export default function McpPage() {
  const { tr } = useI18n();
  const role = useAuth((s) => s.role);
  const isAdmin = role === 'admin';

  const [servers, setServers] = useState<McpServer[]>([]);
  const [secrets, setSecrets] = useState<SecretView[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [toast, setToast] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  // editor modal: null = closed; { id: null } = create; { id } = edit
  const [editing, setEditing] = useState<{ id: number | null; input: McpServerInput } | null>(null);
  const [saving, setSaving] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<McpServer | null>(null);
  const [testState, setTestState] = useState<Record<number, { loading: boolean; tools?: McpTool[]; error?: string }>>({});

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const [list, secs] = await Promise.all([listMcpServers(), listSecrets().catch(() => ({ items: [] }))]);
      setServers(list);
      setSecrets(secs.items ?? []);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), 5000);
    return () => window.clearTimeout(t);
  }, [toast]);

  const errMsg = (e: unknown) => (e instanceof ApiError ? e.message : (e as Error).message);

  const handleSave = useCallback(async () => {
    if (!editing) return;
    const input = editing.input;
    if (!input.name.trim() || !input.endpoint.trim()) return;
    setSaving(true);
    try {
      if (editing.id == null) {
        await createMcpServer(input);
        setToast({ kind: 'ok', text: tr(`已创建 ${input.name}`, `Created ${input.name}`) });
      } else {
        await updateMcpServer(editing.id, input);
        setToast({ kind: 'ok', text: tr(`已保存 ${input.name}`, `Saved ${input.name}`) });
      }
      setEditing(null);
      await refresh();
    } catch (e) {
      setToast({ kind: 'err', text: errMsg(e) });
    } finally {
      setSaving(false);
    }
  }, [editing, refresh, tr]);

  const handleDelete = useCallback(
    async (s: McpServer) => {
      try {
        await deleteMcpServer(s.id);
        setToast({ kind: 'ok', text: tr(`已删除 ${s.name}`, `Deleted ${s.name}`) });
        await refresh();
      } catch (e) {
        setToast({ kind: 'err', text: errMsg(e) });
      }
    },
    [refresh, tr],
  );

  const handleTest = useCallback(
    async (s: McpServer) => {
      setTestState((cur) => ({ ...cur, [s.id]: { loading: true } }));
      try {
        const tools = await testMcpServer(s.id);
        setTestState((cur) => ({ ...cur, [s.id]: { loading: false, tools } }));
        setToast({
          kind: 'ok',
          text: tr(`${s.name}：拉到 ${tools.length} 个工具`, `${s.name}: fetched ${tools.length} tool(s)`),
        });
        // status / tools_cache changed server-side; pull fresh.
        await refresh();
      } catch (e) {
        const msg = errMsg(e);
        setTestState((cur) => ({ ...cur, [s.id]: { loading: false, error: msg } }));
        setToast({ kind: 'err', text: tr(`${s.name} 连接失败：${msg}`, `${s.name} connection failed: ${msg}`) });
      }
    },
    [refresh, tr],
  );

  return (
    <div className="anim-fade space-y-5">
      <div className="flex items-center gap-2">
        <Plug size={18} className="text-zinc-400" />
        <h1 className="text-lg font-semibold text-zinc-100">{tr('MCP 服务', 'MCP servers')}</h1>
        <div className="ml-auto flex items-center gap-1.5">
          <Button onClick={() => void refresh()} disabled={loading} variant="ghost">
            {loading ? <Loader2 size={11} className="animate-spin" /> : <RefreshCw size={11} />}
            {tr('刷新', 'Refresh')}
          </Button>
          {isAdmin && (
            <Button onClick={() => setEditing({ id: null, input: emptyInput() })} variant="subtle">
              <Plus size={13} />
              {tr('新建', 'New server')}
            </Button>
          )}
        </div>
      </div>

      <p className="text-[13px] leading-relaxed text-zinc-500">
        {tr(
          '注册外部 MCP server 后，它暴露的工具会进入 Agent / Workflow 工具箱。工具执行默认需要人工确认；标记为 trusted 的服务免审直跑（仅建议给只读服务）。',
          'Registered external MCP servers expose tools into the Agent / Workflow toolbag. Tool execution requires human approval by default; servers marked trusted run without approval (recommended for read-only servers only).',
        )}
        {!isAdmin && (
          <span className="ml-1 text-amber-400">{tr('仅 admin 可增删改。', 'Only admins can add / edit / remove.')}</span>
        )}
      </p>

      {err && (
        <div className="rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-[12px] text-red-400">{err}</div>
      )}

      {loading && servers.length === 0 ? (
        <Card className="flex h-24 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
        </Card>
      ) : servers.length === 0 ? (
        <Card>
          <EmptyState
            icon={Server}
            title={tr('还没有注册任何 MCP 服务', 'No MCP servers registered yet')}
            hint={
              isAdmin
                ? tr('点右上「新建」接入一个外部 MCP server', 'Use "New server" above to register an external MCP server')
                : tr('请联系 admin 接入 MCP 服务', 'Ask an admin to register an MCP server')
            }
            className="flex h-40 flex-col items-center justify-center gap-2 text-center"
          />
        </Card>
      ) : (
        <div className="space-y-3">
          {servers.map((s) => (
            <ServerRow
              key={s.id}
              server={s}
              test={testState[s.id]}
              isAdmin={isAdmin}
              onTest={() => void handleTest(s)}
              onEdit={() => setEditing({ id: s.id, input: toInput(s) })}
              onDelete={() => setConfirmDelete(s)}
            />
          ))}
        </div>
      )}

      {editing && (
        <ServerEditor
          id={editing.id}
          input={editing.input}
          secrets={secrets}
          saving={saving}
          onChange={(patch) => setEditing((cur) => (cur ? { ...cur, input: { ...cur.input, ...patch } } : cur))}
          onClose={() => setEditing(null)}
          onSave={() => void handleSave()}
        />
      )}

      <Modal
        open={confirmDelete !== null}
        onClose={() => setConfirmDelete(null)}
        size="sm"
        title={confirmDelete ? tr(`删除 ${confirmDelete.name}?`, `Delete ${confirmDelete.name}?`) : ''}
        footer={
          <>
            <Button onClick={() => setConfirmDelete(null)} variant="ghost">
              {tr('取消', 'Cancel')}
            </Button>
            <Button
              onClick={() => {
                const s = confirmDelete;
                setConfirmDelete(null);
                if (s) void handleDelete(s);
              }}
              variant="danger"
            >
              <Trash2 size={12} />
              {tr('确认删除', 'Delete')}
            </Button>
          </>
        }
      >
        <p className="text-sm text-zinc-300">
          {tr('删除后该服务的工具会从 Agent / Workflow 工具箱移除。', "The server's tools will be removed from the Agent / Workflow toolbag.")}
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

function toInput(s: McpServer): McpServerInput {
  return {
    name: s.name,
    transport: s.transport,
    endpoint: s.endpoint,
    credential: s.credential,
    header_template: s.header_template,
    trusted: s.trusted,
    enabled: s.enabled,
  };
}

// ---------- server row --------------------------------------------------------

function ServerRow({
  server,
  test,
  isAdmin,
  onTest,
  onEdit,
  onDelete,
}: {
  server: McpServer;
  test?: { loading: boolean; tools?: McpTool[]; error?: string };
  isAdmin: boolean;
  onTest: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { tr } = useI18n();
  const cachedTools = useMemo(() => parseToolsCache(server.tools_cache), [server.tools_cache]);
  const toolCount = (test?.tools ?? cachedTools).length;
  const probedTools = test?.tools;

  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-center gap-2">
        <Server size={14} className="text-zinc-500" />
        <span className="font-mono text-sm text-zinc-100">{server.name}</span>
        <Chip className="font-mono">{server.transport}</Chip>
        <StatusChip status={server.status} />
        {server.trusted && (
          <Chip tone="warning">
            <ShieldCheck size={10} />
            {tr('免审', 'trusted')}
          </Chip>
        )}
        {!server.enabled && <Chip tone="default">{tr('已停用', 'disabled')}</Chip>}
        {toolCount > 0 && (
          <Chip tone="info">{tr(`${toolCount} 个工具`, `${toolCount} tool${toolCount === 1 ? '' : 's'}`)}</Chip>
        )}

        <div className="ml-auto flex items-center gap-1.5">
          <Button onClick={onTest} disabled={test?.loading} variant="ghost">
            {test?.loading ? <Loader2 size={11} className="animate-spin" /> : <PlugZap size={11} />}
            {tr('测试连接', 'Test')}
          </Button>
          <Button onClick={onEdit} disabled={!isAdmin} variant="ghost" title={isAdmin ? undefined : tr('需要 admin 权限', 'Admin permission required')}>
            <Pencil size={11} />
            {tr('编辑', 'Edit')}
          </Button>
          <Button onClick={onDelete} disabled={!isAdmin} variant="danger" title={isAdmin ? undefined : tr('需要 admin 权限', 'Admin permission required')}>
            <Trash2 size={11} />
            {tr('删除', 'Delete')}
          </Button>
        </div>
      </div>

      <div className="mt-2 break-all font-mono text-[11px] text-zinc-500">{server.endpoint || '—'}</div>

      {server.status === 'error' && server.last_error && (
        <div className="mt-2 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-[11px] text-red-400">
          {server.last_error}
        </div>
      )}

      {test?.error && (
        <div className="mt-2 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-[11px] text-red-400">
          {test.error}
        </div>
      )}

      {probedTools && probedTools.length > 0 && (
        <div className="mt-3 rounded-md border border-zinc-800/80 bg-zinc-950/50 px-3 py-2.5">
          <div className="mb-1.5 text-[11px] uppercase tracking-wide text-zinc-500">
            {tr('拉到的工具', 'Fetched tools')}
          </div>
          <div className="flex flex-wrap gap-1.5">
            {probedTools.map((t) => (
              <span
                key={t.name}
                className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[11px] text-zinc-300"
                title={t.description}
              >
                mcp__{server.name}__{t.name}
              </span>
            ))}
          </div>
        </div>
      )}
    </Card>
  );
}

function StatusChip({ status }: { status: string }) {
  const { tr } = useI18n();
  if (status === 'ok') {
    return (
      <Chip tone="success">
        <CheckCircle2 size={10} />
        {tr('正常', 'ok')}
      </Chip>
    );
  }
  if (status === 'error') {
    return (
      <Chip tone="danger">
        <XCircle size={10} />
        {tr('异常', 'error')}
      </Chip>
    );
  }
  return <Chip tone="default">{tr('未探测', 'untested')}</Chip>;
}

// ---------- editor modal ------------------------------------------------------

function ServerEditor({
  id,
  input,
  secrets,
  saving,
  onChange,
  onClose,
  onSave,
}: {
  id: number | null;
  input: McpServerInput;
  secrets: SecretView[];
  saving: boolean;
  onChange: (patch: Partial<McpServerInput>) => void;
  onClose: () => void;
  onSave: () => void;
}) {
  const { tr } = useI18n();
  const canSave = input.name.trim().length > 0 && input.endpoint.trim().length > 0 && !saving;

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={id == null ? tr('新建 MCP 服务', 'New MCP server') : tr(`编辑 ${input.name}`, `Edit ${input.name}`)}
      footer={
        <>
          <Button onClick={onClose} variant="ghost">
            {tr('取消', 'Cancel')}
          </Button>
          <Button onClick={onSave} disabled={!canSave} variant="subtle">
            {saving ? <Loader2 size={12} className="animate-spin" /> : <Plus size={12} />}
            {saving ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field
          label={tr('名称', 'Name')}
          hint={tr(
            '唯一标识，也是工具前缀：该服务的工具会暴露为 mcp__<name>__*',
            'Unique label, also the tool prefix: this server\'s tools are exposed as mcp__<name>__*',
          )}
        >
          <input
            type="text"
            value={input.name}
            onChange={(e) => onChange({ name: e.target.value })}
            placeholder="github"
            className={cn(inputClass, 'font-mono')}
          />
        </Field>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-[160px_1fr]">
          <Field label={tr('传输方式', 'Transport')} hint={tr('当前仅支持 http', 'Only http supported for now')}>
            <select
              value={input.transport}
              onChange={(e) => onChange({ transport: e.target.value === 'stdio' ? 'stdio' : 'http' })}
              className={inputClass}
            >
              <option value="http">http</option>
              <option value="stdio" disabled>
                stdio ({tr('暂不支持', 'not yet')})
              </option>
            </select>
          </Field>
          <Field label={tr('端点 URL', 'Endpoint URL')} hint={tr('Streamable HTTP MCP 端点', 'Streamable HTTP MCP endpoint')}>
            <input
              type="text"
              value={input.endpoint}
              onChange={(e) => onChange({ endpoint: e.target.value })}
              placeholder="https://mcp.example.com/sse"
              className={cn(inputClass, 'font-mono')}
            />
          </Field>
        </div>

        <Field
          label={tr('凭证', 'Credential')}
          hint={tr('用于填充 header 模板里的 {{字段}}；选「（无）」表示不注入认证', 'Fills the {{field}} placeholders in the header template; pick "(none)" for no auth injection')}
        >
          <select
            value={input.credential || NONE}
            onChange={(e) => onChange({ credential: e.target.value === NONE ? '' : e.target.value })}
            className={inputClass}
          >
            <option value={NONE}>{tr('（无）', '(none)')}</option>
            {secrets.map((s) => (
              <option key={s.id} value={s.name}>
                {s.name}
              </option>
            ))}
          </select>
        </Field>

        <Field
          label={tr('Header 模板', 'Header template')}
          hint={tr(
            'JSON map；{{字段}} 会用所选凭证的同名字段填充。',
            'JSON map; {{field}} placeholders are filled from the selected credential\'s fields.',
          )}
        >
          <textarea
            value={input.header_template}
            onChange={(e) => onChange({ header_template: e.target.value })}
            placeholder={'{"Authorization":"Bearer {{token}}"}'}
            rows={3}
            className={cn(inputClass, 'font-mono')}
          />
        </Field>

        <Toggle
          label={tr('Trusted（免审）', 'Trusted')}
          checked={input.trusted}
          onChange={(v) => onChange({ trusted: v })}
        />
        {input.trusted && (
          <div className="flex items-start gap-2 rounded-md border border-amber-900/50 bg-amber-950/20 px-3 py-2 text-[11px] text-amber-400">
            <AlertTriangle size={13} className="mt-0.5 shrink-0" />
            <span>{tr('免审直跑：仅建议给只读服务，跳过人工确认。', 'Runs without approval. Recommended for read-only servers only.')}</span>
          </div>
        )}

        <Toggle
          label={tr('启用', 'Enabled')}
          checked={input.enabled}
          onChange={(v) => onChange({ enabled: v })}
        />
      </div>
    </Modal>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-[11px] text-zinc-400">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-zinc-500">{hint}</span>}
    </label>
  );
}

function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <button
      type="button"
      onClick={() => onChange(!checked)}
      role="switch"
      aria-checked={checked}
      className="flex w-full items-center justify-between gap-3 rounded-md border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-sm text-zinc-200 hover:border-zinc-700"
    >
      <span>{label}</span>
      <span
        className={cn(
          'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors',
          checked ? 'bg-accent' : 'bg-zinc-700',
        )}
      >
        <span
          className={cn(
            'inline-block h-4 w-4 transform rounded-full bg-white transition-transform',
            checked ? 'translate-x-4' : 'translate-x-0.5',
          )}
        />
      </span>
    </button>
  );
}
