import { createContext, useContext, useLayoutEffect } from 'react';
import { create } from 'zustand';
import { useI18n } from '@/i18n/locale';

export type AssistantPage = { page: string; fields: Record<string, string> };
type Field = string | number | boolean | null | undefined;

// Only the owning page supplies facts. Never collect DOM text, raw URLs or form values.
export const AssistantRoute = createContext<string | null>(null);
export const useAssistantSource = create<{ route: string; snapshot: AssistantPage | null }>(() => ({
  route: '', snapshot: null,
}));

export function useAssistantPage(zh: string, en: string, facts: Record<string, Field> | null | undefined) {
  const route = useContext(AssistantRoute);
  const { tr } = useI18n();
  const page = tr(zh, en);
  const fields: Record<string, string> = {};
  let omitted = false;
  for (const [key, value] of Object.entries(facts ?? {})) {
    if (value == null || value === '') continue;
    const text = String(value).replace(/\s+/g, ' ').trim();
    if (text.length > 200) omitted = true;
    else if (text) fields[key] = text;
  }
  if (omitted) fields.partial = 'true';
  const serialized = facts === undefined ? undefined : Object.keys(fields).length ? JSON.stringify(fields) : null;
  useLayoutEffect(() => {
    if (route === null || serialized === undefined) return;
    const snapshot = serialized === null ? null : { page, fields: JSON.parse(serialized) };
    const entry = { route, snapshot };
    useAssistantSource.setState(entry, true);
    return () => {
      if (useAssistantSource.getState() === entry) useAssistantSource.setState({ route: '', snapshot: null });
    };
  }, [route, page, serialized]);
}
