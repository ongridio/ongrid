import { useCallback, useEffect, useMemo, useState } from 'react';
import { ArrowDown, ArrowUp, Pencil, Trash2, Users } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Checkbox } from '@/components/ui/Checkbox';
import { Chip } from '@/components/ui/Chip';
import { Hint } from '@/components/ui/Tooltip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

import { listCITypes, type CIType } from '../../api/citype';
import { describeError } from '../../api/http';
import {
  createModelGroup,
  deleteModelGroup,
  listModelGroups,
  reorderModelGroups,
  updateModelGroup,
  type ModelGroup,
} from '../../api/preferences';
import { useToast } from '../Toast';

// 模型分组 — the shared sidebar grouping, not a per-user preference.
//
// A GROUP IS ADMIN-OWNED because everyone sees it. The panel reuses the
// existing group endpoints; the only local state is the edit form. Reordering
// uses buttons rather than drag-and-drop: the page has four other tabs and a
// drag dependency for one list is not worth the bundle.

export function ModelGroupsPanel() {
  const { tr } = useI18n();
  const toast = useToast();
  const { isAdmin } = usePermissions();
  const { confirmAction, dialog } = useDialogs();

  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [models, setModels] = useState<CIType[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [newName, setNewName] = useState('');
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<{ id: number; name: string } | null>(null);
  const [assigning, setAssigning] = useState<ModelGroup | null>(null);
  const [selected, setSelected] = useState<number[]>([]);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const [gotGroups, gotModels] = await Promise.all([listModelGroups(), listCITypes()]);
      setGroups(gotGroups);
      setModels(gotModels);
    } catch (error) {
      setErr(describeError(error, tr('无法读取模型分组', 'Could not load the model groups')));
    } finally {
      setLoading(false);
    }
  }, [tr]);

  useEffect(() => {
    void load();
  }, [load]);

  const realGroups = useMemo(() => groups.filter((group) => !group.is_other && group.id), [groups]);
  const adminTip = isAdmin ? undefined : tr('需要管理员权限', 'Administrator role required');

  const create = async () => {
    const name = newName.trim();
    if (!name || creating) return;
    setCreating(true);
    setErr(null);
    try {
      await createModelGroup(name);
      setNewName('');
      toast.success(tr('已创建分组', 'Group created'));
      await load();
    } catch (error) {
      setErr(describeError(error, tr('创建分组失败', 'Could not create the group')));
    } finally {
      setCreating(false);
    }
  };

  const rename = async () => {
    if (!editing || !editing.name.trim() || saving) return;
    setSaving(true);
    setErr(null);
    try {
      await updateModelGroup(editing.id, { name: editing.name.trim() });
      setEditing(null);
      toast.success(tr('已重命名', 'Renamed'));
      await load();
    } catch (error) {
      setErr(describeError(error, tr('重命名失败', 'Could not rename the group')));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (group: ModelGroup) => {
    if (!group.id) return;
    const ok = await confirmAction(
      tr(`删除分组「${group.name}」？组内模型不会被删除。`, `Delete the group “${group.name}”? Its models are not deleted.`),
    );
    if (!ok) return;
    try {
      await deleteModelGroup(group.id);
      toast.success(tr('已删除', 'Deleted'));
      await load();
    } catch (error) {
      setErr(describeError(error, tr('删除失败', 'Could not delete the group')));
    }
  };

  const move = async (index: number, direction: -1 | 1) => {
    const target = index + direction;
    if (target < 0 || target >= realGroups.length) return;
    const next = [...realGroups];
    [next[index], next[target]] = [next[target], next[index]];
    try {
      await reorderModelGroups(next.map((group) => group.id as number));
      await load();
    } catch (error) {
      setErr(describeError(error, tr('保存顺序失败', 'Could not save the order')));
    }
  };

  const openAssign = (group: ModelGroup) => {
    setAssigning(group);
    setSelected((group.ci_types ?? []).map((model) => model.id));
  };

  const saveAssign = async () => {
    if (!assigning?.id || saving) return;
    setSaving(true);
    setErr(null);
    try {
      await updateModelGroup(assigning.id, { type_ids: selected });
      setAssigning(null);
      toast.success(tr('已保存分组', 'Group saved'));
      await load();
    } catch (error) {
      setErr(describeError(error, tr('保存分组失败', 'Could not save the group')));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-48 flex-1">
            <Label htmlFor="cmdb-group-name">{tr('新分组名称', 'New group name')}</Label>
            <Input
              id="cmdb-group-name"
              value={newName}
              onChange={(event) => setNewName(event.target.value)}
              placeholder={tr('例如：网络设备', 'For example: network devices')}
              className="mt-1"
              disabled={!isAdmin || creating}
            />
          </div>
          <Hint content={adminTip}>
            <Button
              variant="primary"
              type="button"
              onClick={() => void create()}
              disabled={!isAdmin || creating || !newName.trim()}
            >
              {creating ? tr('创建中…', 'Creating…') : tr('新建分组', 'New group')}
            </Button>
          </Hint>
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
      ) : groups.length === 0 ? (
        <Card>
          <p className="py-6 text-center text-xs text-text-muted">{tr('还没有分组', 'No groups yet')}</p>
        </Card>
      ) : (
        groups.map((group) => {
          const realIndex = group.is_other || !group.id ? -1 : realGroups.findIndex((item) => item.id === group.id);
          return (
            <Card key={group.id ?? `other-${group.name}`}>
              <div className="mb-2 flex flex-wrap items-center gap-2">
                {editing && editing.id === group.id ? (
                  <div className="flex min-w-0 flex-1 flex-wrap items-end gap-2">
                    <div className="min-w-40 flex-1">
                      <Label htmlFor={`cmdb-group-rename-${group.id}`}>{tr('分组名称', 'Group name')}</Label>
                      <Input
                        id={`cmdb-group-rename-${group.id}`}
                        value={editing.name}
                        onChange={(event) => setEditing({ id: editing.id, name: event.target.value })}
                        className="mt-1"
                      />
                    </div>
                    <Button variant="primary" size="sm" type="button" onClick={() => void rename()} disabled={saving}>
                      {tr('保存', 'Save')}
                    </Button>
                    <Button variant="outline" size="sm" type="button" onClick={() => setEditing(null)} disabled={saving}>
                      {tr('取消', 'Cancel')}
                    </Button>
                  </div>
                ) : (
                  <>
                    <h2 className="text-sm font-medium text-text">
                      {group.is_other ? tr('其他', 'Other') : group.name || tr('未命名分组', 'Untitled group')}
                    </h2>
                    <Chip>{tr(`${group.ci_types.length} 个模型`, `${group.ci_types.length} models`)}</Chip>
                    {!group.is_other && group.id && (
                      <div className="ms-auto flex flex-wrap items-center gap-1">
                        <Button
                          variant="subtle"
                          size="sm"
                          type="button"
                          onClick={() => void move(realIndex, -1)}
                          disabled={!isAdmin || realIndex <= 0}
                          aria-label={tr(`上移 ${group.name}`, `Move ${group.name} up`)}
                        >
                          <ArrowUp size={12} aria-hidden="true" />
                        </Button>
                        <Button
                          variant="subtle"
                          size="sm"
                          type="button"
                          onClick={() => void move(realIndex, 1)}
                          disabled={!isAdmin || realIndex >= realGroups.length - 1}
                          aria-label={tr(`下移 ${group.name}`, `Move ${group.name} down`)}
                        >
                          <ArrowDown size={12} aria-hidden="true" />
                        </Button>
                        <Hint content={adminTip}>
                          <Button
                            variant="subtle"
                            size="sm"
                            type="button"
                            onClick={() => setEditing({ id: group.id as number, name: group.name ?? '' })}
                            disabled={!isAdmin}
                            aria-label={tr(`重命名 ${group.name}`, `Rename ${group.name}`)}
                          >
                            <Pencil size={12} aria-hidden="true" />
                          </Button>
                        </Hint>
                        <Hint content={adminTip}>
                          <Button
                            variant="subtle"
                            size="sm"
                            type="button"
                            onClick={() => openAssign(group)}
                            disabled={!isAdmin}
                            aria-label={tr(`分配模型 ${group.name}`, `Assign models to ${group.name}`)}
                          >
                            <Users size={12} aria-hidden="true" />
                          </Button>
                        </Hint>
                        <Hint content={adminTip}>
                          <Button
                            variant="dangerGhost"
                            size="sm"
                            type="button"
                            onClick={() => void remove(group)}
                            disabled={!isAdmin}
                            aria-label={tr(`删除 ${group.name}`, `Delete ${group.name}`)}
                          >
                            <Trash2 size={12} aria-hidden="true" />
                          </Button>
                        </Hint>
                      </div>
                    )}
                  </>
                )}
              </div>
              <ul className="divide-y divide-border">
                {group.ci_types.map((model) => (
                  <li key={model.id} className="flex items-center gap-2 py-1.5 text-xs">
                    <span className="min-w-0 flex-1 truncate">{model.alias || model.name}</span>
                    <span className="font-mono text-[11px] text-text-faint">{model.name}</span>
                  </li>
                ))}
              </ul>
            </Card>
          );
        })
      )}

      {assigning && (
        <Modal
          open
          onClose={() => setAssigning(null)}
          title={tr(`分配模型到「${assigning.name}」`, `Assign models to “${assigning.name}”`)}
          footer={
            <div className="flex flex-wrap justify-end gap-2">
              <Button variant="outline" type="button" onClick={() => setAssigning(null)} disabled={saving}>
                {tr('取消', 'Cancel')}
              </Button>
              <Button variant="primary" type="button" onClick={() => void saveAssign()} disabled={saving}>
                {saving ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
              </Button>
            </div>
          }
        >
          <div className="max-h-72 overflow-y-auto rounded-md border border-border">
            {models.map((model) => (
              <label
                key={model.id}
                className="flex cursor-pointer items-center gap-2 border-b border-border px-3 py-1.5 text-xs last:border-0 hover:bg-card"
              >
                <Checkbox
                  checked={selected.includes(model.id)}
                  onCheckedChange={(next) =>
                    setSelected((current) =>
                      next === true ? [...current, model.id] : current.filter((id) => id !== model.id),
                    )
                  }
                />
                <span className="min-w-0 flex-1 truncate">{model.alias || model.name}</span>
                <span className="font-mono text-[11px] text-text-faint">{model.name}</span>
              </label>
            ))}
          </div>
        </Modal>
      )}

      {dialog}
    </div>
  );
}
