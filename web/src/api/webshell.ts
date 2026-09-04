// webshell.ts — thin WebSocket factory for the WebSSH endpoint, plus
// typed wrappers for the audit + kill endpoints.
//
// Backend contract (manager/server/webshell):
//   wss://<host>/api/v1/devices/{device_id}/shell        (WS upgrade)
//   GET    /api/v1/webshell/sessions                     (audit list)
//   DELETE /api/v1/webshell/sessions/{id}                (admin kill)
//
// Auth: an authenticated REST call first mints a one-time 30-second ticket;
// only that capability appears in the WebSocket URL. SSH passwords never
// travel in WebSocket frames and JWTs never appear in query strings.
//
// The caller is responsible for setting `binaryType = 'arraybuffer'`,
// sending the first `ShellOpen` text frame, and tearing down on close.

import { request } from './client';

export type OpenShellParams = {
  // Optional WS path override (defaults to same-origin /api/v1).
  baseUrl?: string;
};

const SUBPROTOCOL = 'ongrid.shell.v1';

export function openShellSocket(
  deviceId: number | string,
  ticket: string,
  params: OpenShellParams = {},
): WebSocket {
  const id = encodeURIComponent(String(deviceId));
  const qs = new URLSearchParams({ ticket }).toString();

  let url: string;
  if (params.baseUrl) {
    url = `${params.baseUrl.replace(/\/$/, '')}/api/v1/devices/${id}/shell?${qs}`;
  } else {
    // Derive ws/wss from the current page so dev (vite proxy) and prod
    // (nginx) both work without config.
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    url = `${proto}//${window.location.host}/api/v1/devices/${id}/shell?${qs}`;
  }

  const ws = new WebSocket(url, [SUBPROTOCOL]);
  ws.binaryType = 'arraybuffer';
  return ws;
}

export type ShellOpenFrame = {
  type: 'open';
  cols: number;
  rows: number;
  term: string;
};

export type ShellResizeFrame = {
  type: 'resize';
  cols: number;
  rows: number;
};

export type ShellCloseFrame = {
  type: 'close';
};

export type ShellControlFrameOut =
  | ShellOpenFrame
  | ShellResizeFrame
  | ShellCloseFrame;

// ShellControlFrameIn is what the manager pushes back over text frames.
// `ready` confirms the SSH session is up; `auth_error` is retryable and
// top-level SSH `exit` leaves the terminal page.
export type ShellControlFrameIn =
  | { type: 'ready' }
  | { type: 'auth_error'; message: string }
  | { type: 'credential_save_error'; message: string }
  | { type: 'host_key_unknown'; fingerprint: string }
  | { type: 'host_key_changed'; fingerprint: string; expected: string }
  | { type: 'exit'; exit_code: number; message?: string };

export function sendControl(ws: WebSocket, frame: ShellControlFrameOut): void {
  if (ws.readyState !== WebSocket.OPEN) return;
  ws.send(JSON.stringify(frame));
}

// ---------------------------------------------------------------------------
// Audit + kill endpoints (used by /settings/webshell)
// ---------------------------------------------------------------------------

// TerminatedBy mirrors backend wsmodel.TerminatedBy* — kept as a string
// union so callers can switch exhaustively. New backend values land here
// without breaking the build because the field is also typed as `string`
// inside ShellSession (defensive widening).
export type WebshellTerminatedBy =
  | 'user'
  | 'idle'
  | 'disconnect'
  | 'admin_kill'
  | 'ssh_auth_fail'
  | 'ssh_host_key'
  | 'ssh_exit'
  | 'device_offline';

export type ShellSession = {
  id: string;
  ongrid_user_id: number;
  ssh_user: string;
  ssh_port?: number;
  device_id: number;
  edge_id: number;
  started_at: string;
  ended_at?: string | null;
  bytes_stdin: number;
  bytes_stdout: number;
  exit_code: number;
  terminated_by?: WebshellTerminatedBy | string;
  is_active: boolean;
};

export type ShellSessionListResp = {
  items: ShellSession[];
  total: number;
};

export function listShellSessions(): Promise<ShellSessionListResp> {
  return request<ShellSessionListResp>('GET', '/webshell/sessions');
}

export function killShellSession(id: string): Promise<void> {
  return request<void>('DELETE', `/webshell/sessions/${encodeURIComponent(id)}`);
}

export type ShellCredential = {
  id: number;
  ssh_user: string;
  ssh_port: number;
  last_used_at?: string;
  created_at: string;
};

export type ShellTicketInput = {
  credential_id?: number;
  ssh_user?: string;
  ssh_pass?: string;
  ssh_port?: number;
  save_credential?: boolean;
  accept_host_key?: string;
};

export function createShellTicket(deviceId: number | string, input: ShellTicketInput) {
  return request<{ ticket: string; expires_at: string }>('POST', `/devices/${encodeURIComponent(String(deviceId))}/shell/tickets`, input);
}

export function listShellCredentials(deviceId: number | string) {
  return request<{ items: ShellCredential[] }>('GET', `/devices/${encodeURIComponent(String(deviceId))}/shell/credentials`);
}

export function deleteShellCredential(deviceId: number | string, credentialId: number) {
  return request<void>('DELETE', `/devices/${encodeURIComponent(String(deviceId))}/shell/credentials/${credentialId}`);
}

export function resetShellKnownHost(deviceId: number | string, port: number) {
  return request<void>('DELETE', `/devices/${encodeURIComponent(String(deviceId))}/shell/known-hosts/${port}`);
}
