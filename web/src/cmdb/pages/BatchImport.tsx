import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Download, FileUp, Loader2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { PageHeader } from '@/components/ui/PageHeader';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';

import { listCITypes, listTypeAttributes, type CIType } from '../api/citype';
import { describeError } from '../api/http';
import {
  commitImport,
  previewImport,
  type ImportPreview,
  type ImportRequest,
  type ImportResult,
} from '../api/import';
import {
  buildTemplateBlob,
  MAX_IMPORT_COLUMNS,
  MAX_IMPORT_ROWS,
  parseSheetFile,
  SheetError,
  type ParsedSheet,
} from '../lib/sheet';
import { useToast } from '../components/Toast';

// 批量导入 — choose a model, parse the file in the browser, preview the
// server's mapping, then commit the same sheet in one transaction.
//
// THE PREVIEW IS THE CONTRACT: the commit sends the same `{headers, rows}` the
// preview did, so a valid preview cannot turn into a different commit.

export default function BatchImportPage() {
  const { tr } = useI18n();
  const toast = useToast();
  const [models, setModels] = useState<CIType[]>([]);
  const [modelRef, setModelRef] = useState('');
  const [policy, setPolicy] = useState<'reject' | 'replace'>('reject');
  const [sheet, setSheet] = useState<ParsedSheet | null>(null);
  const [fileName, setFileName] = useState('');
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [previewing, setPreviewing] = useState(false);
  const [committing, setCommitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    void listCITypes()
      .then((got) => live && setModels(got))
      .catch((error) => live && setErr(describeError(error, tr('无法读取模型', 'Could not load the models'))))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [tr]);

  const requestFor = useCallback(
    (parsed: ParsedSheet): ImportRequest => ({
      ci_type: modelRef,
      headers: parsed.headers,
      rows: parsed.rows,
      exist_policy: policy,
    }),
    [modelRef, policy],
  );

  const runPreview = useCallback(
    async (parsed: ParsedSheet) => {
      setPreviewing(true);
      setErr(null);
      setPreview(null);
      setResult(null);
      try {
        setPreview(await previewImport(requestFor(parsed)));
      } catch (error) {
        setErr(describeError(error, tr('预览失败', 'Could not preview the sheet')));
      } finally {
        setPreviewing(false);
      }
    },
    [requestFor, tr],
  );

  const chooseFile = async (file: File | undefined) => {
    if (!file) return;
    if (!modelRef) {
      setErr(tr('先选择模型，再上传表格。', 'Choose a model before uploading a sheet.'));
      return;
    }
    setFileName(file.name);
    setSheet(null);
    try {
      const parsed = await parseSheetFile(file);
      setSheet(parsed);
      await runPreview(parsed);
    } catch (error) {
      setErr(
        error instanceof SheetError
          ? error.message
          : describeError(error, tr('无法解析表格', 'Could not parse the sheet')),
      );
    }
  };

  const changePolicy = async (next: 'reject' | 'replace') => {
    setPolicy(next);
    if (sheet) {
      setPreviewing(true);
      setErr(null);
      try {
        setPreview(
          await previewImport({
            ci_type: modelRef,
            headers: sheet.headers,
            rows: sheet.rows,
            exist_policy: next,
          }),
        );
      } catch (error) {
        setErr(describeError(error, tr('预览失败', 'Could not preview the sheet')));
      } finally {
        setPreviewing(false);
      }
    }
  };

  const commit = async () => {
    if (!sheet || !preview || preview.invalid > 0 || committing) return;
    setCommitting(true);
    setErr(null);
    try {
      const got = await commitImport(requestFor(sheet));
      setResult(got);
      toast.success(tr(`已导入 ${got.written} 行`, `Imported ${got.written} rows`));
    } catch (error) {
      setErr(describeError(error, tr('导入失败', 'Could not import the sheet')));
    } finally {
      setCommitting(false);
    }
  };

  const downloadTemplate = async () => {
    if (!modelRef) return;
    try {
      const got = await listTypeAttributes(modelRef);
      const headers = (got.attributes ?? []).map((attribute) => attribute.alias || attribute.name);
      const blob = await buildTemplateBlob(headers);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = `${models.find((model) => String(model.id) === modelRef)?.name || 'cmdb'}-template.xlsx`;
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      setErr(describeError(error, tr('无法生成模板', 'Could not build the template')));
    }
  };

  const invalidRows = (preview?.rows ?? []).filter((row) => !row.ok).slice(0, 5);

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('批量导入', 'Batch import')}
        subtitle={tr(
          `上传 CSV 或 XLSX，先预览映射与错误，再一次性写入。最多 ${MAX_IMPORT_ROWS} 行、${MAX_IMPORT_COLUMNS} 列。`,
          `Upload a CSV or XLSX, preview the mapping and errors, then write in one transaction. Up to ${MAX_IMPORT_ROWS} rows and ${MAX_IMPORT_COLUMNS} columns.`,
        )}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" type="button" onClick={() => void downloadTemplate()} disabled={!modelRef}>
              <Download size={12} className="me-1.5" aria-hidden="true" />
              {tr('下载模板', 'Download template')}
            </Button>
            <Link to="/cmdb/instances" className="text-xs font-medium text-indigo-400 underline">
              {tr('返回资源数据', 'Back to resources')}
            </Link>
          </div>
        }
      />

      <div className="min-h-0 flex-1 overflow-y-auto p-6">
        <Card className="mb-4">
          <div className="grid gap-4 md:grid-cols-3">
            <div>
              <Label htmlFor="cmdb-import-model">{tr('模型', 'Model')}</Label>
              <Select
                id="cmdb-import-model"
                label={tr('模型', 'Model')}
                value={modelRef}
                onValueChange={(value) => {
                  setModelRef(value);
                  setSheet(null);
                  setPreview(null);
                  setResult(null);
                  setFileName('');
                }}
                options={models.map((model) => ({ value: String(model.id), label: model.alias || model.name }))}
                className="mt-1 w-full"
                disabled={loading}
              />
            </div>
            <div>
              <Label htmlFor="cmdb-import-policy">{tr('已存在时', 'When the instance exists')}</Label>
              <Select
                id="cmdb-import-policy"
                label={tr('已存在时', 'When the instance exists')}
                value={policy}
                onValueChange={(value) => void changePolicy(value as 'reject' | 'replace')}
                options={[
                  { value: 'reject', label: tr('拒绝这一行', 'Reject the row') },
                  { value: 'replace', label: tr('覆盖已有值', 'Replace the values') },
                ]}
                className="mt-1 w-full"
              />
            </div>
            <div>
              <Label htmlFor="cmdb-import-file">{tr('上传表格', 'Upload sheet')}</Label>
              <Input
                id="cmdb-import-file"
                type="file"
                accept=".csv,.xlsx,.xlsm"
                className="mt-1 w-full"
                onChange={(event) => {
                  void chooseFile(event.target.files?.[0]);
                  event.target.value = '';
                }}
              />
              {fileName && <p className="mt-1 truncate text-[11px] text-text-faint">{fileName}</p>}
            </div>
          </div>
          {err && (
            <div role="alert" className="mt-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
              {err}
            </div>
          )}
        </Card>

        {previewing && (
          <Card className="mb-4">
            <p className="flex items-center gap-2 py-4 text-xs text-text-muted">
              <Loader2 size={12} className="animate-spin" aria-hidden="true" />
              {tr('正在预览…', 'Previewing…')}
            </p>
          </Card>
        )}

        {preview && (
          <>
            <Card className="mb-4">
              <div className="flex flex-wrap items-center gap-2">
                <FileUp size={14} className="text-text-muted" aria-hidden="true" />
                <span className="text-sm font-medium text-text">{tr('预览', 'Preview')}</span>
                <Chip tone={preview.invalid > 0 ? 'warning' : 'success'}>
                  {tr(`${preview.valid} 行可导入，${preview.invalid} 行有问题`, `${preview.valid} valid, ${preview.invalid} invalid`)}
                </Chip>
                <span className="text-xs text-text-muted">
                  {tr(`将新建 ${preview.creates} 行，更新 ${preview.updates} 行`, `${preview.creates} create, ${preview.updates} update`)}
                </span>
                <Button
                  variant="primary"
                  size="sm"
                  type="button"
                  onClick={() => void commit()}
                  disabled={committing || preview.invalid > 0}
                  className="ms-auto"
                >
                  {committing ? tr('导入中…', 'Importing…') : tr('导入', 'Import')}
                </Button>
              </div>
            </Card>

            <Card className="mb-4 overflow-x-auto">
              <table className="og-resource-table w-full text-xs">
                <thead>
                  <tr>
                    <th className="text-start">{tr('列', 'Column')}</th>
                    <th className="text-start">{tr('匹配属性', 'Attribute')}</th>
                    <th className="text-start">{tr('说明', 'Note')}</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.columns.map((column) => (
                    <tr key={column.index}>
                      <td className="font-mono">{column.header || '—'}</td>
                      <td>
                        {column.matched ? (
                          <span>{column.alias || column.attr || '—'}</span>
                        ) : (
                          <Chip tone="warning">{tr('未匹配', 'Unmatched')}</Chip>
                        )}
                        {column.identifier && <Chip tone="info">{tr('标识', 'Identifier')}</Chip>}
                      </td>
                      <td className="text-text-muted">{column.note || (column.ignored ? tr('只读属性，已忽略', 'Read-only, ignored') : '—')}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>

            {invalidRows.length > 0 && (
              <Card className="mb-4">
                <h2 className="mb-2 text-sm font-medium text-text">{tr('需要修正的行', 'Rows to fix')}</h2>
                <ul className="divide-y divide-border">
                  {invalidRows.map((row) => (
                    <li key={row.row} className="py-1.5 text-xs">
                      <span className="font-mono text-text-faint">#{row.sheet_row}</span>{' '}
                      {(row.issues ?? []).map((issue) => `${issue.column ? `${issue.column}: ` : ''}${issue.message}`).join('; ')}
                    </li>
                  ))}
                </ul>
              </Card>
            )}
          </>
        )}

        {result && (
          <Card>
            <p className="text-sm text-text">
              {tr(`已导入 ${result.written} 行`, `Imported ${result.written} rows`)}
              <span className="ms-2 text-xs text-text-muted">
                {tr(`新建 ${result.creates}，更新 ${result.updates}`, `${result.creates} created, ${result.updates} updated`)}
              </span>
            </p>
          </Card>
        )}
      </div>
    </div>
  );
}
