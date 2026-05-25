import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';
import type { ModelSelection } from '@/components/ChatInput';

// useModelSelection holds the user's chosen (provider, model) for chat,
// shared across Home + every ChatThread and persisted to localStorage. It
// was previously per-component local state that re-defaulted to the catalog
// default on every mount — so picking a model on Home, navigating away, and
// back reverted to the default (and the launched session never inherited the
// pick). One persisted store fixes all of that.
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
