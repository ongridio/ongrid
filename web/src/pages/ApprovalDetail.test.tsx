import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { useAuth } from '@/store/auth';
import * as api from '@/api/approvals';
import ApprovalDetailPage, { reviewPayload } from './ApprovalDetail';
vi.mock('@/api/approvals', () => ({ getApproval: vi.fn(), approveApproval: vi.fn(), rejectApproval: vi.fn() }));
const dialogs = vi.hoisted(() => ({ confirmAction: vi.fn(), promptAction: vi.fn() }));
vi.mock('@/components/ui/useDialogs', () => ({ useDialogs: () => ({ ...dialogs, dialog: null }) }));
const row: api.Approval = { id:'test-id',kind:'agent_tool',title:'Restart test service',summary:'Review target',payload:'{"execution_token":"private-token","target":"test-host"}',source:'agent',status:'pending',proposed_by:1,created_at:'2026-11-01T00:00:00Z' };
function show() { return render(<MemoryRouter initialEntries={['/approvals/test-id']}><Routes><Route path="/approvals/:id" element={<ApprovalDetailPage />} /></Routes></MemoryRouter>); }
beforeEach(() => {
 vi.resetAllMocks(); useAuth.setState({ role:'admin',token:'test' });
 vi.mocked(api.getApproval).mockResolvedValue(row);
 dialogs.confirmAction.mockResolvedValue(true); dialogs.promptAction.mockResolvedValue('No maintenance window');
});
afterEach(cleanup);
describe('approval detail', () => {
 it('preserves external MCP operation states on the shared approval route', async () => {
  vi.mocked(api.getApproval).mockResolvedValue({...row, kind:'external_mcp', status:'approved', operation:{operation_id:'op-test',state:'running',verification:'pending',applied:null}});
  show();
  await screen.findByText(/正在执行|Running/);
  expect(screen.queryByRole('button',{name:/批准并执行|Approve and execute/})).not.toBeInTheDocument();
  expect(api.approveApproval).not.toHaveBeenCalled();
 });
 it('opening a link only reads and redacts execution capabilities', async () => {
  show(); await screen.findByText(row.title);
  expect(api.approveApproval).not.toHaveBeenCalled();
  expect(screen.queryByText(/private-token/)).not.toBeInTheDocument();
  expect(screen.getByText(/test-host/)).toBeInTheDocument();
 });
 it('does not fetch privileged details for a non-admin', () => {
  useAuth.setState({role:'user'}); show(); expect(api.getApproval).not.toHaveBeenCalled();
 });
 it('prevents duplicate submits', async () => {
  let finish!: (v:api.Approval) => void;
  vi.mocked(api.approveApproval).mockReturnValue(new Promise(resolve => {finish=resolve;}));
  show(); await screen.findByText(row.title);
  const button=screen.getByRole('button',{name:/批准并执行|Approve and execute/});
  fireEvent.click(button); fireEvent.click(button);
  await waitFor(() => expect(api.approveApproval).toHaveBeenCalledTimes(1));
  vi.mocked(api.getApproval).mockResolvedValue({...row,status:'executed'}); finish({...row,status:'executed'});
  await waitFor(() => expect(screen.queryByRole('button',{name:/批准并执行|Approve and execute/})).not.toBeInTheDocument());
 });
 it('cancelled confirmation never executes', async () => {
  dialogs.confirmAction.mockResolvedValue(false); show(); await screen.findByText(row.title);
  fireEvent.click(screen.getByRole('button',{name:/批准并执行|Approve and execute/}));
  await waitFor(() => expect(dialogs.confirmAction).toHaveBeenCalledOnce()); expect(api.approveApproval).not.toHaveBeenCalled();
 });
 it('refreshes after timeout without assuming execution failed', async () => {
  vi.mocked(api.approveApproval).mockRejectedValue(new Error('timeout'));
  show(); await screen.findByText(row.title);
  vi.mocked(api.getApproval).mockResolvedValue({...row,status:'approved'});
  fireEvent.click(screen.getByRole('button',{name:/批准并执行|Approve and execute/}));
  await screen.findByRole('alert'); await waitFor(() => expect(api.getApproval).toHaveBeenCalledTimes(2));
  expect(screen.queryByRole('button',{name:/批准并执行|Approve and execute/})).not.toBeInTheDocument();
 });
 it('rejects with the entered reason', async () => {
  show(); await screen.findByText(row.title); vi.mocked(api.getApproval).mockResolvedValue({...row,status:'rejected'});
  fireEvent.click(screen.getByRole('button',{name:/^拒绝$|^Reject$/}));
  await waitFor(() => expect(api.rejectApproval).toHaveBeenCalledWith('test-id','No maintenance window'));
 });
 it('redacts credentials inside serialized tool arguments', () => expect(reviewPayload(JSON.stringify({args_json:JSON.stringify({password:'hidden'})}))).not.toContain('hidden'));
 it('redacts nested credential fields' , () => expect(reviewPayload('{"nested":{"password":"hidden"}}')).not.toContain('hidden'));
});
