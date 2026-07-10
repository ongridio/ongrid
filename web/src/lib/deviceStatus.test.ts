import { describe, expect, it } from 'vitest';
import type { Incident } from '@/api/alerts';
import { buildOfflineAlertDeviceIDs, resolveDevicePresence } from './deviceStatus';

describe('deviceStatus', () => {
  it('prefers an open device_offline alert over tunnel status', () => {
    const alerts = buildOfflineAlertDeviceIDs([
      { id: 1, rule_key: 'device_offline', rule_name: '设备离线', severity: 'critical', status: 'open', summary: '', target_id: '42', event_count: 1, fired_at: '', last_fired_at: '', updated_at: '' },
    ] as Incident[]);

    expect(resolveDevicePresence({ device_id: 42, status: 'online', last_seen_at: new Date().toISOString() }, alerts)).toBe('offline-alert');
  });

  it('marks stale heartbeats as degraded while tunnel status is still online', () => {
    const now = Date.parse('2026-07-03T10:00:00.000Z');
    expect(resolveDevicePresence({ device_id: 7, status: 'online', last_seen_at: '2026-07-03T09:58:00.000Z' }, new Set(), now)).toBe('heartbeat-stale');
  });

  it('keeps fresh online status unchanged', () => {
    const now = Date.parse('2026-07-03T10:00:00.000Z');
    expect(resolveDevicePresence({ device_id: 7, status: 'online', last_seen_at: '2026-07-03T09:59:30.000Z' }, new Set(), now)).toBe('online');
  });
});
