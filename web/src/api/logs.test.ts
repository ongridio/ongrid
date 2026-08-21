import { describe, expect, it } from 'vitest';

import { currentLogBackend, type LogBackend } from './logs';

describe('currentLogBackend', () => {
  it('uses the explicit server-side current backend for an unselected visible row', () => {
    expect(currentLogBackend({ current_backend: 'elasticsearch' } as LogBackend)).toBe('elasticsearch');
    expect(currentLogBackend({ current_backend: 'loki' } as LogBackend)).toBe('loki');
  });

  it('uses Loki when there is no configured backend', () => {
    expect(currentLogBackend(null)).toBe('loki');
  });
});
