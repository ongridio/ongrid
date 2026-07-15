import { describe, expect, it } from 'vitest';
import { HttpResponse, http } from 'msw';

import { server } from '@/test/msw-server';

import { executeSkill } from './skills';

describe('executeSkill', () => {
  it('sends the selected Edge id for host-scoped skills', async () => {
    let requestBody: unknown;
    server.use(
      http.post('/api/v1/skills/host_probe_tcp/execute', async ({ request }) => {
        requestBody = await request.json();
        return HttpResponse.json({ result: { ok: true } });
      }),
    );

    await executeSkill('host_probe_tcp', 52, { target: '127.0.0.1:22' });

    expect(requestBody).toEqual({
      edge_id: '52',
      params: { target: '127.0.0.1:22' },
    });
  });

  it('omits the Edge id for manager-scoped skills', async () => {
    let requestBody: unknown;
    server.use(
      http.post('/api/v1/skills/query_devices/execute', async ({ request }) => {
        requestBody = await request.json();
        return HttpResponse.json({ result: { items: [] } });
      }),
    );

    await executeSkill('query_devices', null, { limit: 10 });

    expect(requestBody).toEqual({ params: { limit: 10 } });
  });
});
