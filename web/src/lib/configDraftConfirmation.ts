type ConfigDraftLike = {
  domain?: string;
  action?: string;
  payload?: unknown;
  apply_tool?: string;
  draft_hash?: string;
};

type TrFn = (zh: string, en: string) => string;

export function configDraftApplyTool(draft: ConfigDraftLike): string {
  return draft.apply_tool || 'apply_config_change';
}

export function buildConfigDraftConfirmMessage(draft: ConfigDraftLike, tr: TrFn): string {
  const applyTool = configDraftApplyTool(draft);
  const draftHash = draft.draft_hash || '';
  return [
    tr('确认应用这个配置草案。', 'Confirm applying this configuration draft.'),
    `domain: ${draft.domain || 'config'}`,
    `action: ${draft.action || 'apply'}`,
    draftHash ? `draft_hash: ${draftHash}` : '',
    `apply_tool: ${applyTool}`,
    tr(
      '请调用 apply_config_change，传 confirmed=true、domain=alert_rule、action=create、上方 draft_hash 和下方原始 payload，创建这条告警规则；不要改写 payload。',
      'Call apply_config_change with confirmed=true, domain=alert_rule, action=create, the draft_hash above, and the exact payload below; do not rewrite the payload.',
    ),
    'payload:',
    '```json',
    JSON.stringify(draft.payload ?? {}, null, 2),
    '```',
  ].filter(Boolean).join('\n');
}

export function isConfigDraftConfirmationMessage(content: string): boolean {
  const text = content.trim();
  return (
    (
      text.startsWith('确认应用这个配置草案。') ||
      text.startsWith('Confirm applying this configuration draft.')
    ) &&
    text.includes('apply_config_change') &&
    text.includes('draft_hash:') &&
    text.includes('payload:') &&
    text.includes('```json')
  );
}
