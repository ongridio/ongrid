import { request } from '@/api/client';

export type HealthStatus = 'ok' | 'degraded' | 'failed' | 'unknown';

export type HealthCheck = {
  id: string;
  group: string;
  label: string;
  status: HealthStatus;
  message: string;
  details?: Record<string, unknown>;
  duration_ms: number;
};

export type HealthSummary = {
  ok: number;
  degraded: number;
  failed: number;
  unknown: number;
};

export type HealthReport = {
  status: HealthStatus;
  checked_at: string;
  summary: HealthSummary;
  checks: HealthCheck[];
};

export function runSystemHealthCheck() {
  return request<HealthReport>('POST', '/system/health/check');
}
