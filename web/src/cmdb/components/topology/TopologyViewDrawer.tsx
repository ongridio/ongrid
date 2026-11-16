import { useState } from 'react';

import { Button } from '@/components/ui';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { Select } from '@/components/ui/Select';
import { Textarea } from '@/components/ui/Textarea';
import { useI18n } from '@/i18n/locale';

import { describeError } from '../../api/http';
import type { CIType } from '../../api/citype';
import {
  createTopologyView,
  previewTopologyGraph,
  updateTopologyView,
  type TopologyGroup,
  type TopologyGraph,
  type TopologyInput,
  type TopologyView,
} from '../../api/topology';
import { Drawer } from '../Drawer';

// The view form.
//
// THE PATH IS EDITED AS JSON, not as a nested picker. A path is a small object
// — `{"1":[switch],"-1":[rack]}` — and the model graph that would drive a picker
// is a second network round trip for a field most operators copy from another
// view. The JSON is validated before anything is sent, and the preview shows
// what it assembles.

type Props = {
  view: TopologyView | null;
  groups: TopologyGroup[];
  models: CIType[];
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
};

function parsePath(raw: string): Record<string, number[]> {
  const parsed: unknown = JSON.parse(raw || '{}');
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('path must be an object');
  }
  const out: Record<string, number[]> = {};
  for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
    const level = Number(key);
    if (!Number.isInteger(level) || level === 0) throw new Error(`invalid level ${key}`);
    if (!Array.isArray(value) || value.some((item) => !Number.isInteger(Number(item)) || Number(item) <= 0)) {
      throw new Error(`level ${key} must be a list of model ids`);
    }
    out[key] = value.map((item) => Number(item));
  }
  return out;
}

export function TopologyViewDrawer({ view, groups, models, onClose, onSaved }: Props) {
  const { tr } = useI18n();
  const [name, setName] = useState(view?.name ?? '');
  const [groupID, setGroupID] = useState(view?.group_id ? String(view.group_id) : '');
  const [centralType, setCentralType] = useState(view ? String(view.central_node_type) : '');
  const [instances, setInstances] = useState(view?.central_node_instances ?? '');
  const [pathText, setPathText] = useState(JSON.stringify(view?.path ?? {}, null, 2));
  const [preview, setPreview] = useState<TopologyGraph | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const buildInput = (): TopologyInput | null => {
    if (!name.trim()) {
      setErr(tr('名称不能为空', 'A name is required'));
      return null;
    }
    if (!centralType) {
      setErr(tr('先选择中心模型', 'Choose the central model first'));
      return null;
    }
    let path: Record<string, number[]>;
    try {
      path = parsePath(pathText);
    } catch {
      setErr(tr('路径必须是 {层级: [模型 id]} 的 JSON 对象', 'The path must be a JSON object of level → model ids'));
      return null;
    }
    return {
      name: name.trim(),
      group_id: groupID ? Number(groupID) : undefined,
      central_node_type: Number(centralType),
      central_node_instances: instances.trim(),
      path,
    };
  };

  const runPreview = async () => {
    setErr(null);
    const input = buildInput();
    if (!input) return;
    setPreviewing(true);
    try {
      setPreview(await previewTopologyGraph(input));
    } catch (error) {
      setErr(describeError(error, tr('预览失败', 'Could not preview it')));
    } finally {
      setPreviewing(false);
    }
  };

  const submit = async () => {
    setErr(null);
    const input = buildInput();
    if (!input) return;
    setSubmitting(true);
    try {
      if (view) await updateTopologyView(view.id, input);
      else await createTopologyView(input);
      await onSaved(view ? tr('已保存', 'Saved') : tr('已创建', 'Created'));
    } catch (error) {
      setErr(describeError(error, tr('保存失败', 'Could not save')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Drawer
      open
      onClose={onClose}
      size="lg"
      title={view ? tr('编辑拓扑视图', 'Edit topology view') : tr('新建拓扑视图', 'New topology view')}
      description={tr(
        '视图保存的是一次遍历：中心模型、根实例查询，以及向下 / 向上的关系层级。',
        'A view saves a walk: a central model, a root query, and the relation levels below and above.',
      )}
      footer={
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="outline" type="button" onClick={() => void runPreview()} disabled={previewing || submitting}>
            {previewing ? tr('预览中…', 'Previewing…') : tr('预览', 'Preview')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting}>
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      {err && (
        <div role="alert" className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}
      {preview && (
        <p role="status" className="mb-3 rounded-md border border-indigo-500/40 bg-indigo-500/5 px-3 py-2 text-xs text-indigo-300">
          {tr(`预览：${preview.nodes?.length ?? 0} 个节点，${preview.links?.length ?? 0} 条边`,
            `Preview: ${preview.nodes?.length ?? 0} nodes, ${preview.links?.length ?? 0} links`)}
        </p>
      )}

      <div className="flex flex-col gap-3">
        <div>
          <Label htmlFor="topo-view-name">{tr('名称', 'Name')}</Label>
          <Input id="topo-view-name" value={name} onChange={(event) => setName(event.target.value)} className="mt-1 w-full" />
        </div>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <Label htmlFor="topo-view-group">{tr('分组', 'Group')}</Label>
            <Select
              id="topo-view-group"
              label={tr('分组', 'Group')}
              value={groupID}
              onValueChange={setGroupID}
              options={[
                { value: '', label: tr('未分组', 'Ungrouped') },
                ...groups.map((group) => ({ value: String(group.id), label: group.name })),
              ]}
              className="mt-1 w-full"
            />
          </div>
          <div>
            <Label htmlFor="topo-view-central">{tr('中心模型', 'Central model')}</Label>
            <Select
              id="topo-view-central"
              label={tr('中心模型', 'Central model')}
              value={centralType}
              onValueChange={setCentralType}
              options={models.map((model) => ({ value: String(model.id), label: model.alias || model.name }))}
              searchable
              className="mt-1 w-full"
            />
          </div>
        </div>

        <div>
          <Label htmlFor="topo-view-instances">{tr('根实例查询', 'Root instances')}</Label>
          <Input
            id="topo-view-instances"
            value={instances}
            onChange={(event) => setInstances(event.target.value)}
            placeholder="hostname:web*"
            className="mt-1 w-full font-mono"
          />
          <p className="mt-1 text-[11px] text-text-faint">
            {tr(
              '查询语言表达式，或逗号分隔的实例 id。留空表示中心模型的全部实例（最多 200 个）。',
              'A query expression, or comma-separated instance ids. Empty means every instance of the central model (at most 200).',
            )}
          </p>
        </div>

        <div>
          <Label htmlFor="topo-view-path">{tr('路径 JSON', 'Path JSON')}</Label>
          <Textarea
            id="topo-view-path"
            aria-label={tr('路径 JSON', 'Path JSON')}
            value={pathText}
            onChange={(event) => setPathText(event.target.value)}
            spellCheck={false}
            className="mt-1 min-h-[9rem] w-full font-mono text-xs"
          />
          <p className="mt-1 text-[11px] text-text-faint">
            {tr(
              '键是层级（正数向下、负数向上，绝对值 ≤ 5），值是该层允许的模型 id。示例：{"1":[2],"-1":[4]}',
              'Keys are levels (positive downward, negative upward, absolute value ≤ 5); values are the model ids allowed at that level. Example: {"1":[2],"-1":[4]}',
            )}
          </p>
        </div>
      </div>
    </Drawer>
  );
}
