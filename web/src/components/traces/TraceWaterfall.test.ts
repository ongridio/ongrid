import { describe, expect, it, vi } from 'vitest';
import { createElement } from 'react';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import type { TraceGetResponse } from '@/api/traces';
import { TraceWaterfall, buildTraceWaterfallModel } from './TraceWaterfall';

describe('buildTraceWaterfallModel', () => {
  it('builds an ordered span tree and keeps orphan spans visible', () => {
    const trace: TraceGetResponse = {
      resourceSpans: [
        {
          resource: { attributes: [{ key: 'service.name', value: { stringValue: 'api' } }] },
          scopeSpans: [{
            spans: [
              { spanId: 'root', name: 'GET /orders', startTimeUnixNano: '1000000000', endTimeUnixNano: '5000000000' },
              { spanId: 'late', parentSpanId: 'root', name: 'publish', startTimeUnixNano: '3000000000', endTimeUnixNano: '4500000000', status: { code: 2 } },
              {
                spanId: 'early',
                parentSpanId: 'root',
                name: 'DB SELECT',
                startTimeUnixNano: '1200000000',
                endTimeUnixNano: '1800000000',
                attributes: [{ key: 'peer.service', value: { stringValue: 'mysql' } }],
              },
              { spanId: 'orphan', parentSpanId: 'missing', name: 'detached', startTimeUnixNano: '2000000000', endTimeUnixNano: '2100000000' },
            ],
          }],
        },
      ],
    };

    const model = buildTraceWaterfallModel(trace);

    expect(model.durationMs).toBe(4000);
    expect(model.errorCount).toBe(1);
    expect(model.services).toEqual(['api', 'mysql']);
    expect(model.roots.map((span) => span.spanId)).toEqual(['root', 'orphan']);
    expect(model.byKey.get('root')?.children.map((span) => span.spanId)).toEqual(['early', 'late']);
    expect(model.byKey.get('early')?.peerService).toBe('mysql');
  });
});


it('shows and copies the original SkyWalking ID when opened by an OTLP ID', async () => {
  localStorage.setItem('ongrid-locale', 'en-US');
  const user = userEvent.setup();
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
  const originalID = '0123456789abcdef0123456789abcdef.1.17887680000000001';
  render(createElement(TraceWaterfall, {
    traceId: '0123456788abcdef001c9cf74a26f2ef',
    trace: {
      resourceSpans: [{
        resource: { attributes: [
          { key: 'service.name', value: { stringValue: 'orders' } },
          { key: 'sw8.trace_id', value: { stringValue: originalID } },
        ] },
        scopeSpans: [{ spans: [{ spanId: 'root', name: 'GET /orders', startTimeUnixNano: '1000000000', endTimeUnixNano: '1020000000' }] }],
      }],
    },
  }));
  expect(screen.getByTitle(originalID)).toBeInTheDocument();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Copy SkyWalking trace ID' })); });
  expect(copy).toHaveBeenCalledWith(originalID);
});

it.each([
  ['STATUS_CODE_OK', 'SUCCESS', false],
  ['STATUS_CODE_ERROR', 'request failed', true],
  [0, 'status detail', false],
] as const)('styles status messages by status code %s', (code, message, error) => {
  render(createElement(TraceWaterfall, { traceId: 'trace', trace: { resourceSpans: [{ scopeSpans: [{ spans: [{
    spanId: 'root', name: 'operation', startTimeUnixNano: '1000000000', endTimeUnixNano: '1020000000', status: { code, message },
  }] }] }] } }));
  const banner = screen.getByText(message).parentElement!;
  expect(banner.classList.contains('text-red-300')).toBe(error);
  expect(Boolean(banner.querySelector('svg'))).toBe(error);
});
