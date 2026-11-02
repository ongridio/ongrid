import { describe, expect, it } from 'vitest';
import { safeLoginReturn } from './loginReturn';
describe('login return', () => {
  it('preserves a local approval destination', () => expect(safeLoginReturn('/approvals/a-123')).toBe('/approvals/a-123'));
  it.each(['//evil.example', '/\\evil.example', 'https://evil.example', '/login', '/\n/evil.example', null, {}])('rejects unsafe destination %j', value => expect(safeLoginReturn(value)).toBe('/'));
});
