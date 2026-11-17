import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import { SavedToolResult } from './SavedToolResult';
import { getSavedResult, readSavedResult } from '@/api/toolResults';
import { setLocale } from '@/i18n/locale';

vi.mock('@/api/toolResults', () => ({ getSavedResult: vi.fn(), readSavedResult: vi.fn(), downloadSavedResult: vi.fn() }));
beforeEach(() => {
  vi.clearAllMocks(); setLocale('en-US');
  vi.mocked(getSavedResult).mockResolvedValue({ id: 'result', storage_status: 'ready', capture_status: 'complete', execution_status: 'success', captured_bytes: 10000, expires_at: '' });
  vi.mocked(readSavedResult).mockResolvedValue({ items: [{ start: 0, end: 100, text: 'first page' }], scan_start: 0, scan_end: 100, scan_complete: false, next_cursor: 'next' });
});
it('replaces pages, binds continuation to the active search, and preserves failed input', async () => {
  render(<SavedToolResult sessionId="session" resultId="result" />);
  fireEvent.click(screen.getByText('View saved result'));
  await screen.findByText('first page');
  fireEvent.change(screen.getByLabelText('Search result'), { target: { value: 'failed' } });
  fireEvent.click(screen.getByText('Search'));
  await waitFor(() => expect(readSavedResult).toHaveBeenLastCalledWith('session', 'result', 'failed', undefined, expect.any(AbortSignal)));
  await waitFor(() => expect(screen.getByText('Continue reading')).not.toBeDisabled());
  fireEvent.change(screen.getByLabelText('Search result'), { target: { value: 'unsubmitted' } });
  vi.mocked(readSavedResult).mockResolvedValueOnce({ items: [{ start: 100, end: 200, text: 'second page' }], scan_start: 100, scan_end: 200, scan_complete: true });
  fireEvent.click(screen.getByText('Continue reading'));
  await screen.findByText('second page');
  expect(readSavedResult).toHaveBeenLastCalledWith('session', 'result', 'failed', 'next', expect.any(AbortSignal));
  expect(screen.queryByText('first page')).not.toBeInTheDocument();
  vi.mocked(readSavedResult).mockRejectedValueOnce(new Error('Read unavailable'));
  fireEvent.click(screen.getByText('Search'));
  await screen.findByRole('alert');
  expect(screen.getByLabelText('Search result')).toHaveValue('unsubmitted');
});
it('shows expired storage without pretending the result was empty', async () => {
  vi.mocked(getSavedResult).mockResolvedValueOnce({ id: 'result', storage_status: 'expired', capture_status: 'complete', execution_status: 'success', captured_bytes: 10000, expires_at: '' });
  render(<SavedToolResult sessionId="session" resultId="result" />);
  fireEvent.click(screen.getByText('View saved result'));
  await screen.findByText('The original result has expired or is unavailable.');
  expect(readSavedResult).not.toHaveBeenCalled();
});
