import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { InstanceTree } from './InstanceTree';
import { ToastProvider } from './Toast';

const host = { id: 1, name: 'host', alias: '主机', enabled: true, is_attached: true, icon: '', order: 0, unique_key: 'hostname', parent_ids: [] };

function renderTree(maxLevel: number) {
  return render(
    <ToastProvider>
      <InstanceTree rootModel={host} maxLevel={maxLevel} />
    </ToastProvider>,
  );
}

function handlers() {
  server.use(
    http.get('/api/v1/cmdb/ci-types/1/instances', () =>
      HttpResponse.json({
        type_id: 1,
        items: [{ ci_id: 11, type_id: 1, values: { hostname: 'web-01' } }],
        total: 1,
        numfound: 1,
        page: 1,
      }),
    ),
    http.get('/api/v1/cmdb/ci-relations/search', ({ request }) => {
      const url = new URL(request.url);
      const root = url.searchParams.get('root_id');
      if (root === '11') {
        return HttpResponse.json({
          numfound: 2,
          total: 2,
          page: 1,
          result: [
            { _id: 21, _type: 2, ci_id: 21, ci_type: 'disk', unique: 'name', name: 'sda' },
            { _id: 22, _type: 2, ci_id: 22, ci_type: 'disk', unique: 'name', name: 'sdb' },
          ],
        });
      }
      return HttpResponse.json({ numfound: 0, total: 0, page: 1, result: [] });
    }),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

describe('the instance tree', () => {
  it('starts from the model’s instances and expands one hop at a time', async () => {
    const user = userEvent.setup();
    handlers();
    const walked: string[] = [];
    server.use(
      http.get('/api/v1/cmdb/ci-relations/search', ({ request }) => {
        const url = new URL(request.url);
        walked.push(`${url.searchParams.get('root_id')}|${url.searchParams.get('level')}`);
        return HttpResponse.json({
          numfound: 2,
          total: 2,
          page: 1,
          result: [
            { _id: 21, _type: 2, ci_id: 21, ci_type: 'disk', unique: 'name', name: 'sda' },
            { _id: 22, _type: 2, ci_id: 22, ci_type: 'disk', unique: 'name', name: 'sdb' },
          ],
        });
      }),
    );
    renderTree(3);

    expect(await screen.findByText('web-01')).toBeInTheDocument();
    // NOTHING IS WALKED UNTIL THE OPERATOR OPENS SOMETHING: a tree that loads the
    // whole estate on mount is a tree nobody can afford.
    expect(walked).toHaveLength(0);

    await user.click(screen.getByRole('treeitem'));
    await user.keyboard('{ArrowRight}');

    await waitFor(() => expect(walked).toHaveLength(1));
    // LEVEL 1 IS "ONE HOP BELOW", so the children arrive with their values and a
    // label costs no second request.
    expect(walked[0]).toBe('11|1');
    expect(await screen.findByText('sda')).toBeInTheDocument();
    expect(screen.getByText('sdb')).toBeInTheDocument();
  });

  it('does not offer an expansion past the level cap', async () => {
    const user = userEvent.setup();
    handlers();
    let walked = 0;
    server.use(
      http.get('/api/v1/cmdb/ci-relations/search', () => {
        walked += 1;
        return HttpResponse.json({ numfound: 1, total: 1, page: 1, result: [{ _id: 21, _type: 2, ci_id: 21, ci_type: 'disk', unique: 'name', name: 'sda' }] });
      }),
    );
    // ONE LEVEL: the roots are the only level, so a root is a leaf as far as the
    // page is concerned.
    renderTree(1);

    const root = await screen.findByRole('treeitem');
    expect(root).not.toHaveAttribute('aria-expanded');
    await user.click(root);
    await user.keyboard('{ArrowRight}');
    // THE CAP IS ENFORCED BY NOT OFFERING THE EXPANSION, because a click the
    // server will not answer is worse than a missing chevron.
    expect(walked).toBe(0);
  });

  it('says the model has no instances rather than drawing an empty box', async () => {
    server.use(
      http.get('/api/v1/cmdb/ci-types/1/instances', () =>
        HttpResponse.json({ type_id: 1, items: [], total: 0, numfound: 0, page: 1 }),
      ),
    );
    renderTree(3);

    expect(await screen.findByText('这个模型还没有实例')).toBeInTheDocument();
  });
});
