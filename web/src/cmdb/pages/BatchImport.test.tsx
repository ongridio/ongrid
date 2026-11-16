import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { selectOption } from '@/test/select-option';
import { useAuth } from '@/store/auth';
import BatchImportPage from './BatchImport';
import { ToastProvider } from '../components/Toast';

const host = {
  id: 1,
  name: 'host',
  alias: '主机',
  enabled: true,
  is_attached: true,
  icon: '',
  order: 0,
  unique_key: 'hostname',
  parent_ids: [],
};

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <BatchImportPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the batch import page', () => {
  it('parses a CSV, previews it, and commits the same sheet', async () => {
    let previewBody: unknown;
    let commitBody: unknown;
    server.use(
      http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [host], total: 1 })),
      http.post('/api/v1/cmdb/import/preview', async ({ request }) => {
        previewBody = await request.json();
        return HttpResponse.json({
          total: 1,
          valid: 1,
          invalid: 0,
          creates: 1,
          updates: 0,
          columns: [
            { index: 0, header: 'hostname', attr_id: 10, attr: 'hostname', alias: '主机名', matched: true, identifier: true },
            { index: 1, header: 'cpu', attr_id: 11, attr: 'cpu', alias: 'CPU', matched: true },
          ],
          rows: [{ row: 1, sheet_row: 2, ok: true, action: 'create', unique: 'web-01' }],
        });
      }),
      http.post('/api/v1/cmdb/import/commit', async ({ request }) => {
        commitBody = await request.json();
        return HttpResponse.json({
          written: 1,
          creates: 1,
          updates: 0,
          rows: [{ row: 1, sheet_row: 2, ok: true, action: 'create', unique: 'web-01', ci_id: 42 }],
        });
      }),
    );
    renderPage();

    await selectOption(await screen.findByLabelText('模型'), '主机');
    const file = new File(['hostname,cpu\nweb-01,8\n'], 'hosts.csv', { type: 'text/csv' });
    await userEvent.upload(screen.getByLabelText('上传表格'), file);

    await waitFor(() =>
      expect(previewBody).toEqual({
        ci_type: '1',
        headers: ['hostname', 'cpu'],
        rows: [['web-01', '8']],
        exist_policy: 'reject',
      }),
    );

    await userEvent.click(await screen.findByRole('button', { name: '导入' }));
    await waitFor(() => expect(commitBody).toEqual(previewBody));
    expect((await screen.findAllByText(/已导入 1 行/)).length).toBeGreaterThan(0);
  });
});
