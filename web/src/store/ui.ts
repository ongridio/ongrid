import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';

type UiState = {
  sidebarCollapsed: boolean;
  toggleSidebar(): void;
  setSidebarCollapsed(v: boolean): void;
  sessionsCollapsed: boolean;
  toggleSessions(): void;
  sessionsPreviewOpen: boolean;
  setSessionsPreviewOpen(v: boolean): void;
  sessionsTriggerHovered: boolean;
  setSessionsTriggerHovered(v: boolean): void;
  // Command palette (⌘P) — keyed in store so Sidebar's search button
  // and the global keyboard handler in Layout can both flip it.
  paletteOpen: boolean;
  setPaletteOpen(v: boolean): void;
  // Agent side panel (⌘K) — same rationale; surfaced via global hotkey
  // for now, but kept open here so future entry points (status row,
  // empty state CTAs) can pop it without prop-drilling.
  agentPanelOpen: boolean;
  setAgentPanelOpen(v: boolean): void;
};

export const useUi = create<UiState>()(
  persist(
    (set, get) => ({
      sidebarCollapsed: false,
      toggleSidebar: () => set({ sidebarCollapsed: !get().sidebarCollapsed }),
      setSidebarCollapsed: (v) => set({ sidebarCollapsed: v }),
      sessionsCollapsed: false,
      toggleSessions: () => set({ sessionsCollapsed: !get().sessionsCollapsed, sessionsPreviewOpen: false, sessionsTriggerHovered: false }),
      sessionsPreviewOpen: false,
      setSessionsPreviewOpen: (v) => set({ sessionsPreviewOpen: v }),
      sessionsTriggerHovered: false,
      setSessionsTriggerHovered: (v) => set({ sessionsTriggerHovered: v, ...(v && get().sessionsCollapsed ? { sessionsPreviewOpen: true } : {}) }),
      paletteOpen: false,
      setPaletteOpen: (v) => set({ paletteOpen: v }),
      agentPanelOpen: false,
      setAgentPanelOpen: (v) => set({ agentPanelOpen: v }),
    }),
    {
      name: 'ongrid.ui',
      storage: createJSONStorage(() => localStorage),
      // Don't persist transient overlay state; only the structural
      // sidebar preferences should survive reload.
      partialize: (s) => ({ sidebarCollapsed: s.sidebarCollapsed, sessionsCollapsed: s.sessionsCollapsed }),
    }
  )
);
