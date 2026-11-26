// Sheet parsing for CMDB batch import.
//
import type ExcelJS from 'exceljs';

// THE BACKEND TAKES A JSON SHEET, NOT A FILE: `{headers, rows}` is what
// /import/preview and /import/commit understand. Parsing stays in the browser
// so the server needs no spreadsheet dependency.

export const MAX_IMPORT_ROWS = 5000;
export const MAX_IMPORT_COLUMNS = 200;

export class SheetError extends Error {
  readonly code: 'format' | 'rows' | 'columns' | 'empty';

  constructor(code: SheetError['code'], message: string) {
    super(message);
    this.name = 'SheetError';
    this.code = code;
  }
}

export type ParsedSheet = {
  headers: string[];
  rows: unknown[][];
};

/** Parses a CSV or XLSX file into the backend's sheet shape. */
export async function parseSheetFile(file: File): Promise<ParsedSheet> {
  const name = file.name.toLowerCase();
  if (name.endsWith('.csv')) {
    return normalise(parseCSV(await readText(file)));
  }
  if (name.endsWith('.xlsx') || name.endsWith('.xlsm')) {
    return normalise(await parseXLSX(file));
  }
  throw new SheetError('format', 'Choose a CSV or XLSX file');
}

function normalise(raw: unknown[][]): ParsedSheet {
  if (raw.length === 0) {
    throw new SheetError('empty', 'The sheet is empty');
  }
  const headers = raw[0].map((value) => String(value ?? '').trim());
  if (headers.length === 0 || headers.every((header) => header === '')) {
    throw new SheetError('empty', 'The first row must contain column names');
  }
  if (headers.length > MAX_IMPORT_COLUMNS) {
    throw new SheetError('columns', `The sheet has more than ${MAX_IMPORT_COLUMNS} columns`);
  }
  const rows = raw
    .slice(1)
    .filter((row) => row.some((value) => String(value ?? '').trim() !== ''))
    .map((row) => {
      const out = row.slice(0, headers.length).map((value) => value ?? '');
      while (out.length < headers.length) out.push('');
      return out;
    });
  if (rows.length > MAX_IMPORT_ROWS) {
    throw new SheetError('rows', `The sheet has more than ${MAX_IMPORT_ROWS} data rows`);
  }
  return { headers, rows };
}

/** A small RFC-4180-ish CSV parser: quotes, escaped quotes, embedded newlines. */
export function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = '';
  let quoted = false;

  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (quoted) {
      if (char === '"') {
        if (text[index + 1] === '"') {
          field += '"';
          index += 1;
        } else {
          quoted = false;
        }
      } else {
        field += char;
      }
      continue;
    }
    if (char === '"') {
      quoted = true;
    } else if (char === ',') {
      row.push(field);
      field = '';
    } else if (char === '\n' || char === '\r') {
      if (char === '\r' && text[index + 1] === '\n') index += 1;
      row.push(field);
      field = '';
      if (row.some((cell) => cell !== '')) rows.push(row);
      row = [];
    } else {
      field += char;
    }
  }
  if (quoted) {
    throw new SheetError('format', 'The CSV has an unterminated quoted field');
  }
  row.push(field);
  if (row.some((cell) => cell !== '')) rows.push(row);
  return rows;
}

// jsdom (tests) and some older browsers expose File without text()/arrayBuffer();
// FileReader is available in both.
function readText(file: File): Promise<string> {
  if (typeof file.text === 'function') return file.text();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ''));
    reader.onerror = () => reject(reader.error ?? new Error('Could not read the file'));
    reader.readAsText(file);
  });
}

function readArrayBuffer(file: File): Promise<ArrayBuffer> {
  if (typeof file.arrayBuffer === 'function') return file.arrayBuffer();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(reader.error ?? new Error('Could not read the file'));
    reader.readAsArrayBuffer(file);
  });
}

async function parseXLSX(file: File): Promise<unknown[][]> {
  const mod = await import('exceljs');
  const ExcelJS = mod.default ?? mod;
  const workbook = new ExcelJS.Workbook();
  await workbook.xlsx.load(await readArrayBuffer(file));
  const sheet = workbook.worksheets[0];
  if (!sheet) {
    throw new SheetError('empty', 'The workbook has no worksheet');
  }
  const rows: unknown[][] = [];
  sheet.eachRow({ includeEmpty: false }, (row) => {
    const values: unknown[] = [];
    row.eachCell({ includeEmpty: true }, (cell) => {
      values.push(cellValue(cell.value));
    });
    rows.push(values);
  });
  return rows;
}

function cellValue(value: ExcelJS.CellValue): unknown {
  if (value == null) return '';
  if (value instanceof Date) return formatExcelDate(value);
  if (typeof value !== 'object') return value;
  if ('richText' in value) return value.richText.map((part) => part.text).join('');
  if ('result' in value) return value.result ?? '';
  if ('text' in value) return value.text ?? '';
  if ('hyperlink' in value) {
    const link = value as unknown as Record<string, unknown>;
    return link.text ?? link.hyperlink ?? '';
  }
  return String(value);
}

/**
 * Formats an Excel date as the sheet's wall clock.
 *
 * EXCEL DATES ARE NOT INSTANTS. ExcelJS returns a date cell as a Date at the
 * serial's UTC epoch, so the UTC fields are the day and time the sheet shows;
 * formatting them keeps a UTC+8 operator's 2026-10-26 from becoming 10-25.
 *
 * THE SPACE-SEPARATED FORM is what the import validators parse: a date
 * attribute rejects `2026-10-26T00:00:00.000Z`, while `2026-10-26 00:00:00`
 * parses as both a date and a datetime.
 */
function formatExcelDate(value: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${value.getUTCFullYear()}-${pad(value.getUTCMonth() + 1)}-${pad(value.getUTCDate())} ` +
    `${pad(value.getUTCHours())}:${pad(value.getUTCMinutes())}:${pad(value.getUTCSeconds())}`
  );
}

/** Builds a one-row XLSX template with the model's attribute aliases. */
export async function buildTemplateBlob(headers: string[]): Promise<Blob> {
  const mod = await import('exceljs');
  const ExcelJS = mod.default ?? mod;
  const workbook = new ExcelJS.Workbook();
  const sheet = workbook.addWorksheet('template');
  sheet.addRow(headers);
  sheet.getRow(1).font = { bold: true };
  const buffer = await workbook.xlsx.writeBuffer();
  return new Blob([buffer], {
    type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  });
}
