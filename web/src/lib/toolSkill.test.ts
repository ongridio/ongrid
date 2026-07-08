import { describe, expect, it } from 'vitest';

import { groupTitle, orderedGroupKeys, toolGroupKey } from './toolSkill';

describe('toolSkill grouping', () => {
  it('groups Kubernetes read tools under observability and write tools under other', () => {
    expect(toolGroupKey('query_k8s_snapshot')).toBe('observe');
    expect(toolGroupKey('describe_k8s_resource')).toBe('observe');
    expect(toolGroupKey('query_k8s_logs')).toBe('observe');
    expect(toolGroupKey('execute_k8s_action')).toBe('other');
    expect(groupTitle('observe', true)).toBe('观测');
    expect(groupTitle('other', true)).toBe('其他');
  });

  it('keeps observability before other built-in groups', () => {
    expect(orderedGroupKeys(['fleet', 'observe', 'other'])).toEqual(['observe', 'fleet', 'other']);
  });
});
