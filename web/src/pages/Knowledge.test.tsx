// Knowledge 页面测试 — 覆盖「点击文档查看正文 + 编辑保存」链路：
//   1. 内置(vault)文档点击 → 只读查看器拉取并渲染 markdown 正文
//   2. 查看器「复制为组织文档」→ 预填编辑表单 → 保存为新建组织文档
//   3. 组织(manual)文档点击 → 直接进入编辑表单，PATCH 保存
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import KnowledgePage from './Knowledge';
import { server } from '@/test/msw-server';

// client.ts / api/knowledge.ts 只需要 token 取值与 401 兜底；测试里全部打桩。
vi.mock('@/store/auth', () => ({
  useAuth: Object.assign(
    <T,>(selector: (s: { role: string }) => T): T => selector({ role: 'admin' }),
    { getState: () => ({ logout: () => {} }) },
  ),
  getToken: () => null,
  getRefreshToken: () => null,
}));

const vaultDoc = {
  id: '101',
  source_type: 'vault',
  title: 'Host Metric Alerts Runbook',
  url: 'alerts/host-metrics.md',
  path: 'alerts',
  created_at: '2026-06-01T00:00:00Z',
  updated_at: '2026-06-01T00:00:00Z',
};

const vaultContent = '# Runbook 正文\n\n第一步：查看告警上下文';

const manualDoc = {
  id: '202',
  source_type: 'manual',
  title: '组织 SOP',
  content: '原有正文',
  path: '',
  created_at: '2026-06-01T00:00:00Z',
  updated_at: '2026-06-01T00:00:00Z',
};

const listURL = '/api/v1/knowledge/docs';

function useBaseHandlers() {
  server.use(
    http.get(listURL, () =>
      HttpResponse.json({ items: [vaultDoc, manualDoc], total: 2 }),
    ),
    http.get(`${listURL}/101`, () =>
      HttpResponse.json({ ...vaultDoc, content: vaultContent }),
    ),
    http.get(`${listURL}/202`, () =>
      HttpResponse.json({ ...manualDoc }),
    ),
  );
}

describe('KnowledgePage', () => {
  beforeEach(() => {
    // 固定语言，断言中文文案不受运行机器时区/浏览器语言影响。
    localStorage.setItem('ongrid-locale', 'zh-CN');
    useBaseHandlers();
  });

  // 右侧文档列表默认在「组织」作用域；先点侧边栏「内置知识库」切过去。
  async function openBuiltinScope() {
    // ^锚定避免命中「从云端同步内置知识库…」的同步按钮
    await userEvent.click(await screen.findByRole('button', { name: /^内置知识库/ }));
  }

  it('点击内置文档打开只读查看器并渲染正文', async () => {
    render(<KnowledgePage />);
    await openBuiltinScope();
    const card = await screen.findByText('Host Metric Alerts Runbook');
    await userEvent.click(card);

    // 查看器：拉取 GET /knowledge/docs/:id 并渲染 markdown 正文
    expect(await screen.findByText('第一步：查看告警上下文')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /复制为组织文档/ })).toBeInTheDocument();
    // 只读：没有「保存」按钮
    expect(screen.queryByRole('button', { name: '保存' })).not.toBeInTheDocument();
  });

  it('复制为组织文档：预填表单并 POST 新建', async () => {
    let createdBody: Record<string, unknown> | null = null;
    server.use(
      http.post(listURL, async ({ request }) => {
        createdBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          ...manualDoc,
          id: '303',
          title: createdBody.title,
        });
      }),
    );

    render(<KnowledgePage />);
    await openBuiltinScope();
    await userEvent.click(await screen.findByText('Host Metric Alerts Runbook'));
    await screen.findByText('第一步：查看告警上下文');
    await userEvent.click(screen.getByRole('button', { name: /复制为组织文档/ }));

    // 表单预填了原标题与正文
    const titleInput = await screen.findByDisplayValue('Host Metric Alerts Runbook');
    expect(titleInput).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(createdBody).not.toBeNull());
    expect(createdBody).toMatchObject({
      title: 'Host Metric Alerts Runbook',
      content: vaultContent,
      path: 'alerts',
    });
    // 来源 url（builtin/repo 文件路径）不带入副本
    expect(createdBody).not.toHaveProperty('url');
  });

  it('组织文档点击进入编辑表单并 PATCH 保存', async () => {
    let patchedBody: Record<string, unknown> | null = null;
    server.use(
      http.patch(`${listURL}/202`, async ({ request }) => {
        patchedBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ ...manualDoc, ...patchedBody });
      }),
    );

    render(<KnowledgePage />);
    await userEvent.click(await screen.findByText('组织 SOP'));

    const body = await screen.findByDisplayValue('原有正文');
    await userEvent.clear(body);
    await userEvent.type(body, '更新后的正文');
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(patchedBody).not.toBeNull());
    expect(patchedBody).toMatchObject({ title: '组织 SOP', content: '更新后的正文' });
  });
});
