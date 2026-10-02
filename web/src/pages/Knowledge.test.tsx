// Knowledge 页面测试 — 覆盖「点击文档查看正文 + 编辑保存」链路：
//   1. 内置(vault)文档点击 → 只读查看器拉取并渲染 markdown 正文
//   2. 查看器「复制为组织文档」→ 预填编辑表单 → 保存为新建组织文档
//   3. 组织(manual)文档点击 → 直接进入编辑表单，PATCH 保存
import { render, screen, waitFor, within } from '@testing-library/react';
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

const vaultContent =
  '# Runbook 正文\n\n第一步：查看告警上下文\n\n' +
  '| Severity | Acknowledge |\n|---|---|\n| SEV1 | < 5 min |\n';

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
    http.get('/api/v1/knowledge/llm-wiki/tree', () =>
      HttpResponse.json({ code: 'ok', message: 'ok', data: { items: [], total: 0, document_count: 0 } }),
    ),
    http.get('/api/v1/knowledge/llm-wiki/jobs', () =>
      HttpResponse.json({ code: 'ok', message: 'ok', data: { items: [], total: 0 } }),
    ),
    http.get('/api/v1/knowledge/llm-wiki/sources', () =>
      HttpResponse.json({ code: 'ok', message: 'ok', data: { items: [], total: 0 } }),
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
    // GFM 表格渲染为真 <table>（remark-gfm），而非一行竖线文本
    expect(screen.getByRole('table')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Severity' })).toBeInTheDocument();
    expect(screen.getByRole('cell', { name: '< 5 min' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /复制为组织文档/ })).toBeInTheDocument();
    // 只读：没有「保存」按钮
    expect(screen.queryByRole('button', { name: '保存' })).not.toBeInTheDocument();
  });

  it('frontmatter 不渲染为标题，元信息以弱化 key/value 展示', async () => {
    server.use(
      http.get(`${listURL}/101`, () =>
        HttpResponse.json({
          ...vaultDoc,
          content: '---\ntitle: Linux Memory Model\ntags: [linux, memory]\n---\n\n正文内容',
        }),
      ),
    );
    render(<KnowledgePage />);
    await openBuiltinScope();
    await userEvent.click(await screen.findByText('Host Metric Alerts Runbook'));

    expect(await screen.findByText('正文内容')).toBeInTheDocument();
    // 元信息行存在（dt/dd），tags 拆成单个 chip
    expect(screen.getByText('Linux Memory Model')).toBeInTheDocument();
    expect(screen.getByText('linux')).toBeInTheDocument();
    expect(screen.getByText('memory')).toBeInTheDocument();
    // 不再被 setext 语法渲染成 <h2> 粗体标题
    expect(
      screen.queryByRole('heading', { name: /Linux Memory Model/ }),
    ).not.toBeInTheDocument();
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
      // 提交时 trim：fixture 末尾的换行不入库
      content: vaultContent.trim(),
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

  it('LLM Wiki 只有总根目录无文件时显示空状态，且不展示本地路径', async () => {
    render(<KnowledgePage />);

    expect(screen.getByRole('button', { name: 'LLM Wiki' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '生成页面' })).toBeInTheDocument();
    const wikiButton = await screen.findByRole('button', { name: /^LLM Wiki/ });
    expect(wikiButton).not.toHaveClass('bg-zinc-800');
    await userEvent.click(wikiButton);
    await waitFor(() => expect(wikiButton).toHaveClass('bg-zinc-800'));

    expect(await screen.findByText('LLM Wiki 还没有内容')).toBeInTheDocument();
    expect(screen.queryByText('/var/lib/ongrid/llm-wiki/')).not.toBeInTheDocument();
  });

  it('LLM Wiki 通过目录查看生成页，详情展示知识节点和来源', async () => {
    let rawPreviewRequested = false;
    server.use(
      http.get('/api/v1/knowledge/llm-wiki/tree', ({ request }) => {
        const layer = new URL(request.url).searchParams.get('layer');
        const items = layer === 'raw'
          ? [
              { id: 'raw-docs', parent_id: '', layer: 'raw', kind: 'folder', name: 'docs', relative_path: 'docs', has_children: true, child_count: 2, document_count: 2 },
              { id: 'raw-platform', parent_id: 'raw-docs', layer: 'raw', kind: 'folder', name: 'platform', relative_path: 'docs/platform', has_children: true, child_count: 1, document_count: 1 },
              { id: 'raw-xx', parent_id: 'raw-docs', layer: 'raw', kind: 'file', name: '知识库.pdf', relative_path: 'docs/知识库.pdf', source_id: '7', has_children: false, child_count: 0, document_count: 1 },
              { id: 'raw-knowledge', parent_id: 'raw-platform', layer: 'raw', kind: 'file', name: 'knowledge.md', relative_path: 'docs/platform/knowledge.md', source_id: '8', has_children: false, child_count: 0, document_count: 1 },
            ]
          : [
              { id: 'wiki-rag', parent_id: '', layer: 'wiki', kind: 'file', name: 'RAG', relative_path: 'rag.md', page_type: 'generated', has_children: false, child_count: 0, document_count: 1 },
              { id: 'wiki-deploy', parent_id: '', layer: 'wiki', kind: 'file', name: 'Deployment', relative_path: 'deployment.md', page_type: 'generated', has_children: false, child_count: 0, document_count: 1 },
              { id: 'wiki-guide', parent_id: '', layer: 'wiki', kind: 'file', name: 'Guide', relative_path: 'guide.md', page_type: 'generated', has_children: false, child_count: 0, document_count: 1 },
            ];
        return HttpResponse.json({ code: 'ok', message: 'ok', data: { items, total: items.length, document_count: layer === 'wiki' ? 3 : items.length } });
      }),
      http.get('/api/v1/knowledge/llm-wiki/nodes/wiki-rag', () =>
        HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: {
            id: 'wiki-rag', layer: 'wiki', kind: 'file', name: 'RAG', page_type: 'generated',
            relative_path: 'rag.md', content: '# RAG 正文\n\n可阅读的 Wiki 文件。', updated_at: '2026-09-08T10:20:30Z',
            metadata: {
              page_type: 'generated',
              source_files: [{ path: 'docs/platform/knowledge.md', node_id: 'raw-knowledge', version_id: '3' }],
            },
          },
        }),
      ),
      http.get('/api/v1/knowledge/llm-wiki/nodes/raw-xx', () =>
        HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: {
            id: 'raw-xx', layer: 'raw', kind: 'file', name: '知识库.pdf',
            relative_path: '知识库.pdf', content: '# Raw 正文', updated_at: '2026-09-08T09:10:20Z',
            metadata: {
              wiki_files: [{ title: 'RAG', path: 'rag.md', node_id: 'wiki-rag', page_id: 'rag' }],
            },
          },
        }),
      ),
      http.get('/api/v1/knowledge/llm-wiki/nodes/raw-xx/preview', () => {
        rawPreviewRequested = true;
        return new HttpResponse('%PDF-1.4 preview', { headers: { 'Content-Type': 'application/pdf' } });
      }),
      http.get('/api/v1/knowledge/llm-wiki/nodes/raw-knowledge', () =>
        HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: {
            id: 'raw-knowledge', layer: 'raw', kind: 'file', name: 'knowledge.md',
            relative_path: 'docs/platform/knowledge.md', content: '# Knowledge Raw 正文', updated_at: '2026-09-08T08:10:20Z',
            metadata: {
              wiki_files: [{ title: 'RAG', path: 'rag.md', node_id: 'wiki-rag', page_id: 'rag' }],
            },
          },
        }),
      ),
    );

    render(<KnowledgePage />);

    expect(await screen.findByText('LLM Wiki')).toBeInTheDocument();
    expect(document.querySelector('input[accept=".md,.markdown,.txt,.text,.pdf,.docx"]')).toBeInTheDocument();
    expect(await screen.findByText('共 5 条 · 组织 1 · 内置 1 · LLM Wiki 3')).toBeInTheDocument();
    expect(screen.queryByText('/var/lib/ongrid/llm-wiki/')).not.toBeInTheDocument();
    expect(screen.getByText('原始来源').parentElement?.parentElement).toHaveTextContent('原始来源2');
    expect(screen.getByText('生成页面').parentElement?.parentElement).toHaveTextContent('生成页面3');
    expect(screen.queryByRole('button', { name: 'schema.md' })).not.toBeInTheDocument();
    const wikiRoot = screen.getByRole('button', { name: /^LLM Wiki/ });
    await userEvent.click(wikiRoot);
    await waitFor(() => expect(wikiRoot).toHaveAttribute('aria-current', 'page'));
    expect((await screen.findAllByText('知识库.pdf')).length).toBeGreaterThanOrEqual(1);
    expect(await screen.findByText('RAG')).toBeInTheDocument();
    expect(await screen.findByText('Guide')).toBeInTheDocument();

    const rawRoot = screen.getByRole('button', { name: /^原始来源/ });
    await userEvent.click(rawRoot);
    await waitFor(() => expect(wikiRoot).not.toHaveAttribute('aria-current'));
    await waitFor(() => expect(rawRoot).toHaveClass('bg-zinc-800'));
    expect((await screen.findAllByText('知识库.pdf')).length).toBeGreaterThanOrEqual(1);
    await userEvent.click(screen.getAllByText('知识库.pdf')[0]);
    await waitFor(() => expect(rawPreviewRequested).toBe(true));
    expect(screen.getByText('关联 (1)')).toBeInTheDocument();
    expect(screen.getAllByText('rag.md').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText(/更新时间：/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'rag.md' }));
    expect(await screen.findByText('可阅读的 Wiki 文件。')).toBeInTheDocument();
    expect(screen.getByText('来源 (1)')).toBeInTheDocument();
    await userEvent.click(screen.getByText('关闭', { selector: 'button' }));

    await userEvent.click(screen.getByText('生成页面'));
    await userEvent.click(screen.getByText('RAG'));
    expect(await screen.findByText('可阅读的 Wiki 文件。')).toBeInTheDocument();
    expect(screen.getByText('来源 (1)')).toBeInTheDocument();
    expect(screen.getAllByText('docs/platform/knowledge.md').length).toBeGreaterThanOrEqual(1);
    await userEvent.click(screen.getByRole('button', { name: 'docs/platform/knowledge.md' }));
    expect(await screen.findByText('Knowledge Raw 正文')).toBeInTheDocument();
    expect(screen.getByText('关联 (1)')).toBeInTheDocument();
    expect(screen.queryByText('v3')).not.toBeInTheDocument();
    expect(screen.queryByText('wiki-rag', { exact: true })).not.toBeInTheDocument();
    expect(screen.queryByText('generated', { exact: true })).not.toBeInTheDocument();
    expect(screen.queryByText('und', { exact: true })).not.toBeInTheDocument();
    expect(within(screen.getByRole('dialog')).queryByText('3', { exact: true })).not.toBeInTheDocument();
    expect(screen.queryByText('title', { exact: true })).not.toBeInTheDocument();
    expect(screen.queryByText('source_file', { exact: true })).not.toBeInTheDocument();
    expect(screen.getByText(/更新时间：/)).toBeInTheDocument();
    await userEvent.click(screen.getByText('关闭', { selector: 'button' }));
    expect(await screen.findByText('RAG')).toBeInTheDocument();
    await userEvent.click(await screen.findByText('docs'));
    expect(await screen.findByText('知识库.pdf')).toBeInTheDocument();
    expect(await screen.findByText('knowledge.md')).toBeInTheDocument();
  });

  it('LLM Wiki 顶部支持创建全量编译任务', async () => {
    let compileBody: Record<string, unknown> | null = null;
    server.use(
      http.post('/api/v1/knowledge/llm-wiki/compile', async ({ request }) => {
        compileBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: { id: '88', status: 'pending', stage: 'queued', created_at: '2026-09-08T10:00:00Z', updated_at: '2026-09-08T10:00:00Z' },
        }, { status: 202 });
      }),
    );

    render(<KnowledgePage />);
    await userEvent.click(await screen.findByRole('button', { name: '编译全部' }));

    await waitFor(() => expect(compileBody).toEqual({ force: false }));
  });

  it('展示失败的编译任务并支持重试', async () => {
    let retried = false;
    server.use(
      http.get('/api/v1/knowledge/llm-wiki/jobs', () =>
        HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: {
            items: [{
              id: '42', status: retried ? 'pending' : 'failed', stage: retried ? 'queued' : 'compile',
              error: retried ? '' : '模型调用失败',
              created_at: '2026-09-08T10:00:00Z', updated_at: '2026-09-08T10:01:00Z',
            }],
            total: 1,
          },
        }),
      ),
      http.post('/api/v1/knowledge/llm-wiki/jobs/42/retry', () => {
        retried = true;
        return HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: { id: '42', status: 'pending', stage: 'queued', created_at: '2026-09-08T10:00:00Z', updated_at: '2026-09-08T10:02:00Z' },
        });
      }),
      http.get('/api/v1/knowledge/llm-wiki/sources', () =>
        HttpResponse.json({ code: 'ok', message: 'ok', data: { items: [{ id: '7', raw_path: 'docs/guide.md' }], total: 1 } }),
      ),
    );

    render(<KnowledgePage />);
    await userEvent.click(await screen.findByRole('button', { name: /^LLM Wiki/ }));
    await userEvent.click(screen.getByRole('button', { name: '编译任务' }));

    expect(await screen.findByText('编译任务')).toBeInTheDocument();
    expect(await screen.findByText('模型调用失败')).toBeInTheDocument();
    expect(screen.getByText('全部来源')).toBeInTheDocument();
    const retryButton = screen.getByRole('button', { name: '重试编译' });
    expect(retryButton.querySelector('.lucide-rotate-ccw')).toBeInTheDocument();
    expect(retryButton.querySelector('.lucide-refresh-cw')).not.toBeInTheDocument();
    await userEvent.click(retryButton);

    await waitFor(() => expect(retried).toBe(true));
    expect(await screen.findByText('排队中')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '重试编译' })).not.toBeInTheDocument();
  });

  it('只展示进行中和失败任务，进行中任务支持取消', async () => {
    let cancelled = false;
    server.use(
      http.get('/api/v1/knowledge/llm-wiki/jobs', () =>
        HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: {
            items: cancelled ? [] : [
              { id: '11', status: 'running', stage: 'summarize', created_at: '2026-09-08T10:00:00Z', updated_at: '2026-09-08T10:01:00Z' },
              { id: '12', status: 'succeeded', stage: 'done', created_at: '2026-09-08T09:00:00Z', updated_at: '2026-09-08T09:01:00Z' },
              { id: '13', status: 'failed', stage: 'compile', error: '编译失败', created_at: '2026-09-08T08:00:00Z', updated_at: '2026-09-08T08:01:00Z' },
            ],
            total: cancelled ? 0 : 3,
          },
        }),
      ),
      http.post('/api/v1/knowledge/llm-wiki/jobs/11/cancel', () => {
        cancelled = true;
        return HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: { id: '11', status: 'cancelled', stage: 'cancelled', created_at: '2026-09-08T10:00:00Z', updated_at: '2026-09-08T10:02:00Z' },
        });
      }),
    );

    render(<KnowledgePage />);
    await userEvent.click(await screen.findByRole('button', { name: /^LLM Wiki/ }));
    await userEvent.click(screen.getByRole('button', { name: '编译任务' }));

    expect(await screen.findByText('编译中')).toBeInTheDocument();
    expect(screen.getByText('编译失败')).toBeInTheDocument();
    expect(screen.queryByText('成功')).not.toBeInTheDocument();
    await userEvent.click(screen.getAllByRole('button', { name: '取消编译' })[0]);

    await waitFor(() => expect(cancelled).toBe(true));
    expect(screen.queryByText('编译中')).not.toBeInTheDocument();
  });

  it('失败任务支持取消并从任务列表移除', async () => {
    let cancelled = false;
    server.use(
      http.get('/api/v1/knowledge/llm-wiki/jobs', () =>
        HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: {
            items: cancelled ? [] : [{
              id: '14', status: 'failed', stage: 'compile', error: '模型调用失败',
              created_at: '2026-09-08T10:00:00Z', updated_at: '2026-09-08T10:01:00Z',
            }],
            total: cancelled ? 0 : 1,
          },
        }),
      ),
      http.post('/api/v1/knowledge/llm-wiki/jobs/14/cancel', () => {
        cancelled = true;
        return HttpResponse.json({
          code: 'ok',
          message: 'ok',
          data: { id: '14', status: 'cancelled', stage: 'cancelled', created_at: '2026-09-08T10:00:00Z', updated_at: '2026-09-08T10:02:00Z' },
        });
      }),
    );

    render(<KnowledgePage />);
    await userEvent.click(await screen.findByRole('button', { name: /^LLM Wiki/ }));
    await userEvent.click(screen.getByRole('button', { name: '编译任务' }));

    expect(await screen.findByText('模型调用失败')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '取消编译' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重试编译' })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: '取消编译' }));

    await waitFor(() => expect(cancelled).toBe(true));
    expect(screen.queryByText('模型调用失败')).not.toBeInTheDocument();
  });
});
