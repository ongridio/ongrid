import { http, HttpResponse } from 'msw';
import { expect, it } from 'vitest';

import { server } from '@/test/msw-server';

import {
  createModelGroup,
  createSavedSearch,
  deleteModelGroup,
  deleteSavedSearch,
  listModelGroups,
  listSavedSearches,
  listTreeLevels,
  reorderModelGroups,
  setTreeLevels,
  updateModelGroup,
  updateSavedSearch,
} from './preferences';

it('lists model groups from the shared list envelope', async () => {
  const group = { id: 3, name: '网络设备', ci_types: [], is_other: false };
  server.use(
    http.get('/api/v1/cmdb/preferences/model-groups', () =>
      HttpResponse.json({ items: [group], total: 1 }),
    ),
  );
  expect(await listModelGroups()).toEqual([group]);
});

it('creates, renames, reorders and deletes model groups', async () => {
  let created = false;
  let updated = false;
  let reordered = false;
  let deleted = false;
  server.use(
    http.post('/api/v1/cmdb/model-groups', async ({ request }) => {
      expect(await request.json()).toEqual({ name: '网络设备' });
      created = true;
      return HttpResponse.json({ id: 3, name: '网络设备', ci_types: [] });
    }),
    http.put('/api/v1/cmdb/model-groups/3', async ({ request }) => {
      expect(await request.json()).toEqual({ name: '网络', type_ids: [1, 2] });
      updated = true;
      return HttpResponse.json({ id: 3, name: '网络', ci_types: [] });
    }),
    http.put('/api/v1/cmdb/model-groups/order', async ({ request }) => {
      expect(await request.json()).toEqual({ group_ids: [3, 1] });
      reordered = true;
      return new HttpResponse(null, { status: 204 });
    }),
    http.delete('/api/v1/cmdb/model-groups/3', () => {
      deleted = true;
      return new HttpResponse(null, { status: 204 });
    }),
  );

  expect(await createModelGroup('网络设备')).toMatchObject({ id: 3, name: '网络设备' });
  expect(await updateModelGroup(3, { name: '网络', type_ids: [1, 2] })).toMatchObject({ name: '网络' });
  await reorderModelGroups([3, 1]);
  await deleteModelGroup(3);
  expect({ created, updated, reordered, deleted }).toEqual({
    created: true,
    updated: true,
    reordered: true,
    deleted: true,
  });
});

it('reads and writes tree levels as a bare array', async () => {
  const view = {
    id: 7,
    type_id: 4,
    name: 'host',
    alias: '主机',
    levels: [{ id: 1, name: 'idc', alias: '机房', value_type: '2' }],
  };
  let saved: unknown;
  server.use(
    http.get('/api/v1/cmdb/preferences/tree-levels', () => HttpResponse.json([view])),
    http.put('/api/v1/cmdb/preferences/ci-types/host/tree-levels', async ({ request }) => {
      saved = await request.json();
      return HttpResponse.json(view);
    }),
  );

  expect(await listTreeLevels()).toEqual([view]);
  expect(await setTreeLevels('host', ['idc', 'rack'])).toEqual(view);
  expect(saved).toEqual({ levels: ['idc', 'rack'] });
});

it('lists and mutates saved searches', async () => {
  const search = { id: 9, name: '在线主机', type_id: 4, option: { q: 'status:在线' } };
  let created: unknown;
  let updated: unknown;
  let deleted = false;
  server.use(
    http.get('/api/v1/cmdb/preferences/saved-searches', ({ request }) => {
      expect(new URL(request.url).searchParams.get('type_id')).toBe('4');
      return HttpResponse.json({ items: [search], total: 1 });
    }),
    http.post('/api/v1/cmdb/preferences/saved-searches', async ({ request }) => {
      created = await request.json();
      return HttpResponse.json(search);
    }),
    http.put('/api/v1/cmdb/preferences/saved-searches/9', async ({ request }) => {
      updated = await request.json();
      return HttpResponse.json(search);
    }),
    http.delete('/api/v1/cmdb/preferences/saved-searches/9', () => {
      deleted = true;
      return new HttpResponse(null, { status: 204 });
    }),
  );

  expect(await listSavedSearches({ type_id: 4 })).toEqual([search]);
  expect(await createSavedSearch({ name: '在线主机', type_id: 4, option: { q: 'status:在线' } })).toEqual(search);
  expect(await updateSavedSearch(9, { name: '在线主机', option: { q: 'status:在线' } })).toEqual(search);
  await deleteSavedSearch(9);
  expect(created).toEqual({ name: '在线主机', type_id: 4, option: { q: 'status:在线' } });
  expect(updated).toEqual({ name: '在线主机', option: { q: 'status:在线' } });
  expect(deleted).toBe(true);
});
