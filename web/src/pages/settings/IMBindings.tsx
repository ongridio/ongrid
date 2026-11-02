import { useCallback, useEffect, useRef, useState } from 'react';
import { request, ApiError } from '@/api/client';
import type { IMApp } from '@/api/imbridge';
import { Button, Card } from '@/components/ui';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';

type Binding = { id: string; app_id: number; subject: string };
type Challenge = { id: string; code?: string; subject?: string; claimed: boolean; expires_at: string };
type Envelope<T> = { data: T };

function identityLabel(subject?: string): string {
 if (!subject) return '';
 try {
  const value = JSON.parse(subject) as {scope?: unknown; user?: unknown};
  if (typeof value.user === 'string') return [value.scope, value.user].filter(v => typeof v === 'string' && v).join(' / ');
 } catch { /* 兼容旧接口的纯文本身份，不丢失可核对信息。 */ }
 return subject;
}

export function IMBindings({ apps }: { apps: IMApp[] }) {
 const { tr } = useI18n();
 const [bindings,setBindings] = useState<Binding[]>([]);
 const [challenge,setChallenge] = useState<Challenge | null>(null);
 const [appID,setAppID] = useState('');
 const [busy,setBusy] = useState(false);
 const [disabled,setDisabled] = useState(false);
 const [error,setError] = useState('');
 const [notice,setNotice] = useState('');
 const [expired,setExpired] = useState(false);
 const gate = useRef(false);
 const refresh = useCallback(async () => {
  try { const r = await request<Envelope<{items:Binding[]}>>('GET','/im/bindings'); setBindings(r.data.items);setDisabled(false); }
  catch (e) { if(e instanceof ApiError && e.status === 503) setDisabled(true); else setError((e as Error).message); }
 },[]);
 useEffect(() => {void refresh();},[refresh]);
 useEffect(() => {
  if (!challenge) { setExpired(false); return; }
  if (Date.now() >= Date.parse(challenge.expires_at)) { setExpired(true); return; }
  let cancelled = false;
  let timer: ReturnType<typeof setTimeout>;
  async function poll() {
   if (!challenge || cancelled) return;
   if (Date.now() >= Date.parse(challenge.expires_at)) { setExpired(true); return; }
   if (!challenge.claimed && !gate.current) {
    try {
     const r = await request<Envelope<Challenge>>('GET', `/im/bindings/challenges/${challenge.id}`);
     if (!cancelled) setChallenge(old => old?.id === r.data.id ? {...old,...r.data} : old);
    } catch(e) {
     if (!cancelled) {
      if(e instanceof ApiError && e.status === 404) { setExpired(true); return; }
      setError((e as Error).message);
     }
    }
   }
   if (!cancelled) timer = setTimeout(() => void poll(), 3000);
  }
  timer = setTimeout(() => void poll(), 1000);
  return () => { cancelled = true; clearTimeout(timer); };
 }, [challenge?.id, challenge?.claimed, challenge?.expires_at]);
 async function action(fn:()=>Promise<void>) {
  if(gate.current)return;gate.current=true;setBusy(true);setError('');setNotice('');
  try {await fn();} catch(e){setError((e as Error).message);} finally {gate.current=false;setBusy(false);}
 }
 return <Card className="p-4 space-y-3">
  <h3 className="text-sm font-medium text-zinc-100">{tr('我的 IM 审批身份','My IM approval identities')}</h3>
  <p className="text-xs text-zinc-400">{tr('绑定后可在机器人消息中批准或拒绝操作。每次审批仍会检查当前管理员权限。','Bind your account to approve or reject bot requests. Current administrator permissions are checked for every decision.')}</p>
  {disabled ? <p className="text-xs text-zinc-500">{tr('原生审批尚未启用，仍可通过消息中的链接在 Ongrid 审批。','Native approvals are not enabled. Use the link in bot messages to approve in Ongrid.')}</p> : <>
   <ol className="space-y-1 text-xs text-zinc-400">
    <li>{tr('1. 选择机器人，生成你自己账号的一次性绑定码。','1. Select a bot and generate a one-time code for your account.')}</li>
    <li>{tr('2. 私聊机器人发送绑定命令，本页面会自动检测。','2. Send the binding command privately to the bot. This page checks automatically.')}</li>
    <li>{tr('3. 回到本页核对身份并确认；以后直接在卡片中审批。','3. Return here to verify and confirm the identity, then approve directly on cards.')}</li>
   </ol>
   <div className="flex flex-wrap gap-2">
    <Select aria-label={tr('绑定机器人','Bot to bind')} className="w-52 max-w-full" value={appID} onValueChange={value=>{setAppID(value);setChallenge(null);}} disabled={busy}>
     <option value="">{tr('选择机器人','Select a bot')}</option>
     {apps.filter(a=>a.enabled && a.mode==='stream').map(a=><option key={a.id} value={a.id}>{a.name}</option>)}
    </Select>
    <Button variant="primary" size="sm" disabled={!appID || busy} onClick={()=>void action(async()=>{const r=await request<Envelope<Challenge>>('POST','/im/bindings/challenges',{app_id:Number(appID)});setExpired(false);setChallenge(r.data);})}>{tr('开始绑定','Start binding')}</Button>
   </div>
   {challenge && <div className="space-y-3 rounded-md border border-zinc-800 bg-zinc-900 p-3 text-sm text-zinc-300">
    {expired && <p role="status" className="text-amber-500">{tr('绑定码已失效，请点击「开始绑定」重新生成。','The binding code expired. Select Start binding to generate another.')}</p>}
    {!expired && <>{!challenge.claimed && <p>{tr('在所选机器人私聊中发送以下口令，十分钟内有效。不要发送到群聊。','Send the command below in a private chat with the selected bot within ten minutes. Do not post it in a group.')}</p>}
    {!challenge.claimed && challenge.code && <code className="block select-all break-all text-xs">{apps.find(a=>String(a.id)===appID)?.provider==='slack'?'/ongrid-bind':'/bind'} {challenge.code}</code>}
    {!challenge.claimed && challenge.code && <Button size="sm" variant="outline" disabled={busy} onClick={()=>void action(async()=>{await navigator.clipboard.writeText(`${apps.find(a=>String(a.id)===appID)?.provider==='slack'?'/ongrid-bind':'/bind'} ${challenge.code}`);setNotice(tr('绑定命令已复制，请只发送给所选机器人。','Command copied. Send it only to the selected bot.'));})}>{tr('复制绑定命令','Copy binding command')}</Button>}
    {!challenge.claimed && <Button size="sm" variant="outline" disabled={busy} onClick={()=>void action(async()=>{const r=await request<Envelope<Challenge>>('GET',`/im/bindings/challenges/${challenge.id}`);setChallenge(old=>old?.id===r.data.id?{...old,...r.data}:old);})}>{tr('检查平台确认','Check platform confirmation')}</Button>}
    {challenge.claimed && <><p>{tr('机器人已收到绑定请求。请确认刚才是你本人发送的绑定命令。','The bot received your binding request. Confirm that you personally sent the command.')}</p><p className="break-all">{tr('请核对这是你的平台账号：','Verify this is your platform identity:')} {identityLabel(challenge.subject)}</p><Button variant="primary" size="sm" disabled={busy} onClick={()=>void action(async()=>{await request('POST',`/im/bindings/challenges/${challenge.id}/confirm`);setChallenge(null);await refresh();setNotice(tr('绑定成功，可以返回飞书等 IM 点击有效的审批卡片。绑定不会执行任何操作。','Account bound. You can now use valid approval cards in your IM app. Binding does not execute operations.'));})}>{tr('确认绑定此账号','Confirm this identity')}</Button></>}
    </>}
   </div>}
   {!bindings.length && <p className="text-xs text-zinc-500">{tr('当前账号尚未绑定 IM 审批身份。每位审批人需分别绑定自己的账号。','No IM approval identity bound. Each approver should bind their own account.')}</p>}
   <div className="divide-y divide-zinc-800">{bindings.map(b=><div key={b.id} className="flex items-center justify-between gap-3 py-2 text-xs text-zinc-400"><span className="min-w-0 break-all">{apps.find(a=>a.id===b.app_id)?.name ?? b.app_id} · {identityLabel(b.subject)}</span><Button size="sm" variant="outline" disabled={busy} onClick={()=>void action(async()=>{await request('DELETE',`/im/bindings/${b.id}`);await refresh();setNotice(tr('已解绑，该 IM 身份不能再以当前账号审批。','Unbound. This IM identity can no longer approve as your account.'));})}>{tr('解绑','Unbind')}</Button></div>)}</div>
  </>}
  {notice && <p role="status" className="text-xs text-zinc-400">{notice}</p>}
  {error && <p role="alert" className="text-xs text-red-500">{error}</p>}
 </Card>;
}
