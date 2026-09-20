import { http, HttpResponse } from 'msw';
import { expect, it } from 'vitest';
import { server } from '@/test/msw-server';
import { getDeviceEnvironment, setDeviceEnvironment } from './devices';

it('unwraps the shared device environment response for reads and writes', async () => {
  const value = { environment: '', effective_environment: 'production', inherited_environment: 'production', cluster_name: 'hosts', source: 'cluster' };
  server.use(
    http.get('/api/v1/devices/650/environment', () => HttpResponse.json({ code: 0, message: '', data: value })),
    http.put('/api/v1/devices/650/environment', async ({ request }) => {
      expect(await request.json()).toEqual({ environment: '' });
      return HttpResponse.json({ code: 0, message: '', data: value });
    }),
  );
  expect(await getDeviceEnvironment(650)).toEqual(value);
  expect(await setDeviceEnvironment(650, '')).toEqual(value);
});
