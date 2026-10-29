import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, expect, it, vi } from 'vitest';
import ApprovalConfirmationPage from './ApprovalConfirmation';
import { approveApproval, getApproval, rejectApproval, type Approval } from '@/api/approvals';

vi.mock('@/api/approvals', () => ({ approveApproval: vi.fn(), getApproval: vi.fn(), rejectApproval: vi.fn() }));
const pending: Approval = {
  id: 'specific-operation', kind: 'external_mcp', title: 'Update fixture panel', summary: 'panel:123', payload: JSON.stringify({ arguments: { title: 'CPU' } }),
  source: 'external_mcp', status: 'pending', proposed_by: 1, created_at: '2026-10-29T00:00:00Z',
  operation: { operation_id: 'op-123', state: 'awaiting_approval', verification: 'pending', applied: null },
};
const completed: Approval = { ...pending, status: 'executed', operation: { ...pending.operation!, state: 'succeeded', verification: 'read_back', applied: true } };
const show = () => render(<MemoryRouter initialEntries={['/approvals/specific-operation']}><Routes><Route path="/approvals/:id" element={<ApprovalConfirmationPage />} /></Routes></MemoryRouter>);
beforeEach(() => {
  vi.resetAllMocks();
  localStorage.setItem('ongrid-locale', 'en-US');
  vi.mocked(getApproval).mockResolvedValue(pending);
});

it('opens the exact operation without executing and confirms once without a second dialog', async () => {
  let finish!: (value: Approval) => void;
  vi.mocked(approveApproval).mockReturnValue(new Promise(resolve => { finish = resolve; }));
  show();
  const button = await screen.findByRole('button', { name: 'Confirm and execute' });
  expect(getApproval).toHaveBeenCalledWith('specific-operation');
  expect(approveApproval).not.toHaveBeenCalled();
  expect(screen.getByText('panel:123')).toBeInTheDocument();
  fireEvent.click(button);
  fireEvent.click(button);
  expect(approveApproval).toHaveBeenCalledOnce();
  expect(approveApproval).toHaveBeenCalledWith('specific-operation');
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Submitting…' })).toBeDisabled();
  await act(async () => finish(completed));
  expect(await screen.findByText('Completed')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Confirm and execute' })).not.toBeInTheDocument();
});

it('rejects without execution and keeps the reason when a decision fails', async () => {
  vi.mocked(rejectApproval).mockRejectedValueOnce(new Error('offline')).mockResolvedValue({ ok: true });
  show();
  const reason = await screen.findByLabelText('Rejection reason (optional)');
  fireEvent.change(reason, { target: { value: 'Wrong target' } });
  fireEvent.click(screen.getByRole('button', { name: 'Reject' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('offline');
  expect(reason).toHaveValue('Wrong target');
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
  fireEvent.click(screen.getByRole('button', { name: 'Reject' }));
  expect(await screen.findByText('Rejected. This operation will not execute.')).toBeInTheDocument();
  expect(rejectApproval).toHaveBeenLastCalledWith('specific-operation', 'Wrong target');
  expect(approveApproval).not.toHaveBeenCalled();
});

it('shows a failed read with retry and no confirmation controls', async () => {
  vi.mocked(getApproval).mockRejectedValue(new Error('Not found'));
  show();
  expect(await screen.findByRole('alert')).toHaveTextContent('Not found');
  expect(screen.queryByRole('button', { name: 'Confirm and execute' })).not.toBeInTheDocument();
  expect(approveApproval).not.toHaveBeenCalled();
});

it('shows cancellation when the linked approval is invalidated', async () => {
  vi.mocked(getApproval).mockResolvedValue({ ...pending, status: 'failed', operation: { ...pending.operation!, state: 'cancelled', applied: false } });
  show();
  expect(await screen.findByText('Cancelled')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Confirm and execute' })).not.toBeInTheDocument();
});
