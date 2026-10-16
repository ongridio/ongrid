import { create } from 'zustand';
import { listSessions, type ChatSession } from '@/api/chat';

// chatSessionsStore is the single source of truth for the home/chat
// session list. Components that mutate sessions (creating a new one in
// Home, sending the first message that bumps `updated_at`, etc.) call
// invalidateChatSessions() to trigger a refetch so the session list updates
// without a full page reload.
type ChatSessionsState = {
  sessions: ChatSession[];
  loading: boolean;
  error: boolean;
  refresh(): Promise<void>;
};

export const useChatSessions = create<ChatSessionsState>((set) => ({
  sessions: [],
  loading: false,
  error: false,
  async refresh() {
    set({ loading: true, error: false });
    try {
      const r = await listSessions();
      set({ sessions: r.items ?? [], loading: false, error: false });
    } catch {
      set({ loading: false, error: true });
    }
  },
}));

// invalidateChatSessions triggers a refetch from anywhere in the app.
export function invalidateChatSessions() {
  void useChatSessions.getState().refresh();
}
