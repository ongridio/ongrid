import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';
import type { ModelSelection } from '@/components/ChatInput';

// This store holds only the Home/catalog default. Conversation-specific
// choices live on chat_sessions in the backend.
//
// `selected` is null until the user explicitly picks; callers fall back to
// the live catalog default while it's null, so the default still tracks the
// server config rather than a stale pinned value.
type ModelSelectionState = {
  selected: ModelSelection | null;
  setSelected(m: ModelSelection | null): void;
};

export const useModelSelection = create<ModelSelectionState>()(
  persist(
    (set) => ({
      selected: null,
      setSelected: (selected) => set({ selected }),
    }),
    {
      name: 'ongrid.model-selection',
      storage: createJSONStorage(() => localStorage),
    }
  )
);
