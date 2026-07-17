// Skills 页面测试 — 覆盖「点击行打开只读详情弹窗」链路：
// 完整描述（列表里被 line-clamp 截断）、参数表、不可编辑提示。
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import SkillsPage from './Skills';
import { server } from '@/test/msw-server';

vi.mock('@/store/auth', () => ({
  useAuth: Object.assign(
    <T,>(selector: (s: { role: string }) => T): T => selector({ role: 'admin' }),
    { getState: () => ({ logout: () => {} }) },
  ),
  getToken: () => null,
  getRefreshToken: () => null,
}));

const longDesc =
  'Analyze database health and performance from ongrid database metrics sources. ' +
  'Use this as the first tool for any MySQL question that exporter metrics can cover.';

const skills = [
  {
    key: 'analyze_database_status',
    name: 'analyze_database_status',
    description: longDesc,
    class: 'safe',
    scope: 'manager',
    category: 'agent',
    params: [
      { name: 'device_id', type: 'int', required: true, desc: '目标设备 id' },
      { name: 'window', type: 'duration', default: '15m', desc: '分析时间窗' },
    ],
    result_preview: '{summary, findings[]}',
  },
];

describe('SkillsPage', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/skills', () =>
        HttpResponse.json({ items: skills, total: skills.length }),
      ),
      http.get('/api/v1/flow-tools', () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
    );
  });

  it('点击技能行打开只读详情弹窗', async () => {
    render(
      <MemoryRouter>
        <SkillsPage />
      </MemoryRouter>,
    );
    // 名称与 key 同文案（列表里各渲染一次），点第一个（名称）即可
    await userEvent.click((await screen.findAllByText('analyze_database_status'))[0]);

    // 弹窗：完整描述不再截断（列表行 line-clamp + 弹窗正文各一份），
    // 参数表逐行展示
    expect(await screen.findAllByText(longDesc)).toHaveLength(2);
    expect(screen.getByText('device_id')).toBeInTheDocument();
    expect(screen.getByText('目标设备 id')).toBeInTheDocument();
    expect(screen.getByText('15m')).toBeInTheDocument();
    expect(screen.getByText('{summary, findings[]}')).toBeInTheDocument();
    // 只读定位：明示技能由代码定义、没有编辑入口
    expect(screen.getByText(/不可在线编辑/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /保存|编辑/ })).not.toBeInTheDocument();
  });
});
