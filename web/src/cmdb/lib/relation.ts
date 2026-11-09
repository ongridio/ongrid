// cantor stores a model relation's cardinality as 0/1/2; the console shows the
// familiar 1-N / 1-1 / N-N labels. The form must send the code, not the label —
// the handler validates against model.Constraint and rejects "1-N".
export const RELATION_CONSTRAINTS = [
  { value: '0', label: '1-N' },
  { value: '1', label: '1-1' },
  { value: '2', label: 'N-N' },
] as const;

export function relationConstraintLabel(code: string): string {
  return RELATION_CONSTRAINTS.find((item) => item.value === code)?.label ?? code;
}
