import { useEffect, useMemo, useState } from 'react';
import { Plus, X } from 'lucide-react';

import { Button } from '@/components/ui';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';
import {
  ADVANCED_OPERATORS,
  COMPARE_OPS,
  OPERATOR_LABELS,
  SIMPLE_OPERATORS,
  buildClause,
  joinClauses,
  operatorNeedsValue,
  parseClause,
  parseConnectors,
  type Clause,
  type CompareOp,
  type Connector,
  type Operator,
} from '../lib/dsl';
import type { ValueTypeCode } from '../lib/valuetype';

// The filter builder.
//
// THE EXPRESSION IS THE VALUE, not the builder's row list. A page stores one
// string — the `q` it sends — and this component derives its rows from it. That
// keeps three things consistent that drift apart otherwise: what the builder
// shows, what the operator typed in the raw field, and what the server receives.
//
// THE CONNECTOR IS A SEPARATE CONTROL FROM THE OPERATOR. The language uses `-`
// for OR and `~` for NOT, and they compose (`-~` is OR-NOT); a builder that
// folded them into one dropdown would have to invent a combined vocabulary the
// server does not speak.

export type QueryAttribute = {
  /** The name, alias or id the expression addresses. */
  ref: string;
  label: string;
  type: ValueTypeCode;
  /** A JSON attribute is not matchable — the backend refuses it. */
  matchable?: boolean;
};

type Props = {
  attributes: QueryAttribute[];
  /** The expression. */
  value: string;
  onChange(next: string): void;
  /** Shown when the expression is empty. */
  placeholder?: string;
  /** Renders a clause row the builder cannot edit as text instead of guessing. */
  className?: string;
};

type Row = { connector: Connector; clause: Clause | null; token: string };

const CONNECTOR_LABELS: Record<Connector, [string, string]> = {
  and: ['并且', 'and'],
  or: ['或者', 'or'],
  andNot: ['并且不是', 'and not'],
  orNot: ['或者不是', 'or not'],
};

export function QueryBar({ attributes, value, onChange, placeholder, className }: Props) {
  const { tr } = useI18n();
  const [draft, setDraft] = useState<Row>(() => emptyRow());

  // THE PARSED ROWS COME FROM `value`, so an expression set from outside (a saved
  // filter, a "search this" link) is what the builder shows.
  const rows = useMemo<Row[]>(() => {
    if (!value.trim()) return [];
    return parseConnectors(value).map((entry) => {
      const clause = parseClause(entry.clause);
      return { connector: entry.connector, clause, token: entry.clause };
    });
  }, [value]);

  // THE DRAFT NEEDS A REAL ATTRIBUTE FROM THE START. A model's attributes are a
  // second request, so the first render has none — and a draft with an empty ref
  // builds a clause that cannot be emitted, which made the builder's first "add"
  // silently do nothing. It also re-points when the list arrives late or the
  // chosen attribute disappears.
  const draftRef = draft.clause?.ref;
  useEffect(() => {
    const known = draftRef !== undefined && attributes.some((attribute) => attribute.ref === draftRef);
    if (!known) setDraft(emptyRow(attributes[0]?.ref));
  }, [attributes, draftRef]);

  const commit = (next: Row[]) => {
    const parts = next
      .map((row) => {
        if (!row.clause) return { connector: row.connector, clause: row.token };
        try {
          return { connector: row.connector, clause: buildClause(row.clause) };
        } catch {
          // AN INCOMPLETE ROW IS NOT AN EXPRESSION. It stays in the builder until
          // the operator finishes it rather than becoming a half-formed term the
          // server would read as a different question.
          return null;
        }
      })
      .filter((part): part is { connector: Connector; clause: string } => part !== null);
    onChange(joinClauses(parts));
  };

  const selected = attributes.find((attribute) => attribute.ref === draft.clause?.ref) ?? attributes[0];

  return (
    <div className={cn('flex min-w-0 flex-col gap-2', className)}>
      {rows.length > 0 && (
        <ul className="flex min-w-0 flex-col gap-1">
          {rows.map((row, index) => (
            <li key={`${index}-${row.token}`} className="flex min-w-0 items-center gap-2 text-xs">
              <span className="w-20 shrink-0 text-text-faint">
                {index === 0 ? tr('筛选', 'Where') : tr(CONNECTOR_LABELS[row.connector][0], CONNECTOR_LABELS[row.connector][1])}
              </span>
              {row.clause ? (
                <code className="min-w-0 flex-1 truncate rounded bg-card px-2 py-1 font-mono text-[11px] text-text-muted">
                  {humanise(row.clause, attributes, tr)}
                </code>
              ) : (
                // WRITTEN BY HAND: shown as it is, because the builder cannot
                // round-trip a shape it does not emit.
                <code className="min-w-0 flex-1 truncate rounded border border-amber-500/30 bg-amber-500/5 px-2 py-1 font-mono text-[11px] text-amber-300">
                  {row.token}
                </code>
              )}
              <button
                type="button"
                aria-label={tr('移除这条筛选', 'Remove this filter')}
                onClick={() => {
                  const next = rows.filter((_, position) => position !== index);
                  commit(next);
                }}
                className="shrink-0 rounded p-1 text-text-faint hover:text-red-400"
              >
                <X size={12} aria-hidden="true" />
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <Select
          label={tr('连接方式', 'Connector')}
          value={draft.connector}
          onValueChange={(next) => setDraft((current) => ({ ...current, connector: next as Connector }))}
          options={(['and', 'or', 'andNot', 'orNot'] as Connector[]).map((connector) => ({
            value: connector,
            label: tr(CONNECTOR_LABELS[connector][0], CONNECTOR_LABELS[connector][1]),
          }))}
          className="w-32"
        />

        <Select
          label={tr('属性', 'Attribute')}
          value={selected?.ref ?? ''}
          onValueChange={(next) =>
            setDraft((current) => ({
              ...current,
              clause: { ...(current.clause ?? { operator: 'is' }), ref: next, operator: current.clause?.operator ?? 'is' },
            }))
          }
          options={attributes
            .filter((attribute) => attribute.matchable !== false)
            .map((attribute) => ({ value: attribute.ref, label: attribute.label }))}
          searchable
          className="w-44"
        />

        <Select
          label={tr('条件', 'Condition')}
          value={draft.clause?.operator ?? 'is'}
          onValueChange={(next) =>
            setDraft((current) => ({
              ...current,
              clause: { ...(current.clause ?? { ref: selected?.ref ?? '' }), operator: next as Operator },
            }))
          }
          options={[...SIMPLE_OPERATORS, ...ADVANCED_OPERATORS].map((operator) => ({
            value: operator,
            label: tr(OPERATOR_LABELS[operator][0], OPERATOR_LABELS[operator][1]),
          }))}
          searchable
          className="w-32"
        />

        <OperatorValueInput
          type={selected?.type ?? '2'}
          clause={draft.clause}
          onChange={(clause) => setDraft((current) => ({ ...current, clause }))}
        />

        <Button
          variant="outline"
          size="sm"
          type="button"
          onClick={() => {
            if (!draft.clause?.ref) return;
            commit([...rows, draft]);
            setDraft(emptyRow(selected?.ref));
          }}
          className="inline-flex items-center gap-1.5"
        >
          <Plus size={12} aria-hidden="true" />
          {tr('添加条件', 'Add filter')}
        </Button>
      </div>

      <Input
        type="search"
        aria-label={tr('筛选表达式', 'Filter expression')}
        placeholder={placeholder ?? tr('也可以直接写表达式，例如 hostname:web*', 'Or type an expression, e.g. hostname:web*')}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="font-mono text-[11px]"
      />
    </div>
  );
}

// --- the value field, by operator and attribute type ---

function OperatorValueInput({
  type,
  clause,
  onChange,
}: {
  type: ValueTypeCode;
  clause: Clause | null;
  onChange(next: Clause): void;
}) {
  const { tr } = useI18n();
  const operator = clause?.operator ?? 'is';

  if (!operatorNeedsValue(operator)) return null;

  if (operator === 'compare') {
    return (
      <div className="flex items-center gap-1">
        <Select
          label={tr('比较', 'Comparison')}
          value={clause?.compare ?? '>'}
          onValueChange={(next) => onChange({ ...(clause ?? { ref: '', operator }), compare: next as CompareOp })}
          options={COMPARE_OPS.map((op) => ({ value: op, label: op }))}
          className="w-20"
        />
        <Input
          aria-label={tr('比较值', 'Comparison value')}
          type={type === '0' || type === '1' ? 'number' : 'text'}
          value={clause?.value ?? ''}
          onChange={(event) => onChange({ ...(clause ?? { ref: '', operator }), value: event.target.value })}
          className="w-28"
        />
      </div>
    );
  }

  if (operator === 'in' || operator === 'notIn') {
    return (
      <Input
        aria-label={tr('候选值，用分号分隔', 'Candidates, separated by semicolons')}
        value={(clause?.values ?? []).join(';')}
        onChange={(event) =>
          onChange({
            ...(clause ?? { ref: '', operator }),
            values: event.target.value.split(';').map((item) => item.trim()).filter((item) => item !== ''),
          })
        }
        placeholder="a;b;c"
        className="w-48"
      />
    );
  }

  if (operator === 'range' || operator === 'notRange') {
    const inputType = type === '0' || type === '1' ? 'number' : type === '4' ? 'date' : type === '3' ? 'datetime-local' : 'text';
    return (
      <div className="flex items-center gap-1">
        <Input
          aria-label={tr('下限', 'Lower bound')}
          type={inputType}
          value={clause?.from ?? ''}
          onChange={(event) => onChange({ ...(clause ?? { ref: '', operator }), from: event.target.value })}
          className="w-32"
        />
        <span className="text-xs text-text-faint">–</span>
        <Input
          aria-label={tr('上限', 'Upper bound')}
          type={inputType}
          value={clause?.to ?? ''}
          onChange={(event) => onChange({ ...(clause ?? { ref: '', operator }), to: event.target.value })}
          className="w-32"
        />
      </div>
    );
  }

  const inputType = type === '0' || type === '1' ? 'number' : type === '4' ? 'date' : type === '3' ? 'datetime-local' : 'text';
  return (
    <Input
      aria-label={tr('值', 'Value')}
      type={inputType}
      value={clause?.value ?? ''}
      onChange={(event) => onChange({ ...(clause ?? { ref: '', operator }), value: event.target.value })}
      className="w-40"
    />
  );
}

function emptyRow(ref?: string): Row {
  return { connector: 'and', clause: ref ? { ref, operator: 'is' } : null, token: '' };
}

/**
 * Renders a clause in words for the summary line.
 *
 * THE EXPRESSION ITSELF IS SHOWN IN THE RAW FIELD; this is the readable echo
 * beside it, and it degrades to the operator's own name for anything it does not
 * recognise rather than pretending to understand it.
 */
function humanise(clause: Clause, attributes: QueryAttribute[], tr: (zh: string, en: string) => string): string {
  const attribute = attributes.find((item) => item.ref === clause.ref);
  const name = attribute?.label ?? clause.ref;
  const operator = tr(OPERATOR_LABELS[clause.operator][0], OPERATOR_LABELS[clause.operator][1]);
  if (!operatorNeedsValue(clause.operator)) return `${name} ${operator}`;
  if (clause.operator === 'in' || clause.operator === 'notIn') return `${name} ${operator} ${(clause.values ?? []).join(', ')}`;
  if (clause.operator === 'range' || clause.operator === 'notRange') return `${name} ${operator} ${clause.from} – ${clause.to}`;
  if (clause.operator === 'compare') return `${name} ${clause.compare} ${clause.value}`;
  return `${name} ${operator} ${clause.value}`;
}
