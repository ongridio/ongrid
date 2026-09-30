import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { expect, it } from 'vitest';
import { http, HttpResponse } from 'msw';
import { server } from '@/test/msw-server';
import Alerts from './Alerts';

it('keeps alert filters and actions inside the shared page header', async () => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  server.use(
    http.get('/api/v1/alerts/incidents', () => HttpResponse.json({ items: [], total: 0 })),
    http.get('/api/v1/edges', () => HttpResponse.json({ items: [], total: 0 })),
  );
  render(<MemoryRouter><Alerts /></MemoryRouter>);
  const header = screen.getByRole('banner');
  expect(within(header).getByRole('heading', { name: '告警' })).toBeInTheDocument();
  expect(within(header).getByText('状态')).toBeInTheDocument();
  expect(within(header).getByText('级别')).toBeInTheDocument();
  expect(within(header).getByRole('link', { name: '规则配置' })).toHaveAttribute('href', '/alerts/rules');
  expect(within(header).getByRole('button', { name: '刷新' })).toBeInTheDocument();
});
