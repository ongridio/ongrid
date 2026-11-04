import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { InvestigationEvidence } from './InvestigationEvidence';
import { getInvestigationEvidence, type InvestigationReport } from '@/api/alerts';
import { ApiError } from '@/api/client';

vi.mock('@/api/alerts', () => ({ getInvestigationEvidence: vi.fn() }));
vi.mock('@/i18n/locale', () => ({ useI18n: () => ({ tr: (zh: string) => zh, locale: 'zh-CN' }), getLocale: () => 'zh-CN' }));

const report: InvestigationReport = {
  id: 'report-1', incident_id: 42, status: 'ready',
  evidence: [{ summary: '内存上涨', source_ids: ['E1'], status: 'traceable' }],
  evidence_check: { version: 1, status: 'partial', root_cause: { source_ids: ['E1', 'E2'], status: 'partial', issues: ['timeout:E2'] }, sources: [
    { id: 'E1', session_id: 'root', tool_call_id: 'metrics', tool: 'query_promql', state: 'available', target: 'device_id=42', started_at: '2026-11-04T06:20:00Z' },
    { id: 'E2', session_id: 'child', tool_call_id: 'logs', tool: 'query_logql', state: 'timeout', started_at: '2026-11-04T06:20:10Z' },
  ] },
};

describe('InvestigationEvidence', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('shows the missing observation and fetches the selected source only on demand', async () => {
    vi.mocked(getInvestigationEvidence).mockResolvedValue({ source: report.evidence_check!.sources[0], arguments: '{"expr":"memory"}', result: '{"value":95}', truncated: false });
    render(<InvestigationEvidence report={report} />);
    expect(screen.getByText('引用部分可追溯')).toBeInTheDocument();
    expect(screen.getByText(/引用的查询超时/)).toBeInTheDocument();
    expect(screen.getByText(/不能证明根因成立/)).toBeInTheDocument();
    expect(getInvestigationEvidence).not.toHaveBeenCalled();
    await userEvent.click(screen.getAllByRole('button', { name: '查看查询与结果' })[0]);
    expect(getInvestigationEvidence).toHaveBeenCalledWith(42, 'report-1', 'E1');
    expect(await screen.findByText(/"value": 95/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: '收起结果' }));
    expect(screen.queryByText(/"value": 95/)).not.toBeInTheDocument();
  });

  it.each([403, 404, 500])('distinguishes source access or availability failures (%d)', async status => {
    vi.mocked(getInvestigationEvidence).mockRejectedValue(new ApiError('internal message', status));
    render(<InvestigationEvidence report={report} />);
    await userEvent.click(screen.getAllByRole('button', { name: '查看查询与结果' })[0]);
    const message = await screen.findByRole('alert');
    expect(message.textContent).toContain(status === 403 ? '访问权限' : status === 404 ? '来源已不可用' : '读取原始结果失败');
    expect(message.textContent).not.toContain('internal message');
  });

  it('does not carry an expanded source across report regeneration', async () => {
    vi.mocked(getInvestigationEvidence).mockResolvedValue({ source: report.evidence_check!.sources[0], arguments: '{}', result: 'old observation', truncated: false });
    const view = render(<InvestigationEvidence report={report} />);
    await userEvent.click(screen.getAllByRole('button', { name: '查看查询与结果' })[0]);
    expect(await screen.findByText('old observation')).toBeInTheDocument();
    view.rerender(<InvestigationEvidence report={{ ...report, id: 'report-2' }} />);
    expect(screen.queryByText('old observation')).not.toBeInTheDocument();
  });

  it('marks historical reports unchecked', () => {
    render(<InvestigationEvidence report={{ id: 'old', incident_id: 42, status: 'ready', evidence: [{ summary: 'Legacy narrative' }] }} />);
    expect(screen.getByText('历史报告，引用未核对')).toBeInTheDocument();
    expect(screen.getByText('Legacy narrative')).toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });
});
