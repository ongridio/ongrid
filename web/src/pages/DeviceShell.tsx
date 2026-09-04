// DeviceShell — full-page WebSSH terminal.
//
// Flow:
//   1. Page mounts → fetch device metadata (hostname for the modal title)
//      and pop the Connect modal with any server-side saved accounts.
//   2. User submits the modal → open WS, send first `open` frame with
//      cols/rows from the freshly-fitted xterm.
//   3. Manager replies with `ready` (SSH up) → terminal becomes interactive.
//      Binary frames are stdout; binary user input is wrapped to stdin.
//   4. `auth_error` / WS close → write a banner and allow reconnect;
//      top-level SSH `exit` → leave the terminal page.
//
// Security:
//   - Manual passwords live only in component state and the ticket request.
//   - Saved passwords are encrypted server-side and never returned to the UI.
//   - onbeforeunload sends a polite `{type:"close"}` so the manager can
//     finalize the audit row without waiting for TCP timeout.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  ChevronLeft,
  KeyRound,
  Plus,
  Power,
  RotateCw,
  Shield,
  ShieldCheck,
  Terminal as TerminalIcon,
  Trash2,
  UserRound,
} from 'lucide-react';
import { Modal } from '@/components/Modal';
import { Button } from '@/components/ui/Button';
import { XTerminal, type XTerminalApi } from '@/components/XTerminal';
import { getEdge, listEdges, type Edge } from '@/api/edges';
import {
  createShellTicket,
  deleteShellCredential,
  listShellCredentials,
  openShellSocket,
  resetShellKnownHost,
  sendControl,
  type ShellCredential,
  type ShellControlFrameIn,
} from '@/api/webshell';
import { usePermissions } from '@/store/me';
import { useI18n } from '@/i18n/locale';
import { Card, EmptyState, PageHeader } from '@/components/ui';

type ConnectInputs = {
  user: string;
  password: string;
  port: number;
  credentialId?: number;
  saveCredential?: boolean;
};

type ConnState =
  | { kind: 'idle' }
  | { kind: 'connecting' }
  | { kind: 'open' }
  | { kind: 'closed'; reason?: string };

// ANSI red wrapper for inline error messages. xterm renders the escape
// sequence so we don't need a separate DOM element for status lines.
function ansiRed(s: string): string {
  return `\x1b[31m${s}\x1b[0m`;
}

function ansiDim(s: string): string {
  return `\x1b[2m${s}\x1b[0m`;
}

export default function DeviceShellPage() {
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  // viewer is read-only. Short-circuit before any WS setup
  // happens — backend rejects too (skill execute / shell open both
  // require non-viewer), but stopping at the page boundary keeps the
  // user from staring at a half-loaded terminal that 403s on connect.
  if (!canMutate) {
    return (
      <main className="anim-fade flex flex-1 flex-col overflow-hidden p-6">
        <PageHeader title={tr('终端', 'Terminal')} subtitle={tr('WebSSH — admin / user only', 'WebSSH — admin / user only')} />
        <Card className="p-6">
          <EmptyState
            icon={Shield}
            title={tr('只读账号不能进入终端', 'Viewer accounts cannot open the terminal')}
            hint={tr('WebSSH 会让你直接登录设备 root shell，只有 admin / user 能打开。', 'WebSSH gives root shell access. Only admin and user roles can open it.')}
          />
        </Card>
      </main>
    );
  }

  return <DeviceShell />;
}

export function DeviceShell() {
  const { tr } = useI18n();
  const { deviceId = '' } = useParams<{ deviceId: string }>();
  const navigate = useNavigate();

  const [edge, setEdge] = useState<Edge | null>(null);
  const [edgeError, setEdgeError] = useState<string | null>(null);
  const [modalOpen, setModalOpen] = useState(true);
  const [conn, setConn] = useState<ConnState>({ kind: 'idle' });
  const [connectionError, setConnectionError] = useState<string | null>(null);

  // The terminal API + ws live on refs — they're side-effectful and
  // outliving any single render is the whole point of this page.
  const termRef = useRef<XTerminalApi | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  // Latest cols/rows reported by xterm. We need them when sending the
  // first `open` frame (called from a callback that doesn't have direct
  // access to the terminal's geometry).
  const sizeRef = useRef<{ cols: number; rows: number }>({ cols: 80, rows: 24 });
  // Track whether we sent the close frame — onbeforeunload + manual close
  // should never double-fire.
  const closedSentRef = useRef(false);
  const openConnectionRef = useRef<(inputs: ConnectInputs, acceptHostKey?: string) => Promise<void>>(async () => {});

  // Fetch device metadata for the title. The route param is the device_id
  // (Prom label). Manager does not yet expose GET /devices/{id}, so we
  // resolve the hostname by listing edges and matching device_id — matches
  // what Edges.tsx already does in-memory.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        // Cheap path: try direct lookup, fall back to list scan. The
        // manager's /edges/{id} expects an edge id, not a device id, so
        // we go straight to list. Limit=1000 mirrors the backend handler.
        const r = await listEdges();
        if (cancelled) return;
        const target = (r.items ?? []).find(
          (e) => String(e.device_id ?? '') === String(deviceId),
        );
        if (target) {
          // Refresh with full detail (host_info has more fields).
          try {
            const detail = await getEdge(target.id);
            if (!cancelled) setEdge(detail);
          } catch {
            if (!cancelled) setEdge(target);
          }
        } else {
          setEdgeError(tr('未找到该设备或设备未上线', 'Device not found or offline'));
        }
      } catch (err) {
        if (!cancelled) setEdgeError((err as Error).message || tr('加载设备信息失败', 'Failed to load device info'));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [deviceId, tr]);

  const tabTitle = useMemo(() => {
    if (!edge) return `Shell · ${deviceId}`;
    return `Shell · ${edge.name || deviceId}`;
  }, [edge, deviceId]);
  useEffect(() => {
    const prev = document.title;
    document.title = tabTitle;
    return () => {
      document.title = prev;
    };
  }, [tabTitle]);

  const writeBanner = useCallback((line: string) => {
    const t = termRef.current;
    if (!t) return;
    t.write(`\r\n${line}\r\n`);
  }, []);

  // sendCloseOnce guards against the WS-close + beforeunload double path.
  const sendCloseOnce = useCallback(() => {
    const ws = wsRef.current;
    if (!ws) return;
    if (closedSentRef.current) return;
    closedSentRef.current = true;
    try {
      sendControl(ws, { type: 'close' });
    } catch {
      /* WS may already be closing */
    }
  }, []);

  // Tear down the socket. Caller decides whether to also dispose the
  // terminal — usually we keep it so the user can read final output.
  const teardown = useCallback(() => {
    sendCloseOnce();
    const ws = wsRef.current;
    wsRef.current = null;
    if (ws && ws.readyState <= WebSocket.OPEN) {
      try {
        ws.close(1000, 'client');
      } catch {
        /* noop */
      }
    }
  }, [sendCloseOnce]);

  const closeTerminalPage = useCallback(() => {
    teardown();
    window.close();
    if (!window.closed) navigate('/devices', { replace: true });
  }, [navigate, teardown]);

  // beforeunload: best-effort polite close. We can't await the close
  // frame; gorilla writes it on the next read tick.
  useEffect(() => {
    const onUnload = () => {
      sendCloseOnce();
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        try {
          ws.close(1000, 'unload');
        } catch {
          /* noop */
        }
      }
    };
    window.addEventListener('beforeunload', onUnload);
    return () => {
      window.removeEventListener('beforeunload', onUnload);
      // Final teardown when page actually unmounts (route change).
      onUnload();
    };
  }, [sendCloseOnce]);

  // openConnection: wire WS handshake to the open frame. Called from the
  // modal Connect button.
  const openConnection = useCallback(
    async (inputs: ConnectInputs, acceptHostKey = '') => {
      // Tear down any previous socket (e.g. user hit "重连").
      teardown();
      closedSentRef.current = false;

      setConn({ kind: 'connecting' });
      setConnectionError(null);

      let ticket: string;
      try {
        const issued = await createShellTicket(deviceId, inputs.credentialId
          ? { credential_id: inputs.credentialId, accept_host_key: acceptHostKey || undefined }
          : {
            ssh_user: inputs.user,
            ssh_pass: inputs.password,
            ssh_port: inputs.port,
            save_credential: inputs.saveCredential,
            accept_host_key: acceptHostKey || undefined,
          });
        ticket = issued.ticket;
      } catch (err) {
        inputs.password = '';
        const message = (err as Error).message || tr('创建终端连接失败', 'Failed to create terminal connection');
        writeBanner(ansiRed(message));
        setConnectionError(message);
        setConn({ kind: 'closed', reason: 'ticket' });
        setModalOpen(true);
        return;
      }

      const ws = openShellSocket(deviceId, ticket);
      wsRef.current = ws;
      const encoder = new TextEncoder();

      ws.onopen = () => {
        if (wsRef.current !== ws) return;
        const { cols, rows } = sizeRef.current;
        sendControl(ws, {
          type: 'open',
          cols,
          rows,
          term: 'xterm-256color',
        });
      };

      ws.onmessage = (ev) => {
        if (wsRef.current !== ws) return;
        // Binary == stdout/stderr. xterm handles it directly.
        if (ev.data instanceof ArrayBuffer) {
          termRef.current?.write(new Uint8Array(ev.data));
          return;
        }
        if (typeof ev.data !== 'string') return;
        let frame: ShellControlFrameIn | null = null;
        try {
          frame = JSON.parse(ev.data) as ShellControlFrameIn;
        } catch {
          return;
        }
        if (!frame || typeof frame.type !== 'string') return;
        switch (frame.type) {
          case 'ready': {
            inputs.password = '';
            setConn({ kind: 'open' });
            writeBanner(ansiDim(tr(`-- SSH 已连接 (${inputs.user}@${edge?.name ?? deviceId}) --`, `-- SSH connected (${inputs.user}@${edge?.name ?? deviceId}) --`)));
            break;
          }
          case 'credential_save_error':
            writeBanner(ansiRed(tr(
              `SSH 已连接，但保存账户失败：${frame.message}`,
              `SSH connected, but saving the account failed: ${frame.message}`,
            )));
            break;
          case 'auth_error': {
            inputs.password = '';
            const message = tr(
              `SSH 认证失败：${frame.message || '用户名或密码错误'}`,
              `SSH auth failed: ${frame.message || 'invalid username or password'}`,
            );
            writeBanner(ansiRed(message));
            setConnectionError(message);
            setConn({ kind: 'closed', reason: 'auth' });
            // Re-open the modal so the user can retry without leaving.
            setModalOpen(true);
            break;
          }
          case 'host_key_unknown':
            if (confirm(tr(
              `首次连接此 SSH 服务。确认信任主机指纹？\n${frame.fingerprint}`,
              `First connection to this SSH service. Trust this host key?\n${frame.fingerprint}`,
            ))) {
              teardown();
              void openConnectionRef.current(inputs, frame.fingerprint);
            } else {
              inputs.password = '';
              setConnectionError(tr('未信任 SSH 主机指纹，连接已取消', 'SSH host key was not trusted; connection cancelled'));
              setConn({ kind: 'closed', reason: 'host-key' });
              setModalOpen(true);
            }
            break;
          case 'host_key_changed': {
            inputs.password = '';
            const message = tr(
              `SSH 主机指纹已变化，已阻止连接。原指纹：${frame.expected}，当前：${frame.fingerprint}`,
              `SSH host key changed; connection blocked. Expected: ${frame.expected}, actual: ${frame.fingerprint}`,
            );
            writeBanner(ansiRed(message));
            setConnectionError(message);
            setConn({ kind: 'closed', reason: 'host-key' });
            setModalOpen(true);
            break;
          }
          case 'exit':
            inputs.password = '';
            closeTerminalPage();
            break;
        }
      };

      ws.onerror = () => {
        if (wsRef.current !== ws) return;
        const message = tr('WebSocket 连接错误', 'WebSocket connection error');
        writeBanner(ansiRed(message));
        setConnectionError(message);
      };

      ws.onclose = (ev) => {
        if (wsRef.current !== ws) return;
        inputs.password = '';
        if (!closedSentRef.current && ev.code === 1006) {
          const message = tr('连接异常断开', 'Connection dropped unexpectedly');
          writeBanner(ansiRed(message));
          setConnectionError(message);
          setModalOpen(true);
        } else if (ev.code !== 1000 && ev.code !== 1005) {
          writeBanner(
            ansiDim(tr(
              `-- 连接关闭 (code=${ev.code}${ev.reason ? `, ${ev.reason}` : ''}) --`,
              `-- Connection closed (code=${ev.code}${ev.reason ? `, ${ev.reason}` : ''}) --`,
            )),
          );
        }
        setConn((s) => (s.kind === 'closed' ? s : { kind: 'closed' }));
        wsRef.current = null;
      };

      // Wire xterm.onData to ws.send via the upper-scope ref. Set up here
      // so each new socket gets a fresh closure with its own encoder.
      pumpRef.current = (data: string) => {
        if (ws.readyState !== WebSocket.OPEN) return;
        ws.send(encoder.encode(data));
      };
    },
    [closeTerminalPage, deviceId, edge, teardown, tr, writeBanner],
  );
  openConnectionRef.current = openConnection;

  // Wire xterm onData → ws via a ref-based pump so we don't have to
  // re-mount the terminal when the socket changes (reconnect).
  const pumpRef = useRef<(data: string) => void>(() => {});

  const onTermData = useCallback((data: string) => {
    pumpRef.current(data);
  }, []);

  const onTermResize = useCallback((cols: number, rows: number) => {
    sizeRef.current = { cols, rows };
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      sendControl(ws, { type: 'resize', cols, rows });
    }
  }, []);

  const attachTerm = useCallback((api: XTerminalApi) => {
    termRef.current = api;
  }, []);

  // Modal submit handler. Password is dropped on the floor after this returns.
  const handleConnect = useCallback(
    (inputs: ConnectInputs) => {
      setModalOpen(false);
      openConnection(inputs);
    },
    [openConnection],
  );

  const handleReconnect = useCallback(() => {
    teardown();
    setConn({ kind: 'idle' });
    setModalOpen(true);
  }, [teardown]);

  const statusLabel = useMemo(() => {
    switch (conn.kind) {
      case 'idle':
        return tr('未连接', 'Disconnected');
      case 'connecting':
        return tr('连接中…', 'Connecting…');
      case 'open':
        return tr('已连接', 'Connected');
      case 'closed':
        return tr('已断开', 'Closed');
    }
  }, [conn, tr]);

  const hostname =
    extractHostname(edge?.host_info) || edge?.name || deviceId || tr('设备', 'device');

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden bg-zinc-950">
      <header className="flex items-center justify-between border-b border-zinc-800/60 bg-zinc-900/60 px-4 py-2">
        <div className="flex min-w-0 items-center gap-2 text-xs text-zinc-300">
          <TerminalIcon size={14} className="text-zinc-500" />
          <span className="truncate font-medium text-zinc-100">{hostname}</span>
          <span className="text-zinc-600">·</span>
          <span
            className={
              conn.kind === 'open'
                ? 'text-emerald-400'
                : conn.kind === 'connecting'
                  ? 'text-amber-300'
                  : 'text-zinc-500'
            }
          >
            {statusLabel}
          </span>
          {edgeError && (
            <span className="ml-2 text-red-400">· {edgeError}</span>
          )}
        </div>
        <div className="flex items-center gap-1.5">
          <Button
            variant="ghost"
            onClick={handleReconnect}
            aria-label={tr('重新连接', 'Reconnect')}
          >
            <RotateCw size={12} />
            {tr('重连', 'Reconnect')}
          </Button>
          <Button
            variant="ghost"
            onClick={closeTerminalPage}
            aria-label={tr('关闭终端', 'Close terminal')}
          >
            <Power size={12} />
            {tr('关闭', 'Close')}
          </Button>
        </div>
      </header>

      <div className="flex-1 overflow-hidden p-2">
        <XTerminal
          onData={onTermData}
          onResize={onTermResize}
          attachRef={attachTerm}
        />
      </div>

      <ConnectModal
        open={modalOpen}
        deviceId={deviceId}
        title={tr(`连接到 ${hostname}`, `Connect to ${hostname}`)}
        connectionError={connectionError}
        onCancel={() => {
          // If we never connected, leave the page; otherwise just hide
          // the modal (terminal is still useful for reading prior output).
          if (conn.kind === 'idle' || conn.kind === 'closed') {
            navigate('/devices');
          } else {
            setModalOpen(false);
          }
        }}
        onSubmit={handleConnect}
      />
    </main>
  );
}

// ConnectModal collects ssh user / password / port. We keep it inline so
// the password lifetime is bounded by this component's mount window.
export function ConnectModal({
  open,
  deviceId,
  title,
  connectionError,
  onSubmit,
  onCancel,
}: {
  open: boolean;
  deviceId: string;
  title: string;
  connectionError?: string | null;
  onSubmit(inputs: ConnectInputs): void;
  onCancel(): void;
}) {
  const { tr } = useI18n();
  const [user, setUser] = useState('root');
  const [password, setPassword] = useState('');
  const [port, setPort] = useState<string>('22');
  const [err, setErr] = useState<string | null>(null);
  const [credentials, setCredentials] = useState<ShellCredential[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [saveCredential, setSaveCredential] = useState(false);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setErr(null);
    setUser('root');
    setPassword('');
    setSelected(null);
    setLoading(true);
    void listShellCredentials(deviceId)
      .then((result) => {
        if (cancelled) return;
        const items = result.items ?? [];
        setCredentials(items);
        setSelected(items[0] ? String(items[0].id) : 'new');
      })
      .catch((cause) => {
        if (cancelled) return;
        setCredentials([]);
        setSelected('new');
        setErr((cause as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, deviceId]);

  if (!open) return null;

  const submit = () => {
    if (selected && selected !== 'new') {
      const credential = credentials.find((item) => String(item.id) === selected);
      if (!credential) {
        setErr(tr('保存的凭据不存在，请刷新后重试', 'Saved credential no longer exists; refresh and retry'));
        return;
      }
      onSubmit({ user: credential.ssh_user, password: '', port: credential.ssh_port, credentialId: credential.id });
      return;
    }
    const u = user.trim();
    if (!u) {
      setErr(tr('请输入 OS 用户名', 'Please enter the OS username'));
      return;
    }
    if (!password) {
      setErr(tr('请输入密码', 'Please enter the password'));
      return;
    }
    const p = Number(port || '22');
    if (!Number.isFinite(p) || p < 1 || p > 65535) {
      setErr(tr('端口必须在 1-65535 之间', 'Port must be between 1 and 65535'));
      return;
    }
    onSubmit({ user: u, password, port: p, saveCredential });
    setPassword('');
  };

  const removeCredential = async (credential: ShellCredential) => {
    if (!confirm(tr(`删除账户“${credential.ssh_user}”？`, `Delete account “${credential.ssh_user}”?`))) return;
    setBusy(true);
    try {
      await deleteShellCredential(deviceId, credential.id);
      const next = credentials.filter((item) => item.id !== credential.id);
      setCredentials(next);
      setSelected(next[0] ? String(next[0].id) : 'new');
    } catch (cause) {
      setErr((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const resetKnownHost = async (sshPort: number) => {
    if (!confirm(tr(`重置端口 ${sshPort} 的主机指纹信任？`, `Reset trusted host key for port ${sshPort}?`))) return;
    setBusy(true);
    try {
      await resetShellKnownHost(deviceId, sshPort);
    } catch (cause) {
      setErr((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onCancel}
      title={title}
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onCancel}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || loading || !selected}>
            {busy ? tr('连接中…', 'Connecting…') : tr('连接', 'Connect')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {loading && (
          <div className="py-8 text-center text-xs text-zinc-500">
            {tr('正在加载已保存账户…', 'Loading saved accounts…')}
          </div>
        )}

        {!loading && selected !== 'new' && (
          <>
            <div className="flex items-center gap-2 text-[11px] font-medium text-zinc-500">
              <KeyRound size={13} />
              {tr('已保存账户', 'Saved accounts')}
            </div>
            <div role="radiogroup" aria-label={tr('已保存账户', 'Saved accounts')} className="space-y-2">
              {credentials.map((credential) => {
                const checked = selected === String(credential.id);
                return (
                  <div
                    key={credential.id}
                    className={`flex items-center rounded-lg border transition-colors ${checked ? 'border-indigo-500/50 bg-indigo-500/10' : 'border-zinc-800 bg-zinc-950 hover:border-zinc-700'}`}
                  >
                    <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-3 px-3 py-2.5">
                      <input
                        type="radio"
                        name="webssh-account"
                        value={credential.id}
                        checked={checked}
                        onChange={() => setSelected(String(credential.id))}
                        className="h-3.5 w-3.5 accent-indigo-500"
                      />
                      <UserRound size={16} className="shrink-0 text-zinc-500" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-xs font-medium text-zinc-100">{credential.ssh_user}</span>
                        <span className="mt-0.5 block text-[11px] text-zinc-500">127.0.0.1 · {tr('端口', 'Port')} {credential.ssh_port}</span>
                      </span>
                    </label>
                    <div className="flex items-center gap-0.5 pr-2">
                      <button
                        type="button"
                        onClick={() => void resetKnownHost(credential.ssh_port)}
                        disabled={busy}
                        aria-label={tr(`重置 ${credential.ssh_user} 的主机信任`, `Reset host trust for ${credential.ssh_user}`)}
                        title={tr('重置主机信任', 'Reset host trust')}
                        className="rounded-md p-1.5 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-40"
                      >
                        <ShieldCheck size={14} />
                      </button>
                      <button
                        type="button"
                        onClick={() => void removeCredential(credential)}
                        disabled={busy}
                        aria-label={tr(`删除 ${credential.ssh_user}`, `Delete ${credential.ssh_user}`)}
                        title={tr('删除账户', 'Delete account')}
                        className="rounded-md p-1.5 text-zinc-500 hover:bg-red-500/10 hover:text-red-400 disabled:opacity-40"
                      >
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
            <button
              type="button"
              onClick={() => {
                setSelected('new');
                setUser('root');
                setPassword('');
                setPort('22');
                setSaveCredential(false);
                setErr(null);
              }}
              className="flex w-full items-center justify-center gap-1.5 rounded-lg border border-dashed border-zinc-700 px-3 py-2.5 text-xs font-medium text-zinc-400 transition-colors hover:border-zinc-600 hover:bg-zinc-800 hover:text-zinc-100"
            >
              <Plus size={14} />
              {tr('新增账户', 'Add account')}
            </button>
          </>
        )}

        {!loading && selected === 'new' && (
          <>
            {credentials.length > 0 && (
              <button
                type="button"
                onClick={() => {
                  setSelected(String(credentials[0].id));
                  setErr(null);
                }}
                className="flex items-center gap-1 text-[11px] text-zinc-500 hover:text-zinc-200"
              >
                <ChevronLeft size={13} />
                {tr('返回已保存账户', 'Back to saved accounts')}
              </button>
            )}
            <div className="flex items-center gap-2 text-[11px] font-medium text-zinc-500">
              <Plus size={13} />
              {tr('新增账户', 'Add account')}
            </div>
            <div className="grid grid-cols-[minmax(0,1fr)_96px] gap-3">
              <div>
                <label htmlFor="webssh-user" className="mb-1 block text-[11px] text-zinc-500">
                  {tr('OS 用户', 'OS user')}
                </label>
                <input
                  id="webssh-user"
                  autoFocus
                  autoComplete="off"
                  value={user}
                  onChange={(e) => setUser(e.target.value)}
                  className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-2 text-xs text-zinc-100 focus:border-indigo-500 focus:outline-none"
                />
              </div>
              <div>
                <label htmlFor="webssh-port" className="mb-1 block text-[11px] text-zinc-500">
                  {tr('SSH 端口', 'SSH port')}
                </label>
                <input
                  id="webssh-port"
                  inputMode="numeric"
                  value={port}
                  onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ''))}
                  placeholder="22"
                  className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-2 text-xs text-zinc-100 focus:border-indigo-500 focus:outline-none"
                />
              </div>
            </div>
            <div>
              <label htmlFor="webssh-pass" className="mb-1 block text-[11px] text-zinc-500">
                {tr('密码', 'Password')}
              </label>
              <input
                id="webssh-pass"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-2 text-xs text-zinc-100 focus:border-indigo-500 focus:outline-none"
                onKeyDown={(e) => {
                  if (e.key === 'Enter') void submit();
                }}
              />
            </div>
            <label className="flex cursor-pointer items-center gap-2 text-xs text-zinc-300">
              <input
                type="checkbox"
                checked={saveCredential}
                onChange={(e) => setSaveCredential(e.target.checked)}
                className="h-3.5 w-3.5 accent-indigo-500"
              />
              {tr('保存此账户，下次可直接登录', 'Save this account for one-click login next time')}
            </label>
            <p className="text-[11px] leading-4 text-zinc-600">
              {tr('连接目标固定为设备本机 127.0.0.1，可使用任意有效 SSH 端口。', 'Connections target device loopback 127.0.0.1 and may use any valid SSH port.')}
            </p>
          </>
        )}
        {(err || connectionError) && (
          <div
            role="alert"
            className="rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
          >
            {err || connectionError}
          </div>
        )}
        {!loading && selected !== 'new' && (
          <p className="text-[11px] leading-4 text-zinc-600">
            {tr('保存的密码由 Manager 加密，仅对你的 Ongrid 账号和当前设备可用。', 'Saved passwords are encrypted by Manager and available only to your Ongrid account on this device.')}
          </p>
        )}
      </div>
    </Modal>
  );
}

// extractHostname is a slimmed-down copy of the helper in Edges.tsx; we
// duplicate rather than refactor because the field shape isn't fixed
// across edge versions and inlining keeps the dependency graph simple.
function extractHostname(hostInfo: Edge['host_info']): string | null {
  if (!hostInfo) return null;
  const obj =
    typeof hostInfo === 'string' ? safeParse(hostInfo) : hostInfo;
  if (!obj || typeof obj !== 'object') return null;
  const candidates = [
    (obj as Record<string, unknown>).hostname,
    (obj as Record<string, unknown>).hostName,
    (obj as Record<string, unknown>).nodename,
    (obj as Record<string, unknown>).host,
  ];
  for (const c of candidates) {
    if (typeof c === 'string' && c.trim()) {
      const v = c.trim();
      return v.includes(':') ? v.split(':')[0] || v : v;
    }
  }
  return null;
}

function safeParse(s: string): Record<string, unknown> | null {
  try {
    const v = JSON.parse(s) as unknown;
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}
