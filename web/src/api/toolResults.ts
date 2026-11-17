import { request, ApiError } from './client';
import { getToken } from '@/store/auth';

export type SavedResult = {
  id: string;
  capture_status: string;
  storage_status: string;
  execution_status: string;
  captured_bytes: number;
  expires_at: string;
};
export type ResultPage = {
  items: { start: number; end: number; text?: string; data_json?: string; fragment?: boolean }[];
  next_cursor?: string;
  scan_start: number;
  scan_end: number;
  scan_complete: boolean;
  notice?: string;
};
const path = (session: string, result: string) => `/chat/sessions/${encodeURIComponent(session)}/tool-results/${encodeURIComponent(result)}`;
export function getSavedResult(session: string, result: string, signal?: AbortSignal) {
  return request<SavedResult>('GET', path(session, result), undefined, { signal });
}
export function readSavedResult(session: string, result: string, keyword: string, cursor?: string, signal?: AbortSignal) {
  return request<ResultPage>('POST', `${path(session, result)}/${keyword ? 'query' : 'read'}`, { mode: 'lines', contains: keyword || undefined, cursor, limit: 50 }, { signal });
}
export async function downloadSavedResult(session: string, result: string, signal?: AbortSignal) {
  const token = getToken();
  const res = await fetch(`/api/v1${path(session, result)}/download`, { headers: token ? { Authorization: `Bearer ${token}` } : {}, signal });
  if (!res.ok) throw new ApiError(await res.text(), res.status);
  return res.blob();
}
