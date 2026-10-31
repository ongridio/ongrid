import { useCallback, useEffect, useMemo, useState } from 'react';
import { Pencil, Plus, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { Select } from '@/components/ui/Select';
import { Textarea } from '@/components/ui/Textarea';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';

import { listCITypes, type CIType } from '../../api/citype';
import { describeError } from '../../api/http';
import {
  createSavedSearch,
  deleteSavedSearch,
  listSavedSearches,
  updateSavedSearch,
  type SavedSearch,
} from '../../api/preferences';
import { useToast } from '../Toast';

// 保存搜索 — a person's own search recipes.
//
// THE OPTION IS A JSON OBJECT because that is what the backend stores; the
// textarea is deliberately explicit rather than a second query builder. A
// malformed object is refused before the request, so the operator keeps the
// text they typed.

export function SavedSearchesPanel() {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();
  const [searches, setSearches] = useState<SavedSearch[]>([]);
  const [models, setModels] = useState<CIType[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<SavedSearch | null | undefined>(undefined);
  const [name, setName] = useState('');
  const [typeID, setTypeID] = useState('');
  const [optionText, setOptionText] = useState('{}');
  const [formErr, setFormErr] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const [gotSearches, gotModels] = await Promise.all([listSavedSearches(), listCITypes()]);
      setSearches(gotSearches);
      setModels(gotModels);
    } catch (error) {
      setErr(describeError(error, tr('无法读取保存搜索', 'Could not load the saved searches')));
    } finally {
      setLoading(false);
    }
  }, [tr]);

  useEffect(() => {
    void load();
  }, [load]);

  const typeName = useMemo(() => {
    const byID = new Map(models.map((model) => [model.id, model]));
    return (id?: number) => (id == null ? tr('全部模型', 'All models') : byID.get(id)?.alias || byID.get(id)?.name || `#${id}`);
  }, [models, tr]);

  const openNew = () => {
    setEditing(null);
    setName('');
    setTypeID('');
    setOptionText('{\n  "q": ""\n}');
    setFormErr(null);
  };

  const openEdit = (search: SavedSearch) => {
    setEditing(search);
    setName(search.name);
    setTypeID(search.type_id == null ? '' : String(search.type_id));
    setOptionText(JSON.stringify(search.option ?? {}, null, 2));
    setFormErr(null);
  };

  const close = () => {
    setEditing(undefined);
    setFormErr(null);
  };

  const save = async () => {
    const trimmed = name.trim();
    if (!trimmed || saving) return;
    let option: unknown;
    try {
      option = JSON.parse(optionText);
    } catch {
      setFormErr(tr('查询 JSON 不是合法 JSON', 'The query JSON is not valid JSON'));
      return;
    }
    if (option === null || typeof option !== 'object' || Array.isArray(option)) {
      setFormErr(tr('查询必须是一个 JSON 对象', 'The query must be a JSON object'));
      return;
    }
    setSaving(true);
    setFormErr(null);
    try {
      const input = {
        name: trimmed,
        option: option as Record<string, unknown>,
        ...(typeID ? { type_id: Number(typeID) } : {}),
      };
      if (editing) await updateSavedSearch(editing.id, input);
      else await createSavedSearch(input);
      toast.success(editing ? tr('已保存', 'Saved') : tr('已创建', 'Created'));
      close();
      await load();
    } catch (error) {
      setFormErr(describeError(error, tr('保存失败', 'Could not save it')));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (search: SavedSearch) => {
    const ok = await confirmAction(
      tr(`删除保存搜索「${search.name}」？`, `Delete the saved search “${search.name}”?`),
    );
    if (!ok) return;
    try {
      await deleteSavedSearch(search.id);
      toast.success(tr('已删除', 'Deleted'));
      await load();
    } catch (error) {
      setErr(describeError(error, tr('删除失败', 'Could not delete it')));
    }
  };

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-sm font-medium text-text">{tr('保存搜索', 'Saved searches')}</h2>
            <p className="mt-1 text-xs text-text-muted">
              {tr('保存的查询表达式可以在资源搜索页直接复用。', 'Saved expressions can be reused on the resource search page.')}
            </p>
          </div>
          <Button variant="primary" type="button" onClick={openNew} className="inline-flex items-center gap-1.5">
            <Plus size={14} aria-hidden="true" />
            {tr('新建保存搜索', 'New saved search')}
          </Button>
        </div>
        {err && (
          <div role="alert" className="mt-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}
      </Card>

      {loading ? (
        <Card>
          <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
        </Card>
      ) : searches.length === 0 ? (
        <Card>
          <p className="py-6 text-center text-xs text-text-muted">{tr('还没有保存搜索', 'No saved searches yet')}</p>
        </Card>
      ) : (
        <Card>
          <ul className="divide-y divide-border">
            {searches.map((search) => (
              <li key={search.id} className="flex flex-wrap items-center gap-2 py-2 text-xs">
                <span className="min-w-0 flex-1 truncate font-medium text-text">{search.name}</span>
                <span className="text-text-muted">{typeName(search.type_id)}</span>
                <Button
                  variant="subtle"
                  size="sm"
                  type="button"
                  onClick={() => openEdit(search)}
                  aria-label={tr(`编辑 ${search.name}`, `Edit ${search.name}`)}
                >
                  <Pencil size={12} aria-hidden="true" />
                </Button>
                <Button
                  variant="dangerGhost"
                  size="sm"
                  type="button"
                  onClick={() => void remove(search)}
                  aria-label={tr(`删除 ${search.name}`, `Delete ${search.name}`)}
                >
                  <Trash2 size={12} aria-hidden="true" />
                </Button>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {editing !== undefined && (
        <Modal
          open
          onClose={close}
          title={editing ? tr('编辑保存搜索', 'Edit saved search') : tr('新建保存搜索', 'New saved search')}
          footer={
            <div className="flex flex-wrap justify-end gap-2">
              <Button variant="outline" type="button" onClick={close} disabled={saving}>
                {tr('取消', 'Cancel')}
              </Button>
              <Button variant="primary" type="button" onClick={() => void save()} disabled={saving || !name.trim()}>
                {saving ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
              </Button>
            </div>
          }
        >
          {formErr && (
            <div role="alert" className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
              {formErr}
            </div>
          )}
          <div className="flex flex-col gap-3">
            <div>
              <Label htmlFor="cmdb-search-name">{tr('名称', 'Name')}</Label>
              <Input
                id="cmdb-search-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                className="mt-1 w-full"
              />
            </div>
            <div>
              <Label htmlFor="cmdb-search-type">{tr('模型', 'Model')}</Label>
              <Select
                id="cmdb-search-type"
                label={tr('模型', 'Model')}
                value={typeID}
                onValueChange={setTypeID}
                options={[
                  { value: '', label: tr('全部模型', 'All models') },
                  ...models.map((model) => ({ value: String(model.id), label: model.alias || model.name })),
                ]}
                className="mt-1 w-full"
              />
            </div>
            <div>
              <Label htmlFor="cmdb-search-option">{tr('查询 JSON', 'Query JSON')}</Label>
              <Textarea
                id="cmdb-search-option"
                value={optionText}
                onChange={(event) => setOptionText(event.target.value)}
                rows={8}
                spellCheck={false}
                className="mt-1 w-full font-mono text-xs"
              />
            </div>
          </div>
        </Modal>
      )}

      {dialog}
    </div>
  );
}
