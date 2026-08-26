import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import SettingsOrgs from './Orgs';
import { server } from '@/test/msw-server';

describe('SettingsOrgs', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/orgs', () =>
        HttpResponse.json({
          items: [
            {
              id: 1,
              name: '默认组织',
              description: '',
              parent_id: null,
              created_at: '2026-01-01T00:00:00Z',
              updated_at: '2026-01-01T00:00:00Z',
            },
          ],
          total: 1,
        }),
      ),
      http.get('/api/v1/orgs/1/members', () =>
        HttpResponse.json({
          items: [
            {
              user_id: 2,
              email: 'ws@example.com',
              display_name: 'ws',
              role: 'member',
            },
          ],
          total: 1,
        }),
      ),
      // The real handler returns 204 with no response body.
      http.patch('/api/v1/orgs/1/members/2', () => new HttpResponse(null, { status: 204 })),
    );
  });

  it('keeps the member row rendered when role update returns 204', async () => {
    render(<SettingsOrgs />);

    const roleSelect = await screen.findByRole('combobox');
    await userEvent.selectOptions(roleSelect, 'viewer');

    expect(screen.getByDisplayValue('只读')).toBeInTheDocument();
    expect(screen.getByText('ws@example.com')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('ws@example.com');
  });
});
