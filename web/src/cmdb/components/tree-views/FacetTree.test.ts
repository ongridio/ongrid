import { describe, expect, it } from 'vitest';

import type { TypeAttribute } from '../../api/citype';
import { treeQuery } from './FacetTree';

// A hierarchy value is data; a comma in it must not become a new query term.
describe('treeQuery', () => {
  it('escapes a comma in a level value', () => {
    const levels = [{ name: 'idc' } as TypeAttribute];
    expect(treeQuery(1, levels, ['Beijing, CN'])).toBe('_type:1,idc:Beijing\\, CN');
  });
});
