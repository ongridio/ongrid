// The query language, as the console builds it.
//
// THE SYNTAX IS THE BACKEND'S AND IT IS NOT LUCENE. It was measured against
// cantor's own implementation rather than read off its documentation, and the
// measured reading differs from the prose in ways that matter here:
//
//   attr:value              equality
//   attr:(v1;v2)            membership — SEMICOLONS, not commas
//   attr:[v1 _TO_ v2]       closed interval
//   attr:>5  >=  <  <=      comparison
//   attr:*                  "has a value"
//   *                       wildcard inside a value (`web*`)
//   ~term                   NOT that term
//   ,                       AND
//   -                       OR — a token beginning with `-` joins the running set
//                           by UNION, and the word after it is NOT negated
//   -~term                  OR-NOT
//
// SO `-` IS A CONNECTOR, NOT A NEGATION, and `~` is the negation. Getting those
// the wrong way round produces a query that reads correctly and selects the
// complement of what the operator meant — which is why the console never emits
// `-` from a value field: the connector is a separate choice, and the value is
// escaped so it cannot become one.
//
// VALUES ARE ESCAPED FOR COMMAS ONLY. That is the one metacharacter the language
// has an escape for (a `\` before the comma keeps the token whole); a value
// containing `(`/`)`/`;`/`[`/`]` cannot be expressed, and the backend refuses
// the query rather than silently misreading it. Refusing is the right outcome —
// a filter that quietly matches something else is worse than one that errors.

/** The connectors a clause may use, in the language's own vocabulary. */
export type Connector = 'and' | 'or' | 'andNot' | 'orNot';

/** The operators the filter builder offers, matching cantor's UI. */
export type Operator =
  | 'is'
  | 'notIs'
  | 'contain'
  | 'notContain'
  | 'startsWith'
  | 'notStartsWith'
  | 'endsWith'
  | 'notEndsWith'
  | 'hasValue'
  | 'hasNoValue'
  | 'in'
  | 'notIn'
  | 'range'
  | 'notRange'
  | 'compare';

/** The comparison operators `attr:>5` accepts. */
export type CompareOp = '>' | '>=' | '<' | '<=';

/** One clause, in the builder's terms. */
export type Clause = {
  /** The attribute reference: name, alias or id. */
  ref: string;
  operator: Operator;
  /** A single value, for the operators that take one. */
  value?: string;
  /** The members, for `in`/`notIn`. */
  values?: string[];
  /** The endpoints, for `range`/`notRange`. */
  from?: string;
  to?: string;
  /** The operator, for `compare`. */
  compare?: CompareOp;
};

/** The operators cantor's builder offered, in its order. */
export const SIMPLE_OPERATORS: readonly Operator[] = [
  'is',
  'notIs',
  'contain',
  'notContain',
  'startsWith',
  'notStartsWith',
  'endsWith',
  'notEndsWith',
  'hasValue',
  'hasNoValue',
];

export const ADVANCED_OPERATORS: readonly Operator[] = ['in', 'notIn', 'range', 'notRange', 'compare'];

export const COMPARE_OPS: readonly CompareOp[] = ['>', '>=', '<', '<='];

/** [Chinese, English] labels, resolved through the caller's `tr`. */
export const OPERATOR_LABELS: Record<Operator, [string, string]> = {
  is: ['等于', 'is'],
  notIs: ['不等于', 'is not'],
  contain: ['包含', 'contains'],
  notContain: ['不包含', "doesn't contain"],
  startsWith: ['开头是', 'starts with'],
  notStartsWith: ['开头不是', "doesn't start with"],
  endsWith: ['结尾是', 'ends with'],
  notEndsWith: ['结尾不是', "doesn't end with"],
  hasValue: ['有值', 'has a value'],
  hasNoValue: ['无值', 'has no value'],
  in: ['属于', 'is one of'],
  notIn: ['不属于', 'is none of'],
  range: ['介于', 'is between'],
  notRange: ['不介于', 'is not between'],
  compare: ['比较', 'compares'],
};

/** Whether an operator needs a value field at all. */
export function operatorNeedsValue(operator: Operator): boolean {
  return operator !== 'hasValue' && operator !== 'hasNoValue';
}

/**
 * Escapes a value so it cannot become the language's punctuation.
 *
 * ONLY THE COMMA HAS AN ESCAPE. A backslash before it keeps the token whole;
 * everything else is literal text and a value containing `;` or brackets will be
 * refused by the backend, which is the behaviour we want.
 */
export function escapeValue(value: string): string {
  return value.replace(/,/g, '\\,');
}

/**
 * Builds one clause.
 *
 * IT THROWS ON AN INCOMPLETE CLAUSE rather than emitting a half-formed term. A
 * `range` with one endpoint would otherwise become `attr:[a _TO_ ]`, which the
 * backend reads as a different question than the operator asked.
 */
export function buildClause(clause: Clause): string {
  const ref = clause.ref.trim();
  if (!ref) throw new Error('a clause needs an attribute');
  const value = clause.value ?? '';

  switch (clause.operator) {
    case 'is':
      return `${ref}:${escapeValue(value)}`;
    case 'notIs':
      return `~${ref}:${escapeValue(value)}`;
    case 'contain':
      return `${ref}:*${escapeValue(value)}*`;
    case 'notContain':
      return `~${ref}:*${escapeValue(value)}*`;
    case 'startsWith':
      return `${ref}:${escapeValue(value)}*`;
    case 'notStartsWith':
      return `~${ref}:${escapeValue(value)}*`;
    case 'endsWith':
      return `${ref}:*${escapeValue(value)}`;
    case 'notEndsWith':
      return `~${ref}:*${escapeValue(value)}`;
    case 'hasValue':
      return `${ref}:*`;
    case 'hasNoValue':
      return `~${ref}:*`;
    case 'in':
    case 'notIn': {
      const members = (clause.values ?? []).map((item) => escapeValue(item)).filter((item) => item !== '');
      if (members.length === 0) throw new Error('a membership clause needs at least one value');
      const term = `${ref}:(${members.join(';')})`;
      return clause.operator === 'in' ? term : `~${term}`;
    }
    case 'range':
    case 'notRange': {
      const from = (clause.from ?? '').trim();
      const to = (clause.to ?? '').trim();
      if (!from || !to) throw new Error('a range clause needs both endpoints');
      const term = `${ref}:[${escapeValue(from)} _TO_ ${escapeValue(to)}]`;
      return clause.operator === 'range' ? term : `~${term}`;
    }
    case 'compare': {
      const op = clause.compare;
      if (!op) throw new Error('a comparison clause needs an operator');
      const trimmed = value.trim();
      if (!trimmed) throw new Error('a comparison clause needs a value');
      return `${ref}:${op}${escapeValue(trimmed)}`;
    }
  }
}

/** The prefix a connector puts on a clause. */
export function connectorPrefix(connector: Connector): string {
  switch (connector) {
    case 'and':
      return '';
    case 'or':
      // `-` JOINS BY UNION and does not negate — see the header.
      return '-';
    case 'andNot':
      return '~';
    case 'orNot':
      return '-~';
  }
}

/** Joins clauses into one expression. */
export function joinClauses(clauses: Array<{ connector: Connector; clause: string }>): string {
  return clauses
    .filter((entry) => entry.clause.trim() !== '')
    .map((entry) => `${connectorPrefix(entry.connector)}${entry.clause}`)
    .join(',');
}

/**
 * Splits an expression into its tokens, honouring the comma escape.
 *
 * IT EXISTS SO THE RAW EDITOR AND THE TESTS AGREE with the backend about where a
 * token ends. A naive `split(',')` turns a value the console escaped
 * (`web\,01`) into two clauses, one of which the backend then reads as a
 * different filter.
 */
export function splitTokens(expression: string): string[] {
  const out: string[] = [];
  let current = '';
  for (let i = 0; i < expression.length; i += 1) {
    const ch = expression[i];
    if (ch === '\\' && expression[i + 1] === ',') {
      // The escape is CONSUMED: the token keeps the comma, drops the backslash.
      current += ',';
      i += 1;
      continue;
    }
    if (ch === ',') {
      if (current !== '') out.push(current);
      current = '';
      continue;
    }
    current += ch;
  }
  if (current !== '') out.push(current);
  return out;
}

/**
 * Splits an expression into clause and connector pairs, which is what the
 * builder needs to reopen a saved filter.
 *
 * THE CONNECTOR IS READ OFF THE TOKEN'S PREFIX in the order the language itself
 * uses: `-~`, then `-`, then `~`. Reading `~` first would turn every OR-NOT into
 * an AND-NOT.
 */
export function parseConnectors(expression: string): Array<{ connector: Connector; clause: string }> {
  return splitTokens(expression).map((token) => {
    if (token.startsWith('-~')) return { connector: 'orNot', clause: token.slice(2) };
    if (token.startsWith('-')) return { connector: 'or', clause: token.slice(1) };
    if (token.startsWith('~')) return { connector: 'andNot', clause: token.slice(1) };
    return { connector: 'and', clause: token };
  });
}

// --- reading a clause back ---
//
// THE BUILDER HAS TO REOPEN A SAVED FILTER, which means turning one token back
// into the clause it came from. Only the forms the builder itself emits are
// recognised; a token written by hand in the raw editor is reported as `null`
// and the builder shows the expression text instead. Guessing at an unfamiliar
// shape would be worse than admitting the builder cannot edit it.

export type ParsedClause = { ref: string; operator: Operator; value?: string; values?: string[]; from?: string; to?: string; compare?: CompareOp };

const RANGE_RE = /^([^:]+):\[(.*?) _TO_ (.*?)\]$/;
const IN_RE = /^([^:]+):\((.*)\)$/;
const COMPARE_RE = /^([^:]+):(>=|<=|>|<)(.*)$/;
const TERM_RE = /^([^:]+):(.*)$/;

export function parseClause(token: string): ParsedClause | null {
  if (!token) return null;

  const range = RANGE_RE.exec(token);
  if (range) {
    return { ref: range[1], operator: 'range', from: unescapeValue(range[2]), to: unescapeValue(range[3]) };
  }

  const membership = IN_RE.exec(token);
  if (membership) {
    const values = membership[2] === '' ? [] : membership[2].split(';').map(unescapeValue);
    return { ref: membership[1], operator: 'in', values };
  }

  const compare = COMPARE_RE.exec(token);
  if (compare) {
    return { ref: compare[1], operator: 'compare', compare: compare[2] as CompareOp, value: unescapeValue(compare[3]) };
  }

  const term = TERM_RE.exec(token);
  if (!term) return null;
  const ref = term[1];
  const raw = term[2];

  if (raw === '*') return { ref, operator: 'hasValue' };
  if (raw.startsWith('*') && raw.endsWith('*') && raw.length >= 2) {
    return { ref, operator: 'contain', value: unescapeValue(raw.slice(1, -1)) };
  }
  if (raw.startsWith('*')) return { ref, operator: 'endsWith', value: unescapeValue(raw.slice(1)) };
  if (raw.endsWith('*')) return { ref, operator: 'startsWith', value: unescapeValue(raw.slice(0, -1)) };
  return { ref, operator: 'is', value: unescapeValue(raw) };
}

function unescapeValue(raw: string): string {
  return raw.replace(/\\,/g, ',');
}
