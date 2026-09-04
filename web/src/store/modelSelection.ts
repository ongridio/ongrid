import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';
import type { ModelSelection } from '@/components/ChatInput';

// useModelSelection holds the user's chosen (provider, model) for chat and
// persists it to localStorage. `selected` is Home's default for newly opened
// sessions; `sessionSelections` freezes an independent choice per chat thread
// so switching routes cannot leak the most recently selected model into a
// different conversation.
//
// `selected` is null until the user explicitly picks; callers fall back to
// the live catalog default while it's null, so the default still tracks the
// server config rather than a stale pinned value.
type ModelSelectionState = {
  selected: ModelSelection | null;
  sessionSelections: Record<string, ModelSelection>;
  setSelected(m: ModelSelection | null): void;
  setSessionSelected(sessionId: string, m: ModelSelection): void;
  clearSessionSelected(sessionId: string): void;
};

export const useModelSelection = create<ModelSelectionState>()(
  persist(
    (set) => ({
      selected: null,
      sessionSelections: {},
      setSelected: (selected) => set({ selected }),
      setSessionSelected: (sessionId, selected) =>
        set((state) => ({
          sessionSelections: { ...state.sessionSelections, [sessionId]: selected },
        })),
      clearSessionSelected: (sessionId) =>
        set((state) => {
          const { [sessionId]: _removed, ...sessionSelections } = state.sessionSelections;
          return { sessionSelections };
        }),
    }),
    {
      name: 'ongrid.model-selection',
      storage: createJSONStorage(() => localStorage),
    }
  )
);
