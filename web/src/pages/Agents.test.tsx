// Agents 页面测试 — 覆盖「点击助理卡片查看定义详情」链路，并验证
// 按 source 区分的编辑路径（user 直接编辑 / 内置预置 fork 成自定义）。
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import AgentsPage from './Agents';
import { server } from '@/test/msw-server';

vi.mock('@/store/auth', () => ({
  useAuth: Object.assign(
    <T,>(selector: (s: { role: string }) => T): T => selector({ role: 'admin' }),
    { getState: () => ({ logout: () => {} }) },
  ),
  getToken: () => null,
  getRefreshToken: () => null,
}));

const diskAgent = {
  name: 'specialist-sre',
  description: 'SRE 专家：黄金四信号 / SLO / 错误预算。',
  when_to_use: '当任务围绕系统是否健康时派给我。',
  system_prompt: '# SRE 专家\n你是 SRE 专家，优先看黄金四信号。',
  tools: ['query_promql', 'query_incidents'],
  permission_mode: 'read-only',
  source: 'disk',
};

const userAgent = {
  name: 'my_db_helper',
  description: '我的数据库助手。',
  system_prompt: '你是数据库专家。',
  tools: ['analyze_database_status'],
  source: 'user',
};

const listURL = '/api/v1/skills';
const mcpListURL = '/api/v1/mcp/servers';

describe('AgentsPage', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/agents', () =>
        HttpResponse.json({ items: [diskAgent, userAgent], total: 2 }),
      ),
      // AgentEditor 打开时拉工具列表
      http.get(listURL, () => HttpResponse.json({ items: [], total: 0 })),
      http.get(mcpListURL, () => HttpResponse.json({ items: [], total: 0 })),
    );
  });

  it('点击预置助理卡片打开只读详情，编辑入口是「复制为自定义助理」', async () => {
    render(
      <MemoryRouter>
        <AgentsPage />
      </MemoryRouter>,
    );
    // 淡色卡片操作不能被实心主按钮背景覆盖，否则浅色主题下紫字落在紫底上。
    const useButton = (await screen.findAllByRole('button', { name: '使用此助理' }))[0];
    expect(useButton).toHaveAttribute('data-variant', 'plain');
    expect(useButton).toHaveClass('bg-indigo-500/10', 'text-indigo-200');
    // 卡片标题用短名；点击卡片主体打开详情
    await userEvent.click(await screen.findByText('SRE 专家'));

    const dialog = await screen.findByRole('dialog');
    // system prompt 原文完整展示
    expect(within(dialog).getByText(/你是 SRE 专家，优先看黄金四信号/)).toBeInTheDocument();
    // 工具清单
    expect(within(dialog).getByText('query_promql')).toBeInTheDocument();
    // disk source 不可直接编辑 → 提供 fork 入口，没有「编辑」按钮
    expect(within(dialog).getByRole('button', { name: /复制为自定义助理/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: '编辑' })).not.toBeInTheDocument();
  });

  it('点击自定义助理卡片，详情里有「编辑」入口', async () => {
    render(
      <MemoryRouter>
        <AgentsPage />
      </MemoryRouter>,
    );
    // user 助理无短名，name 同时出现在卡片标题和 mono 行；点标题
    await userEvent.click((await screen.findAllByText('my_db_helper'))[0]);

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/你是数据库专家/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: /编辑/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: /复制为自定义助理/ })).not.toBeInTheDocument();
  });

  it('新建助理可选择已测试且启用的 MCP 工具', async () => {
    server.use(
      http.get(listURL, () => HttpResponse.json({ items: [{ key: 'query_promql' }], total: 1 })),
      http.get(mcpListURL, () => HttpResponse.json({
        items: [{
          ID: 1,
          Name: 'demo-operations',
          Enabled: true,
          ToolsCacheJSON: JSON.stringify([{ name: 'get_service_health', description: 'Read health.' }]),
        }],
        total: 1,
      })),
    );

    render(
      <MemoryRouter>
        <AgentsPage />
      </MemoryRouter>,
    );

    await userEvent.click(await screen.findByRole('button', { name: /新建助理/ }));
    expect(await screen.findByText('mcp__demo_operations__get_service_health')).toBeInTheDocument();
  });
});
