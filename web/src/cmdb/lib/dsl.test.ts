import { describe, expect, it } from 'vitest';

import {
  buildClause,
  connectorPrefix,
  escapeValue,
  joinClauses,
  parseClause,
  parseConnectors,
  splitTokens,
} from './dsl';

// The query language's two dangerous properties are tested here: what a value
// cannot become, and which of `-` / `~` is the negation.

describe('building clauses', () => {
  it('emits the syntax the backend reads', () => {
    expect(buildClause({ ref: 'hostname', operator: 'is', value: 'web-01' })).toBe('hostname:web-01');
    expect(buildClause({ ref: 'hostname', operator: 'notIs', value: 'web-01' })).toBe('~hostname:web-01');
    expect(buildClause({ ref: 'hostname', operator: 'contain', value: 'web' })).toBe('hostname:*web*');
    expect(buildClause({ ref: 'hostname', operator: 'startsWith', value: 'web' })).toBe('hostname:web*');
    expect(buildClause({ ref: 'hostname', operator: 'endsWith', value: '01' })).toBe('hostname:*01');
    expect(buildClause({ ref: 'hostname', operator: 'hasValue' })).toBe('hostname:*');
    expect(buildClause({ ref: 'hostname', operator: 'hasNoValue' })).toBe('~hostname:*');
    expect(buildClause({ ref: 'cpu', operator: 'in', values: ['4', '8'] })).toBe('cpu:(4;8)');
    expect(buildClause({ ref: 'cpu', operator: 'notIn', values: ['4', '8'] })).toBe('~cpu:(4;8)');
    expect(buildClause({ ref: 'cpu', operator: 'range', from: '4', to: '8' })).toBe('cpu:[4 _TO_ 8]');
    expect(buildClause({ ref: 'cpu', operator: 'compare', compare: '>=', value: '4' })).toBe('cpu:>=4');
  });

  it('refuses a clause it cannot express rather than emitting half of one', () => {
    expect(() => buildClause({ ref: '', operator: 'is', value: 'x' })).toThrow();
    expect(() => buildClause({ ref: 'cpu', operator: 'range', from: '4' })).toThrow();
    expect(() => buildClause({ ref: 'cpu', operator: 'in', values: [] })).toThrow();
    expect(() => buildClause({ ref: 'cpu', operator: 'compare', value: '4' })).toThrow();
    expect(() => buildClause({ ref: 'cpu', operator: 'compare', compare: '>', value: '  ' })).toThrow();
  });

  it('escapes a comma so a value cannot become a second clause', () => {
    expect(escapeValue('web,01')).toBe('web\\,01');
    // THE ESCAPED FORM MUST SURVIVE A BUILD: the token stays whole.
    expect(splitTokens(buildClause({ ref: 'hostname', operator: 'is', value: 'web,01' }))).toEqual(['hostname:web,01']);
  });
});

describe('connectors', () => {
  // `-` IS OR AND `~` IS NOT. Folding them together is the mistake that produces
  // a query reading correctly and selecting the complement.
  it('spells OR and NOT differently', () => {
    expect(connectorPrefix('and')).toBe('');
    expect(connectorPrefix('or')).toBe('-');
    expect(connectorPrefix('andNot')).toBe('~');
    expect(connectorPrefix('orNot')).toBe('-~');
  });

  it('joins clauses with commas and puts the connector in the prefix', () => {
    expect(
      joinClauses([
        { connector: 'and', clause: 'a:1' },
        { connector: 'or', clause: 'b:2' },
        { connector: 'andNot', clause: 'c:3' },
        { connector: 'orNot', clause: 'd:4' },
      ]),
    ).toBe('a:1,-b:2,~c:3,-~d:4');
  });

  it('reads a prefix back in the order the language uses', () => {
    // `-~` BEFORE `-` BEFORE `~`: reading `~` first turns every OR-NOT into an
    // AND-NOT and every OR into an AND-NOT.
    expect(parseConnectors('a:1,-b:2,~c:3,-~d:4').map((entry) => entry.connector)).toEqual([
      'and',
      'or',
      'andNot',
      'orNot',
    ]);
  });
});

describe('splitting', () => {
  it('keeps an escaped comma inside its token', () => {
    expect(splitTokens('hostname:a\\,b,cpu:4')).toEqual(['hostname:a,b', 'cpu:4']);
  });

  it('drops empty tokens rather than turning them into clauses', () => {
    expect(splitTokens(',a:1,,b:2,')).toEqual(['a:1', 'b:2']);
  });
});

describe('reading a clause back', () => {
  it('round-trips every form the builder emits', () => {
    const clauses = [
      { ref: 'hostname', operator: 'is', value: 'web-01' },
      { ref: 'hostname', operator: 'contain', value: 'web' },
      { ref: 'hostname', operator: 'startsWith', value: 'web' },
      { ref: 'hostname', operator: 'endsWith', value: '01' },
      { ref: 'hostname', operator: 'hasValue' },
      { ref: 'cpu', operator: 'in', values: ['4', '8'] },
      { ref: 'cpu', operator: 'range', from: '4', to: '8' },
      { ref: 'cpu', operator: 'compare', compare: '<=', value: '8' },
    ] as const;

    for (const clause of clauses) {
      const token = buildClause(clause as never);
      const parsed = parseClause(token);
      expect(parsed, token).not.toBeNull();
      expect(parsed).toMatchObject({ ref: clause.ref, operator: clause.operator });
    }
  });

  it('says so when it does not understand a token', () => {
    // A FREE-TEXT QUERY HAS NO COLON, so it is not a clause at all — the page
    // then shows the expression text instead of guessing at a shape the builder
    // does not emit. (Everything with a colon IS a clause: the language has no
    // deeper grammar to be wrong about.)
    expect(parseClause('web-01')).toBeNull();
    expect(parseClause('')).toBeNull();
  });
});
