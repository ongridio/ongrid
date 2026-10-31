// The CMDB module's HTTP surface.
//
// IT IS A THIN SHELL OVER `@/api/client`, not a second client: auth, the 401
// refresh-and-retry, the locale header and the error shape all live there, and
// duplicating any of it here would give the CMDB its own idea of when a session
// expired.
//
// WHAT THIS ADDS IS TWO THINGS THE CMDB ENDPOINTS NEED ON EVERY CALL: a query
// string built from a typed record, and the house list shape `{items, total}`
// that the backend fixes for every list endpoint.

import { request } from '@/api/client';

/** The list shape every CMDB collection endpoint returns. */
export type ListResponse<T> = {
  items: T[];
  total: number;
};

/** A query-string record. `undefined` and `null` are omitted. */
export type Query = Record<string, string | number | boolean | undefined | null>;

/**
 * Builds a query string.
 *
 * A `false` IS SENT, and an absent value is not. The difference matters for the
 * CMDB: `reverse=false` and "no `reverse` at all" are the same request, but
 * `enabled=false` and "no `enabled`" are not — a filter that silently dropped
 * `false` would show every row when the operator asked for the disabled ones.
 */
export function queryString(params?: Query): string {
  if (!params) return '';
  const parts: string[] = [];
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null) continue;
    parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);
  }
  return parts.length === 0 ? '' : `?${parts.join('&')}`;
}

export function cmdbGet<T>(path: string, params?: Query, signal?: AbortSignal): Promise<T> {
  return request<T>('GET', `/cmdb${path}${queryString(params)}`, undefined, { signal });
}

export function cmdbPost<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  return request<T>('POST', `/cmdb${path}`, body, signal ? { signal } : undefined);
}

export function cmdbPut<T>(path: string, body?: unknown): Promise<T> {
  return request<T>('PUT', `/cmdb${path}`, body);
}

export function cmdbDelete(path: string, body?: unknown): Promise<void> {
  return request<void>('DELETE', `/cmdb${path}`, body);
}

/**
 * The message to show an operator for a failed call.
 *
 * A BACKEND REFUSAL IS PLAIN TEXT and already names what is wrong — "the mapping
 * has to carry the attribute that identifies an instance: hostname must appear
 * among…" — so it is shown as-is. Only a failure with no message at all gets a
 * generic one, because "HTTP 500" tells the operator nothing they can act on.
 */
export function describeError(err: unknown, fallback: string): string {
  if (err instanceof Error && err.message.trim()) return err.message.trim();
  if (typeof err === 'string' && err.trim()) return err.trim();
  return fallback;
}
