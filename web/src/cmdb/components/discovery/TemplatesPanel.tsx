import { useState } from 'react';
import { Download, FileUp, Upload } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Label } from '@/components/ui/Label';
import { Textarea } from '@/components/ui/Textarea';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

import { describeError } from '../../api/http';
import {
  ensureBuiltinRules,
  exportTemplates,
  importTemplates,
  type RuleInput,
} from '../../api/discovery';
import { useToast } from '../Toast';

// 模板 — moving provider definitions between deployments.
//
// EXPORT AND IMPORT SPEAK THE SAME SHAPE. An exported file is re-importable
// without a converter: the import is an upsert keyed by rule name, which is
// what makes a template file a way to reproduce a deployment rather than a
// one-way dump.

const EMPTY_TEMPLATE = '{\n  "rules": []\n}\n';

function readFileText(file: File): Promise<string> {
  if (typeof file.text === 'function') return file.text();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ''));
    reader.onerror = () => reject(reader.error ?? new Error('could not read the file'));
    reader.readAsText(file);
  });
}

/** Accepts the exported document or a bare array of rules. */
function parseTemplate(raw: string): RuleInput[] {
  const parsed: unknown = JSON.parse(raw);
  const rules = Array.isArray(parsed)
    ? parsed
    : parsed && typeof parsed === 'object' && Array.isArray((parsed as { rules?: unknown }).rules)
      ? (parsed as { rules: unknown[] }).rules
      : null;
  if (!rules) throw new Error('the template must be an object with a rules array');
  return rules as RuleInput[];
}

export function TemplatesPanel() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const toast = useToast();

  const [text, setText] = useState(EMPTY_TEMPLATE);
  const [busy, setBusy] = useState<'import' | 'export' | 'builtin' | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  const importFile = async () => {
    setErr(null);
    setResult(null);
    let rules: RuleInput[];
    try {
      rules = parseTemplate(text);
    } catch {
      setErr(tr('模板必须是包含 rules 数组的 JSON', 'The template must be JSON with a rules array'));
      return;
    }
    setBusy('import');
    try {
      const got = await importTemplates(rules);
      const message = tr(`新建 ${got.created} 个，更新 ${got.updated} 个`, `Created ${got.created}, updated ${got.updated}`);
      setResult(message);
      toast.success(message);
    } catch (error) {
      setErr(describeError(error, tr('导入失败', 'Import failed')));
    } finally {
      setBusy(null);
    }
  };

  const exportFile = async () => {
    setErr(null);
    setBusy('export');
    try {
      const doc = await exportTemplates();
      const formatted = JSON.stringify(doc, null, 2);
      setText(`${formatted}\n`);
      setResult(tr(`导出了 ${doc.rules?.length ?? 0} 个规则`, `Exported ${doc.rules?.length ?? 0} rules`));
      downloadJSON('cmdb-discovery-templates.json', formatted);
    } catch (error) {
      setErr(describeError(error, tr('导出失败', 'Export failed')));
    } finally {
      setBusy(null);
    }
  };

  const createBuiltins = async () => {
    setErr(null);
    setResult(null);
    setBusy('builtin');
    try {
      const got = await ensureBuiltinRules();
      const message = tr(
        `新建 ${got.created?.length ?? 0} 个内置规则，已存在 ${got.existing?.length ?? 0} 个。`,
        `Created ${got.created?.length ?? 0} built-in rules; ${got.existing?.length ?? 0} were already there.`,
      );
      setResult(message);
      toast.success(message);
    } catch (error) {
      setErr(describeError(error, tr('生成内置规则失败', 'Could not create the built-in rules')));
    } finally {
      setBusy(null);
    }
  };

  const chooseFile = async (file: File | undefined) => {
    if (!file) return;
    setErr(null);
    setResult(null);
    try {
      setText(await readFileText(file));
    } catch (error) {
      setErr(describeError(error, tr('无法读取文件', 'Could not read the file')));
    }
  };

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-text-muted">
        {tr(
          '导出会把所有规则写成同一个文件；导入按规则名更新或新建。',
          'Export writes every rule to one file; import updates or creates each rule by name.',
        )}
      </p>

      {err && (
        <p role="alert" className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </p>
      )}
      {result && (
        <p role="status" className="rounded-md border border-emerald-500/40 bg-emerald-500/5 px-3 py-2 text-xs text-emerald-300">
          {result}
        </p>
      )}

      <Card>
        <div className="flex flex-col gap-3 p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="min-w-0">
              <Label htmlFor="cmdb-discovery-template-file">{tr('上传模板文件', 'Upload a template file')}</Label>
              <p className="mt-0.5 text-[11px] text-text-faint">
                {tr('选择一个导出的 JSON 文件，内容会填到下面的文本框。', 'Pick an exported JSON file; its contents fill the box below.')}
              </p>
            </div>
            <label className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-xs text-text-muted hover:bg-card hover:text-text focus-within:outline focus-within:outline-2 focus-within:-outline-offset-2 focus-within:outline-indigo-500">
              <FileUp size={12} aria-hidden="true" />
              {tr('选择文件', 'Choose a file')}
              <input
                id="cmdb-discovery-template-file"
                type="file"
                accept="application/json,.json"
                className="sr-only"
                onChange={(event) => void chooseFile(event.target.files?.[0])}
              />
            </label>
          </div>

          <div>
            <Label htmlFor="cmdb-discovery-template-json">{tr('模板 JSON', 'Template JSON')}</Label>
            <Textarea
              id="cmdb-discovery-template-json"
              aria-label={tr('模板 JSON', 'Template JSON')}
              value={text}
              onChange={(event) => setText(event.target.value)}
              spellCheck={false}
              className="mt-1 min-h-[18rem] w-full font-mono text-xs"
            />
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="primary"
              type="button"
              onClick={() => void importFile()}
              disabled={!isAdmin || busy !== null}
              className="inline-flex items-center gap-1.5"
            >
              <Upload size={12} aria-hidden="true" />
              {busy === 'import' ? tr('导入中…', 'Importing…') : tr('导入模板', 'Import templates')}
            </Button>
            <Button
              variant="outline"
              type="button"
              onClick={() => void exportFile()}
              disabled={busy !== null}
              className="inline-flex items-center gap-1.5"
            >
              <Download size={12} aria-hidden="true" />
              {busy === 'export' ? tr('导出中…', 'Exporting…') : tr('导出模板', 'Export templates')}
            </Button>
            <Button
              variant="outline"
              type="button"
              onClick={() => void createBuiltins()}
              disabled={!isAdmin || busy !== null}
              className="inline-flex items-center gap-1.5"
            >
              {busy === 'builtin' ? tr('生成中…', 'Creating…') : tr('生成内置规则', 'Create built-in rules')}
            </Button>
          </div>
        </div>
      </Card>
    </div>
  );
}

function downloadJSON(filename: string, text: string): void {
  try {
    const blob = new Blob([text], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename;
    anchor.click();
    URL.revokeObjectURL(url);
  } catch {
    /* The text is already in the editor; a download is a convenience. */
  }
}
