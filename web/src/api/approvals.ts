import { request } from './client';

// Approvals (propose-confirm inbox) API — /v1/approvals/* (HLD-017).
// A dangerous action proposed by an agent (or a flow approval node) waits
// here for a human to approve (→ executes) or reject. Admin-only.

export interface Approval {
  runtime_version?: string;
  id: string;
  kind: string;
  title: string;
  summary: string;
  payload: string;
  source: string;
  session_id?: string;
  status: 'pending' | 'approved' | 'rejected' | 'executed' | 'failed';
  proposed_by: number;
  approved_by?: number;
  reason?: string;
  result?: string;
  created_at: string;
  decided_at?: string;
  executed_at?: string;
}

export function listApprovals(status?: string) {
  const qs = status ? `?status=${encodeURIComponent(status)}` : '';
  return request<{ items: Approval[] }>('GET', `/approvals${qs}`);
}

export function approvalsPendingCount() {
  return request<{ pending: number }>('GET', '/approvals/count');
}

export function getApproval(id: string) {
  return request<Approval>('GET', `/approvals/${id}`);
}

export function approveApproval(id: string) {
  return request<Approval>('POST', `/approvals/${id}/approve`);
}

export function rejectApproval(id: string, reason: string) {
  return request<{ ok: boolean }>('POST', `/approvals/${id}/reject`, { reason });
}
