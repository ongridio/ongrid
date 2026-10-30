import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { BookOpen, Copy, ExternalLink, KeyRound, Plus, RefreshCw } from 'lucide-react';
import { Button, Card, Checkbox, Chip, EmptyStateCard, Input, Label, PaginationFooter, Select } from '@/components/ui';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { createMcpToken, deleteMcpToken, listMcpTokens, updateMcpToken, type McpToken, type McpTokenList } from '@/api/mcpTokens';

const resources: [string, string, string][] = [
  ['devices', '设备', 'Devices'], ['alerts', '告警处理', 'Incidents'], ['alert-rules', '告警规则', 'Alert rules'],
  ['monitor', '监控面板', 'Monitor panels'], ['metrics', '指标', 'Metrics'], ['logs', '日志', 'Logs'],
  ['traces', '链路', 'Traces'], ['k8s', 'Kubernetes', 'Kubernetes'], ['host', '主机服务', 'Host services'],
  ['edges', 'Edge 与升级', 'Edges and upgrades'], ['network', '网络设备', 'Network devices'], ['topology', '拓扑', 'Topology'],
  ['host-files', '主机文件', 'Host files'], ['databases', '数据库观测', 'Database observability'],
  ['apm', '应用性能', 'APM'], ['profiles', '性能剖析', 'Profiles'], ['agent-traces', 'Agent 观测', 'Agent observability'],
  ['knowledge', '知识与代码库', 'Knowledge and source'], ['flows', '工作流', 'Workflows'], ['reports', '报告与任务', 'Reports and tasks'],
  ['agents', 'Agent 与会话', 'Agents and sessions'], ['skills', '技能', 'Skills'], ['operator-runs', '运维执行', 'Operator runs'],
  ['packet-captures', '抓包', 'Packet captures'], ['pages', '页面', 'Pages'], ['webshell', '终端会话管理', 'Terminal sessions'],
  ['notifications', '通知渠道', 'Notification channels'], ['im', '即时通信', 'Messaging'],
  ['integrations', '集成配置', 'Integrations'], ['mcp-servers', 'MCP 服务配置', 'MCP servers'], ['marketplace', '扩展市场', 'Marketplace'],
  ['credentials', '凭证管理', 'Credentials'], ['settings', '系统设置', 'System settings'], ['system', '健康与版本', 'Health and version'],
  ['iam', '用户与组织', 'Users and organizations'], ['approvals', '操作审批', 'Operation approvals'], ['audit', '审计日志', 'Audit logs'],
];
const actions: [string, string, string][] = [['read', '读取', 'Read'], ['write', '修改', 'Write'], ['delete', '删除', 'Delete'], ['execute', '执行', 'Execute']];
const readScope = (scope: string) => scope.startsWith('host:') ? 'devices:read' : `${scope.split(':')[0]}:read`;

export default function ExternalAIPage() {
  const { tr } = useI18n();
  const { confirmAction, dialog } = useDialogs();
  const [data, setData] = useState<McpTokenList | null>(null);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [formOpen, setFormOpen] = useState(false);
  const [guideOpen, setGuideOpen] = useState(false);
  const [editing, setEditing] = useState<McpToken | null>(null);
  const [name, setName] = useState('');
  const [scopes, setScopes] = useState(['devices:read', 'alerts:read']);
  const [expiry, setExpiry] = useState('30');
  const [customExpiry, setCustomExpiry] = useState('');
  const [secret, setSecret] = useState('');
  const [formError, setFormError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try { setData(await listMcpTokens(page)); setError(''); }
    catch { setError(tr('读取失败，请重试。', 'Could not load tokens. Try again.')); }
    finally { setLoading(false); }
  }, [page, tr]);
  useEffect(() => { void load(); }, [load]);

  const startCreate = () => { setEditing(null); setName(''); setScopes(['devices:read', 'alerts:read']); setExpiry('30'); setCustomExpiry(''); setFormError(''); setFormOpen(true); };
  const startEdit = (token: McpToken) => {
    setEditing(token); setName(token.name); setScopes([...token.scopes]); setExpiry('keep'); setFormError('');
    const at = token.expires_at ? new Date(token.expires_at) : null;
    if (at) at.setMinutes(at.getMinutes() - at.getTimezoneOffset());
    setCustomExpiry(at ? at.toISOString().slice(0, 16) : ''); setFormOpen(true);
  };
  const toggle = (scope: string, checked: boolean) => setScopes((current) => {
    if (checked) return [...new Set([...current, scope, readScope(scope)])];
    return current.filter((value) => value !== scope && (!scope.endsWith(':read') || readScope(value) !== scope));
  });
  const copy = async (value: string) => {
    try { await navigator.clipboard.writeText(value); setNotice(tr('已复制', 'Copied')); }
    catch { setNotice(tr('复制失败，请手动选择并复制。', 'Copy failed. Select and copy the text manually.')); }
  };
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    let expiresAt: string | null = expiry === 'keep' && editing ? editing.expires_at : null;
    if (expiry !== 'never' && expiry !== 'keep') {
      const at = expiry === 'custom' ? new Date(customExpiry) : new Date(Date.now() + Number(expiry) * 86400000);
      if (!Number.isFinite(at.getTime()) || at.getTime() <= Date.now()) { setFormError(tr('请选择未来的到期时间。', 'Choose a future expiry time.')); return; }
      expiresAt = at.toISOString();
    }
    setBusy(true); setFormError(''); setNotice('');
    try {
      const input = { name: name.trim(), scopes, expires_at: expiresAt };
      if (editing) {
        await updateMcpToken(editing.public_id, input);
        setNotice(tr('Token 已更新，权限已生效。', 'Token updated. Permissions are now in effect.'));
      } else {
        const result = await createMcpToken(input);
        setSecret(result.token);
      }
      setFormOpen(false); await load();
    } catch { setFormError(editing ? tr('保存失败，请检查权限和输入后重试。', 'Could not save token. Check permissions and input, then retry.') : tr('创建失败，请检查权限和输入后重试。', 'Could not create token. Check permissions and input, then retry.')); }
    finally { setBusy(false); }
  };
  const remove = async (token: McpToken) => {
    if (!(await confirmAction(tr(`删除“${token.name}”？此 Token 将立即失效，使用它的客户端将无法继续访问。`, `Delete “${token.name}”? This token will stop working and clients using it will lose access.`)))) return;
    setBusy(true);
    try {
      await deleteMcpToken(token.public_id);
      if (page > 0 && data?.items.length === 1) setPage(page - 1);
      else await load();
    }
    catch { setError(tr('删除失败，请重试。', 'Could not delete token. Try again.')); }
    finally { setBusy(false); }
  };
  const date = (value: string) => new Date(value).toLocaleString();
  const availableScopes = resources.flatMap(([resource]) => actions.map(([action]) => `${resource}:${action}`)).filter((scope) => data?.scopes.includes(scope));
  const allScopesSelected = availableScopes.length > 0 && availableScopes.every((scope) => scopes.includes(scope));
  const endpoint = data?.endpoint.startsWith('/') ? `${window.location.origin}${data.endpoint}` : data?.endpoint ?? '';
  const workbuddyConfig = JSON.stringify({ mcpServers: { ongrid: { type: 'streamableHttp', url: endpoint, headers: { Authorization: 'Bearer <Token>' } } } }, null, 2);

  return <div className="min-w-0 space-y-5 text-xs text-text">
    {dialog}
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0 space-y-1">
        <h2 className="text-sm font-semibold">{tr('MCP 访问', 'MCP access')}</h2>
        <p className="text-text-muted">{tr('管理客户端通过 MCP 访问 Ongrid 的 Token 和权限。', 'Manage tokens and permissions for MCP clients accessing Ongrid.')}</p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={() => { setNotice(''); setGuideOpen(true); }} disabled={!data} aria-haspopup="dialog"><BookOpen size={13} />{tr('接入指南', 'Connection guide')}</Button>
        <Button size="sm" onClick={() => void load()} disabled={loading}><RefreshCw size={13} />{tr('刷新', 'Refresh')}</Button>
        <Button size="sm" variant="primary" onClick={startCreate} disabled={!data || busy}><Plus size={13} />{tr('创建 Token', 'Create token')}</Button>
      </div>
    </div>
    {error && <div role="alert" className="text-red-500">{error}</div>}
    {data && <Card className="space-y-3 p-4">
      <div className="flex flex-wrap gap-2">
        <Chip tone={data.enabled ? 'success' : 'warning'}>{data.enabled ? tr('接入已启用', 'Access enabled') : tr('接入未启用', 'Access disabled')}</Chip>
        <Chip>{data.writes_enabled ? tr('可按权限写入', 'Scoped writes enabled') : tr('写入未启用', 'Writes disabled')}</Chip>
      </div>
      {!data.enabled && <p className="text-text-muted">{tr('请由部署管理员开启外部 MCP 服务；可先创建并保存 Token。', 'Ask the deployment administrator to enable external MCP. You can create and save a token now.')}</p>}
      <div className="space-y-1"><Label htmlFor="mcp-endpoint">{tr('MCP 服务地址', 'MCP endpoint')}</Label>
        <div className="flex min-w-0 gap-2"><Input id="mcp-endpoint" readOnly value={endpoint} className="min-w-0 flex-1 font-mono" /><Button size="icon" aria-label={tr('复制服务地址', 'Copy endpoint')} onClick={() => void copy(endpoint)}><Copy size={14} /></Button></div>
      </div>
      <p className="text-text-muted">{tr('客户端选择 Streamable HTTP，将 Token 填入 Authorization: Bearer <Token>。每个客户端使用独立 Token。', 'Select Streamable HTTP in your client and set Authorization: Bearer <Token>. Use a separate token for each client.')}</p>
      <p className="text-text-muted">{tr('当前仅系统管理员可创建，权限覆盖本部署。查询结果会传给外部 AI；修改、删除和执行权限请按需选择。', 'System administrators only. Access covers this deployment. Results are sent to the external AI; choose write, delete and execute permissions only as needed.')}</p>
    </Card>}
    {notice && !guideOpen && <p role="status" className="break-words text-text-muted">{notice}</p>}
    {loading && !data ? <p role="status">{tr('加载中…', 'Loading…')}</p> : data?.items.length === 0 ?
      <EmptyStateCard icon={KeyRound} title={tr('还没有接入 Token', 'No access tokens yet')} hint={tr('为一个客户端创建 Token，再填写 MCP 配置。', 'Create a token for one client, then configure MCP.')} action={<Button variant="primary" onClick={startCreate}>{tr('创建 Token', 'Create token')}</Button>} /> :
      data && <Card className="divide-y divide-border">
        {data.items.map((token) => {
          const expired = token.expires_at !== null && Date.parse(token.expires_at) <= Date.now();
          return <div key={token.public_id} className="flex flex-wrap items-center justify-between gap-3 p-4">
            <div className="min-w-0 flex-1 basis-48 space-y-2">
              <div className="flex flex-wrap items-center gap-2"><span className="break-all font-medium">{token.name}</span><Chip tone={expired ? 'default' : 'success'}>{expired ? tr('已到期', 'Expired') : tr('有效', 'Active')}</Chip></div>
              <p className="break-all text-text-muted">{tr('到期：', 'Expires: ')}{token.expires_at ? date(token.expires_at) : tr('不过期', 'Never')} · {tr('创建：', 'Created: ')}{date(token.created_at)}</p>
            </div>
            <div className="flex shrink-0 items-center gap-2"><Button size="sm" disabled={busy} onClick={() => startEdit(token)}>{tr('编辑', 'Edit')}</Button><Button variant="dangerGhost" size="sm" disabled={busy} onClick={() => void remove(token)}>{tr('删除', 'Delete')}</Button></div>
          </div>;
        })}
      </Card>}
    {data && <PaginationFooter page={page} pageSize={20} shown={data.items.length} total={data.total} loading={loading || busy} onPageChange={setPage} />}
    {data && <Modal open={guideOpen} onClose={() => { setGuideOpen(false); setNotice(''); }} title={tr('接入指南', 'Connection guide')} size="lg">
      <div className="min-w-0 space-y-3 text-xs text-text">
      <div className="space-y-1">
        <p className="text-text-muted">{tr('先在本页为客户端创建独立 Token，选择所需权限，并复制创建后显示的 Token。', 'Create a separate token for your client on this page, choose its permissions, and copy the token shown after creation.')}</p>
      </div>
      <section className="min-w-0 space-y-3">
        <h3 className="font-medium">WorkBuddy</h3>
        <ol className="list-decimal space-y-2 pl-5 leading-relaxed">
          <li>{tr('打开 WorkBuddy「专家·技能·连接器 → 连接器 → 自定义连接器」。部分版本入口为「插件 → MCP 服务器 → 配置 MCP」。', 'In WorkBuddy, open Experts · Skills · Connectors → Connectors → Custom connector. Some versions use Plugins → MCP servers → Configure MCP.')}</li>
          <li>{tr('在 MCP JSON 配置中加入下方 ongrid 项，保留已有的其他服务；将 <Token> 替换为刚创建的 Token。', 'Add the ongrid entry below to your MCP JSON configuration and keep your existing servers. Replace <Token> with the token you just created.')}</li>
          <li>{tr('保存并启用 Ongrid。在任务输入框的「+ → 连接器」中选中 Ongrid。', 'Save and enable Ongrid. Select it from + → Connectors in the task input.')}</li>
        </ol>
        <div className="min-w-0 space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="font-medium">{tr('MCP JSON 配置', 'MCP JSON configuration')}</span>
            <Button size="sm" aria-label={tr('复制 WorkBuddy 配置', 'Copy WorkBuddy configuration')} onClick={() => void copy(workbuddyConfig)}><Copy size={13} />{tr('复制配置', 'Copy configuration')}</Button>
          </div>
          <pre className="min-w-0 whitespace-pre-wrap break-all rounded-md border border-border bg-bg p-3 font-mono text-xs leading-relaxed"><code>{workbuddyConfig}</code></pre>
          <p className="text-text-muted">{tr('Token 只在创建后显示一次。复制的配置仅含占位符，请在客户端内替换。', 'Tokens are shown only once after creation. The copied configuration contains a placeholder; replace it inside your client.')}</p>
        </div>
        <a className="og-resource-link inline-flex items-center gap-1" href="https://www.codebuddy.cn/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/Connector" target="_blank" rel="noreferrer noopener">{tr('WorkBuddy 官方文档', 'WorkBuddy documentation')}<ExternalLink size={12} aria-hidden="true" /></a>
      </section>
      <p className="border-t border-border pt-3 text-text-muted">{tr('使用客户端可访问且证书受信任的 HTTPS 地址。若请求从云端发出，该地址也需从云端可达。', 'Use an HTTPS address that your client can reach, with a trusted certificate. If requests run in the cloud, the address must also be reachable from there.')}</p>
      {notice && <p role="status" className="break-words text-text-muted">{notice}</p>}
      </div>
    </Modal>}
    <Modal open={formOpen} onClose={() => { if (!busy) setFormOpen(false); }} title={editing ? tr('编辑接入 Token', 'Edit access token') : tr('创建接入 Token', 'Create access token')} size="lg" footer={<><Button disabled={busy} onClick={() => setFormOpen(false)}>{tr('取消', 'Cancel')}</Button><Button variant="primary" type="submit" form="save-mcp-token" disabled={busy || !name.trim() || scopes.length === 0}>{busy ? tr('保存中…', 'Saving…') : editing ? tr('保存', 'Save') : tr('创建', 'Create')}</Button></>}>
      <form id="save-mcp-token" onSubmit={(event) => void save(event)} className="space-y-4">
        {editing && <p className="text-xs text-text-muted">{tr('保存后继续使用原 Token，客户端无需重新配置；权限修改立即生效。', 'Keep using the same token after saving; no client changes are needed. Permission changes take effect immediately.')}</p>}
        {formError && <p role="alert" className="text-xs text-red-500">{formError}</p>}
        <div className="space-y-1"><Label htmlFor="mcp-token-name">{tr('名称', 'Name')}</Label><Input id="mcp-token-name" required maxLength={128} value={name} onChange={(event) => setName(event.target.value)} placeholder={tr('例如：我的 WorkBuddy', 'For example: My WorkBuddy')} autoComplete="off" /></div>
        <div className="space-y-1"><Label>{tr('有效期', 'Expiry')}</Label><Select className="w-full" label={tr('有效期', 'Expiry')} value={expiry} onValueChange={setExpiry} options={[...(editing ? [{ value: 'keep', label: tr(`保持当前（${editing.expires_at ? date(editing.expires_at) : '不过期'}）`, `Keep current (${editing.expires_at ? date(editing.expires_at) : 'Never'})`) }] : []), { value: '30', label: tr('30 天', '30 days') }, { value: '90', label: tr('90 天', '90 days') }, { value: 'custom', label: tr('自定义', 'Custom') }, { value: 'never', label: tr('不过期', 'Never') }]} /></div>
        {expiry === 'custom' && <div className="space-y-1"><Label htmlFor="mcp-expiry">{tr('到期时间（本地时区）', 'Expiry time (local timezone)')}</Label><Input id="mcp-expiry" type="datetime-local" required value={customExpiry} onChange={(event) => setCustomExpiry(event.target.value)} /></div>}
        {expiry === 'never' && <p className="text-xs text-text-muted">{tr('此 Token 不会因时间到期而失效；仍可随时删除，账号禁用或降权也会使其失效。', 'This token has no time expiry. Deletion, account suspension or loss of administrator access still disables it.')}</p>}
        <fieldset className="space-y-3"><legend className="mb-2 text-xs font-medium">{tr('允许的能力', 'Allowed capabilities')}</legend>
          <label className="flex w-fit cursor-pointer items-center gap-2 text-xs">
            <Checkbox aria-label={tr('全选', 'Select all')} checked={allScopesSelected} indeterminate={!allScopesSelected && availableScopes.some((scope) => scopes.includes(scope))} disabled={busy || availableScopes.length === 0} onCheckedChange={() => setScopes(allScopesSelected ? [] : availableScopes)} />
            {tr('全选', 'Select all')}
          </label>
          {resources.filter(([resource]) => data?.scopes.some((scope) => scope.startsWith(`${resource}:`))).map(([resource, zh, en]) => <div key={resource} className="grid gap-2 border-b border-border pb-3 last:border-0 sm:grid-cols-[130px_1fr]">
            <span className="text-xs text-text-muted">{tr(zh, en)}</span><div className="flex flex-wrap gap-x-4 gap-y-2">{actions.filter(([action]) => data?.scopes.includes(`${resource}:${action}`)).map(([action, actionZh, actionEn]) => {
              const scope = `${resource}:${action}`;
              return <label key={scope} className="flex items-center gap-2 text-xs"><Checkbox checked={scopes.includes(scope)} onCheckedChange={(checked) => toggle(scope, checked)} aria-label={tr(`${zh}：${actionZh}`, `${en}: ${actionEn}`)} />{tr(actionZh, actionEn)}</label>;
            })}</div>
          </div>)}
        </fieldset>
        <p className="text-xs text-text-muted">{tr('写权限会同时选择对应的读取权限。写入、删除和执行会返回当前操作的确认链接；打开后确认一次即可执行。', 'Write permissions also select the required read access. Writes, deletions and actions return a link to the operation. Open it and confirm once to execute.')}</p>
      </form>
    </Modal>
    <Modal open={!!secret} onClose={() => { setSecret(''); setNotice(''); }} title={tr('保存你的 Token', 'Save your token')} size="lg" footer={<Button variant="primary" onClick={() => { setSecret(''); setNotice(''); }}>{tr('已保存，关闭', 'Saved, close')}</Button>}>
      <div className="space-y-3 text-xs"><p>{tr('Token 只显示这一次。请保存到客户端的私有配置中。', 'This token is shown only once. Save it in your client’s private configuration.')}</p><Input aria-label={tr('一次性 Token', 'One-time token')} value={secret} readOnly className="font-mono" autoComplete="off" /><Button onClick={() => void copy(secret)}><Copy size={14} />{tr('复制 Token', 'Copy token')}</Button>{notice && <p role="status">{notice}</p>}</div>
    </Modal>
  </div>;
}
