// Sheet import. The browser parses the file; the backend maps columns, validates
// rows and writes them in one transaction.

import { cmdbPost } from './http';

export type ImportIssue = {
  column?: string;
  message: string;
};

export type ImportColumn = {
  index: number;
  header: string;
  attr_id?: number;
  attr?: string;
  alias?: string;
  identifier?: boolean;
  ignored?: boolean;
  matched: boolean;
  note?: string;
};

export type ImportRow = {
  row: number;
  sheet_row: number;
  ok: boolean;
  action?: 'create' | 'update';
  unique?: string;
  ci_id?: number;
  issues?: ImportIssue[];
};

export type ImportRequest = {
  ci_type: string;
  headers: string[];
  rows: unknown[][];
  exist_policy: 'reject' | 'replace';
};

export type ImportPreview = {
  total: number;
  valid: number;
  invalid: number;
  creates: number;
  updates: number;
  columns: ImportColumn[];
  rows: ImportRow[];
};

export type ImportResult = {
  written: number;
  creates: number;
  updates: number;
  rows: ImportRow[];
};

export function previewImport(input: ImportRequest): Promise<ImportPreview> {
  return cmdbPost<ImportPreview>('/import/preview', input);
}

export function commitImport(input: ImportRequest): Promise<ImportResult> {
  return cmdbPost<ImportResult>('/import/commit', input);
}
