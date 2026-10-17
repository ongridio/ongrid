import { getToken } from '@/store/auth';
import { request } from './client';

export type PacketCaptureState =
  | 'pending_approval'
  | 'queued'
  | 'dispatching'
  | 'capturing'
  | 'uploading'
  | 'parsing'
  | 'ready'
  | 'cancelled'
  | 'failed'
  | 'raw_expired'
  | 'expired'
  | 'deleted';

export type PacketCapture = {
  id: number;
  created_by: number;
  source: string;
  state: PacketCaptureState;
  edge_id: number;
  device_id: number;
  session_id?: number;
  interface_name: string;
  network_namespace?: string;
  canonical_filter: string;
  direction: string;
  format: string;
  promiscuous: boolean;
  duration_seconds: number;
  max_bytes: number;
  max_packets: number;
  snaplen: number;
  title: string;
  description: string;
  captured_bytes: number;
  captured_packets: number;
  live_preview?: string[];
  artifact_id?: string;
  raw_available?: boolean;
  analysis?: PacketCaptureAnalysis;
  error_code?: string;
  error_detail?: string;
  started_at?: string;
  finished_at?: string;
  created_at: string;
  updated_at: string;
};

export type PacketCaptureSession = {
  id: string;
  source: string;
  pcap_count: number;
  state: 'collecting' | 'ready' | 'partial' | 'cancelled' | 'failed';
  title: string;
  description: string;
  canonical_filter: string;
  duration_seconds: number;
  planned_start_at: string;
  clock_quality: string;
  analysis?: PacketCaptureSessionAnalysis;
  created_at: string;
  updated_at: string;
};
export type PacketCaptureSessionAnalysis = {
  summary: { capture_count: number; ready_count: number; flow_count: number; event_count: number; clock_quality: string; warning: string };
  flows: Array<{ id: string; protocol: string; endpoints: string[]; edge_ids: number[]; missing_edge_ids?: number[]; packets: number; first_seen_at: string; last_seen_at: string }>;
  timeline: Array<{ capture_id: number; artifact_id: string; edge_id: number; device_id: number; timestamp: string; source: string; destination: string; protocol: string; length: number; info: string; flow_id: string }>;
};

export type PacketCaptureAnalysis = {
  artifact_id?: string;
  summary?: {
    packets_seen?: number;
    packets_returned?: number;
    bytes_seen?: number;
    truncated?: boolean;
    decode_errors?: number;
    [key: string]: unknown;
  };
  packets?: PacketCapturePacket[];
  meta?: Record<string, unknown>;
};

export type PacketCapturePacket = {
  number?: number;
  observed_at?: string;
  source?: string;
  destination?: string;
  protocol?: string;
  length?: number;
  info?: string;
  tcp_stream?: number;
  index?: Record<string, unknown>;
  protocol_tree?: PacketProtocolNode[];
  hex?: PacketHexLine[];
  [key: string]: unknown;
};

export type PacketProtocolNode = {
  name?: string;
  offset?: number;
  length?: number;
  fields?: PacketProtocolField[];
  children?: PacketProtocolNode[];
};

export type PacketProtocolField = {
  name?: string;
  value?: unknown;
  offset?: number;
  length?: number;
};

export type PacketHexLine = {
  offset?: number;
  data?: string | number[];
};

export type PacketCaptureInput = {
  device_id: number;
  interface: string;
  network_namespace?: string;
  filter?: string;
  duration_seconds?: number;
  max_bytes?: number;
  max_packets?: number;
  snaplen?: number;
  promiscuous?: boolean;
  title?: string;
  description?: string;
  request_idempotency_key?: string;
};

export type PacketCaptureSessionInput = {
  targets: Array<{ device_id: number; interface: string; network_namespace?: string }>;
  filter?: string;
  duration_seconds?: number;
  max_bytes?: number;
  max_packets?: number;
  snaplen?: number;
  promiscuous?: boolean;
  title?: string;
  description?: string;
};

export function listPacketCaptures(params?: {
  device_id?: number;
  edge_id?: number;
  state?: string;
  limit?: number;
  offset?: number;
}) {
  const q = new URLSearchParams();
  if (params?.device_id) q.set('device_id', String(params.device_id));
  if (params?.edge_id) q.set('edge_id', String(params.edge_id));
  if (params?.state) q.set('state', params.state);
  if (params?.limit) q.set('limit', String(params.limit));
  if (params?.offset) q.set('offset', String(params.offset));
  const suffix = q.toString();
  return request<{ items: PacketCapture[]; total: number }>(
    'GET',
    `/packet-captures${suffix ? `?${suffix}` : ''}`,
  );
}

export function createPacketCapture(input: PacketCaptureInput) {
  return request<PacketCapture>('POST', '/packet-captures', input);
}

export function createPacketCaptureSession(input: PacketCaptureSessionInput) {
  return request<{ session: PacketCaptureSession; captures: PacketCapture[]; member_errors?: string[] }>(
    'POST',
    '/packet-capture-sessions',
    input,
  );
}

export function getPacketCapture(id: number) {
  return request<PacketCapture>('GET', `/packet-captures/${id}`);
}

export function getPacketCaptureArtifact(artifactID: string) {
  return request<PacketCapture>('GET', `/packet-captures/artifacts/${encodeURIComponent(artifactID)}`);
}

export function refreshPacketCapture(id: number) {
  return request<PacketCapture>('POST', `/packet-captures/${id}/refresh`, {});
}

export function cancelPacketCapture(id: number) {
  return request<PacketCapture>('POST', `/packet-captures/${id}/cancel`, {});
}

export function stopPacketCapture(id: number) {
  return request<PacketCapture>('POST', `/packet-captures/${id}/stop`, {});
}

export function listPacketCaptureSessions(params?: { limit?: number; offset?: number }) {
  const q = new URLSearchParams();
  if (params?.limit) q.set('limit', String(params.limit));
  if (params?.offset) q.set('offset', String(params.offset));
  const suffix = q.toString();
  return request<{ items: PacketCaptureSession[]; total: number }>('GET', `/packet-capture-sessions${suffix ? `?${suffix}` : ''}`);
}
export function getPacketCaptureSession(id: string) { return request<{ session: PacketCaptureSession; captures: PacketCapture[] }>('GET', `/packet-capture-sessions/${encodeURIComponent(id)}`); }
export function refreshPacketCaptureSession(id: string) { return request<{ session: PacketCaptureSession; captures: PacketCapture[] }>('POST', `/packet-capture-sessions/${encodeURIComponent(id)}/refresh`, {}); }
export function stopPacketCaptureSession(id: string) { return request<PacketCaptureSession>('POST', `/packet-capture-sessions/${encodeURIComponent(id)}/stop`, {}); }

export function packetCaptureArtifactID(capture: Pick<PacketCapture, 'id' | 'artifact_id'>) {
  return capture.artifact_id || `pcap-${capture.id}`;
}

export async function downloadPacketCapture(capture: Pick<PacketCapture, 'id' | 'artifact_id'>) {
  const token = getToken();
  const res = await fetch(`/api/v1/packet-captures/${capture.id}/download`, {
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(text.trim() || `HTTP ${res.status}`);
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = `${packetCaptureArtifactID(capture)}.pcap`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
