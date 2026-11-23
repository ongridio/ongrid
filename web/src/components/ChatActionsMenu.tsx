import { useEffect, useState, type ReactNode } from 'react';
import { Bot, Check, ChevronRight, ExternalLink, Globe, Paperclip, Plug, Plus, Wrench } from 'lucide-react';
import { agentLabel, listAgents } from '@/api/agents';
import { listMcpServers } from '@/api/mcp';
import { listSkills, localizedSkill } from '@/api/skills';
import {
  Button, DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuRadioGroup,
  DropdownMenuRadioIndicator, DropdownMenuRadioItem, DropdownMenuSeparator,
  DropdownMenuSubmenu, DropdownMenuSubmenuTrigger, DropdownMenuTrigger,
} from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import type { IconType } from '@/lib/icon';

function Submenu({ label, icon: Icon, children }: { label: string; icon: IconType; children: ReactNode }) {
  return (
    <DropdownMenuSubmenu>
      <DropdownMenuSubmenuTrigger className="og-menu-item w-full data-[popup-open]:bg-text/[.07]">
        <Icon size={16} /><span>{label}</span><ChevronRight size={13} className="ml-auto text-text-muted" />
      </DropdownMenuSubmenuTrigger>
      <DropdownMenuContent side="right" sideOffset={4} aria-label={label} className="max-h-[min(20rem,var(--available-height))] w-48 rounded-xl">
        {children}
      </DropdownMenuContent>
    </DropdownMenuSubmenu>
  );
}

type Entry = { id: string; label: string };
const catalogs = {
  async experts() {
    const { items } = await listAgents();
    return (items ?? []).filter(a => a.name !== 'default').map(a => ({ id: a.name, label: agentLabel(a.name) }));
  },
  async skills() {
    const { items } = await listSkills();
    return (items ?? []).map(s => ({ id: s.key, label: localizedSkill(s).name || s.key }));
  },
  async connectors() {
    return (await listMcpServers()).filter(s => s.enabled).map(s => ({ id: s.name, label: s.name }));
  },
};

function CatalogItems({ kind, empty, onPick }: {
  kind: keyof typeof catalogs; empty: string; onPick(id: string): void;
}) {
  const { tr, locale } = useI18n();
  const [entries, setEntries] = useState<Entry[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let active = true;
    catalogs[kind]().then(items => { if (active) setEntries(items); })
      .catch(() => { if (active) setFailed(true); });
    return () => { active = false; };
  }, [kind, locale]);
  if (failed) return <DropdownMenuItem disabled>{tr('加载失败，请重新打开', 'Could not load. Reopen to retry.')}</DropdownMenuItem>;
  if (!entries?.length) return <DropdownMenuItem disabled>{entries ? empty : tr('正在加载…', 'Loading…')}</DropdownMenuItem>;
  return entries.map(({ id, label }) => (
    <DropdownMenuItem key={id} label={label} title={label} onClick={() => onPick(id)}>
      <span className="truncate">{label}</span>
    </DropdownMenuItem>
  ));
}

type Props = {
  disabled?: boolean;
  onAddFiles?: () => void;
  webSearchEnabled: boolean;
  onWebSearchToggle?: (next: boolean) => void;
  onReference: (prompt: string) => void;
};

export function ChatActionsMenu({ disabled, onAddFiles, webSearchEnabled, onWebSearchToggle, onReference }: Props) {
  const { tr } = useI18n();
  const label = tr('更多操作', 'More actions');
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="plain" size="sm" />} disabled={disabled}
        aria-label={label} title={label}
        className="group w-7 shrink-0 p-0 text-text-muted hover:text-text">
        <Plus size={18} className="transition-transform group-aria-expanded:rotate-45 motion-reduce:transition-none" />
      </DropdownMenuTrigger>
      <DropdownMenuContent aria-label={label} side="top" sideOffset={8} className="w-44 rounded-xl">
        {onAddFiles && <>
          <DropdownMenuItem onClick={onAddFiles}><Paperclip size={16} />{tr('添加文件', 'Add files')}</DropdownMenuItem>
          <DropdownMenuSeparator />
        </>}
        {onWebSearchToggle && <Submenu label={tr('模式', 'Mode')} icon={Globe}>
          <DropdownMenuRadioGroup value={webSearchEnabled} onValueChange={enabled => onWebSearchToggle(enabled)}>
            {[true, false].map(enabled => (
              <DropdownMenuRadioItem key={String(enabled)} value={enabled} closeOnClick className="og-menu-item">
                {enabled ? tr('自动联网', 'Automatic web search') : tr('关闭联网搜索', 'Web search off')}
                <DropdownMenuRadioIndicator className="ml-auto"><Check size={14} /></DropdownMenuRadioIndicator>
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </Submenu>}
        <Submenu label={tr('专家', 'Experts')} icon={Bot}>
          <CatalogItems kind="experts" empty={tr('暂无可用专家', 'No experts available')}
            onPick={id => onReference(tr(`请让专家「${id}」协助。`, `Please ask the ${id} expert to assist.`))} />
        </Submenu>
        <Submenu label={tr('技能', 'Skills')} icon={Wrench}>
          <CatalogItems kind="skills" empty={tr('暂无可用技能', 'No skills available')}
            onPick={id => onReference(tr(`请使用技能「${id}」。`, `Please use the ${id} skill.`))} />
        </Submenu>
        <Submenu label={tr('连接器', 'Connectors')} icon={Plug}>
          <CatalogItems kind="connectors" empty={tr('暂无已启用的连接器', 'No enabled connectors')}
            onPick={id => onReference(tr(`请使用连接器「${id}」。`, `Please use the ${id} connector.`))} />
          <DropdownMenuSeparator />
          <DropdownMenuItem render={<a href="/mcp" target="_blank" rel="noopener noreferrer" />}>
            {tr('查看 MCP 连接器', 'View MCP connectors')}<ExternalLink size={13} className="ml-auto text-text-muted" />
          </DropdownMenuItem>
        </Submenu>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
