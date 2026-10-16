import { dump } from 'js-yaml';

export type ContentFormat = 'markdown' | 'plain' | 'json' | 'yaml';

export const contentFormats: { value: ContentFormat; label: string }[] = [
  { value: 'markdown', label: 'Markdown' },
  { value: 'plain', label: 'Plain' },
  { value: 'json', label: 'JSON' },
  { value: 'yaml', label: 'YAML' },
];

// SDKs can report a complete JSON document as a string; decode only its display value.
export function structuredContent(value: unknown): unknown {
  if (typeof value !== 'string') return value;
  try {
    const parsed: unknown = JSON.parse(value, (_, item: unknown) => {
      // Keep the original text when JavaScript cannot retain a reported number.
      if (
        typeof item === 'number' &&
        (!Number.isFinite(item) ||
          (Number.isInteger(item) && !Number.isSafeInteger(item)))
      ) {
        throw new Error('Number cannot be represented safely');
      }
      return item;
    });
    return parsed !== null && typeof parsed === 'object' ? parsed : value;
  } catch {
    return value;
  }
}

export function serializeContent(value: unknown, format: 'json' | 'yaml') {
  if (value === undefined) return '';
  return format === 'yaml'
    ? dump(value, { noRefs: true, lineWidth: 120 })
    : JSON.stringify(value, null, 2);
}
