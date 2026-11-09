import { describe, expect, it } from 'vitest';

import { toAPITime } from './HistoryFilters';

// datetime-local is a wall clock with no zone. The API compares instants, so
// the browser has to say which instant it means; sending the bare wall clock
// made a UTC+8 operator filter the UTC day.
describe('toAPITime', () => {
  it('sends the selected wall clock as an RFC 3339 instant', () => {
    const local = '2026-10-26T00:00';
    const got = toAPITime(local);

    expect(got).toBeDefined();
    expect(got).toBe(new Date(local).toISOString());
    expect(got).toMatch(/Z$/);
  });

  it('keeps seconds when the picker supplies them', () => {
    const local = '2026-10-26T23:59:59';
    expect(toAPITime(local)).toBe(new Date(local).toISOString());
  });

  it('drops an empty filter', () => {
    expect(toAPITime('   ')).toBeUndefined();
  });
});
