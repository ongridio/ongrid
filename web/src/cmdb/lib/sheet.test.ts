import ExcelJS from 'exceljs';
import { describe, expect, it } from 'vitest';

import { parseSheetFile, SheetError } from './sheet';

function csvFile(text: string, name = 'hosts.csv'): File {
  return new File([text], name, { type: 'text/csv' });
}

describe('sheet parsing', () => {
  it('parses a CSV header and rows, including quoted commas', async () => {
    const parsed = await parseSheetFile(csvFile('hostname,cpu,note\nweb-01,8,"rack, A"\nweb-02,4,\n'));
    expect(parsed).toEqual({
      headers: ['hostname', 'cpu', 'note'],
      rows: [
        ['web-01', '8', 'rack, A'],
        ['web-02', '4', ''],
      ],
    });
  });

  it('parses an XLSX workbook', async () => {
    const workbook = new ExcelJS.Workbook();
    const sheet = workbook.addWorksheet('hosts');
    sheet.addRow(['hostname', 'cpu']);
    sheet.addRow(['web-01', 8]);
    const buffer = await workbook.xlsx.writeBuffer();
    const file = new File([buffer], 'hosts.xlsx', {
      type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    });

    const parsed = await parseSheetFile(file);
    expect(parsed.headers).toEqual(['hostname', 'cpu']);
    expect(parsed.rows).toEqual([['web-01', 8]]);
  });

  it('writes Excel dates as the wall clock the import parser accepts', async () => {
    const workbook = new ExcelJS.Workbook();
    const sheet = workbook.addWorksheet('hosts');
    sheet.addRow(['hostname', 'installed_on', 'last_seen']);
    sheet.addRow([
      'web-01',
      new Date(Date.UTC(2026, 9, 26)),
      new Date(Date.UTC(2026, 9, 26, 13, 45, 30)),
    ]);
    const buffer = await workbook.xlsx.writeBuffer();
    const file = new File([buffer], 'hosts.xlsx', {
      type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    });

    // A date attribute rejects the toISOString() form; the space-separated
    // wall clock is what both the date and the datetime validators parse.
    expect((await parseSheetFile(file)).rows).toEqual([
      ['web-01', '2026-10-26 00:00:00', '2026-10-26 13:45:30'],
    ]);
  });

  it('refuses too many rows and unsupported extensions', async () => {
    const tooMany = ['hostname', ...Array.from({ length: 5001 }, (_, index) => `host-${index}`)].join('\n');
    await expect(parseSheetFile(csvFile(tooMany))).rejects.toBeInstanceOf(SheetError);
    await expect(parseSheetFile(new File(['x'], 'hosts.txt'))).rejects.toThrow(/CSV or XLSX/);
  });
});
