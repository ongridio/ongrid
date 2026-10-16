import { useState, type ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import {
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  Circle,
  Copy,
  Database,
  Search,
  Sparkles,
  Workflow,
  Wrench,
  User,
  type LucideIcon,
} from 'lucide-react';
import type { AgentContent } from '@/api/agent-observability';
import { Button, Chip } from '@/components/ui';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/DropdownMenu';
import { useI18n } from '@/i18n/locale';
import { contentStateLabel, messageContent, pretty } from './model';
import {
  contentFormats,
  structuredContent,
  serializeContent,
  type ContentFormat,
} from './content-format';

export function KindIcon({ kind, size = 14 }: { kind: string; size?: number }) {
  const icons: Record<string, LucideIcon> = {
    agent: Bot,
    workflow: Workflow,
    llm: Sparkles,
    tool: Wrench,
    retriever: Search,
    embedding: Database,
  };
  const Icon = icons[kind] || Circle;
  return (
    <span className="agent-kind-icon" data-kind={kind}>
      <Icon size={size} aria-hidden />
    </span>
  );
}

export function RunStatus({
  status,
  errors = 0,
}: {
  status: string;
  errors?: number;
}) {
  const { tr } = useI18n();
  if (status !== 'error' && errors === 0) return null;
  return (
    <Chip
      tone={status === 'error' ? 'danger' : 'warning'}
      className="shrink-0"
      title={status !== 'error'
        ? tr(
            '入口未报告失败，但包含失败步骤',
            'Entry did not report an error; contains failed steps',
          )
        : undefined}
    >
      {status === 'error'
        ? tr('失败', 'Failed')
        : tr(`${errors} 个失败步骤`, `${errors} failed ${errors === 1 ? 'step' : 'steps'}`)}
    </Chip>
  );
}

export function CopyButton({ value, label }: { value: string; label: string }) {
  const { tr } = useI18n();
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle');
  return (
    <Button
      size="sm"
      variant="subtle"
      aria-label={label}
      title={label}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setState('copied');
        } catch {
          setState('failed');
        }
      }}
    >
      {state === 'copied' ? <Check size={13} /> : <Copy size={13} />}
      {state === 'copied' && tr('已复制', 'Copied')}
      {state === 'failed' && tr('复制失败', 'Copy failed')}
    </Button>
  );
}

export function CodeValue({ value }: { value: unknown }) {
  const { tr } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const text = pretty(value);
  const shortened = !expanded && text.length > 12000;
  return (
    <div className="min-w-0">
      <pre className="agent-code">
        {shortened ? `${text.slice(0, 12000)}\n…` : text}
      </pre>
      {shortened && (
        <Button size="sm" className="mt-2" onClick={() => setExpanded(true)}>
          {tr('展开完整内容', 'Expand full content')} ·{' '}
          {text.length.toLocaleString()}
        </Button>
      )}
    </div>
  );
}

function ReadableValue({
  value,
  format,
}: {
  value: unknown;
  format: 'markdown' | 'plain';
}) {
  const { tr } = useI18n();
  const [expanded, setExpanded] = useState(false);
  if (typeof value !== 'string') {
    if (Array.isArray(value))
      return (
        <div className="space-y-3">
          {value.slice(0, expanded ? undefined : 30).map((part, index) => {
            const text =
              part &&
              typeof part === 'object' &&
              ['text', 'input_text', 'output_text'].includes(part.type)
                ? (part.text ?? part.content)
                : part;
            return <ReadableValue key={index} value={text} format={format} />;
          })}
          {!expanded && value.length > 30 && (
            <Button size="sm" onClick={() => setExpanded(true)}>
              {tr('显示全部内容', 'Show all content')} ({value.length})
            </Button>
          )}
        </div>
      );
    return <CodeValue value={value} />;
  }
  if (format === 'plain') return <CodeValue value={value} />;
  const shortened = !expanded && value.length > 12000;
  return (
    <div className="min-w-0">
      <div className="agent-prose">
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          components={{
            img: ({ alt }) => (
              <span className="text-text-muted">
                [{tr('图片', 'Image')}: {alt || tr('未加载', 'not loaded')}]
              </span>
            ),
            a: ({ children, href }) => (
              <a href={href} target="_blank" rel="noopener noreferrer">
                {children}
              </a>
            ),
          }}
        >
          {shortened ? `${value.slice(0, 12000)}\n…` : value}
        </ReactMarkdown>
      </div>
      {shortened && (
        <Button size="sm" className="mt-2" onClick={() => setExpanded(true)}>
          {tr('展开完整内容', 'Expand full content')} ·{' '}
          {value.length.toLocaleString()}
        </Button>
      )}
    </div>
  );
}

export function ContentView({
  content,
  title,
  expanded,
  onExpandedChange,
}: {
  content: AgentContent;
  title: string;
  expanded?: boolean;
  onExpandedChange?(open: boolean): void;
}) {
  const { tr } = useI18n();
  const [allMessages, setAllMessages] = useState(false);
  const [open, setOpen] = useState(true);
  const [format, setFormat] = useState<ContentFormat>('markdown');
  const value = format === 'plain' || content.truncated
    ? content.value
    : structuredContent(content.value);
  const { messages, fields } = messageContent(value);
  const isOpen = expanded ?? open;
  const serialized = format === 'json' || format === 'yaml';
  const roleLabels: Record<string, string> = {
    user: tr('用户', 'User'),
    assistant: tr('助手', 'Assistant'),
    system: tr('系统', 'System'),
    tool: tr('工具', 'Tool'),
    developer: tr('开发者', 'Developer'),
  };
  return (
    <InspectorSection
      title={title}
      open={isOpen}
      onToggle={() =>
        onExpandedChange ? onExpandedChange(!isOpen) : setOpen(!isOpen)
      }
      count={messages?.length}
      actions={
        content.state === 'available' && (
          <div className="flex items-center gap-1">
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="subtle" size="sm" />}
                aria-label={tr(`${title}格式`, `${title} format`)}
              >
                {contentFormats.find((item) => item.value === format)?.label}
                <ChevronDown size={12} aria-hidden />
              </DropdownMenuTrigger>
              <DropdownMenuContent
                align="end"
                aria-label={tr(`${title}格式`, `${title} format`)}
              >
                {contentFormats.map((item) => (
                  <DropdownMenuItem
                    key={item.value}
                    onClick={() => setFormat(item.value)}
                  >
                    <span className="min-w-20 flex-1">{item.label}</span>
                    {format === item.value && (
                      <Check size={13} aria-label={tr('已选择', 'Selected')} />
                    )}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
            <CopyButton
              value={
                format === 'yaml'
                  ? serializeContent(value, 'yaml')
                  : serialized
                    ? serializeContent(value, 'json')
                    : pretty(value)
              }
              label={
                content.truncated
                  ? tr(`复制${title}预览`, `Copy ${title} preview`)
                  : tr(`复制${title}`, `Copy ${title}`)
              }
            />
          </div>
        )
      }
    >
      {content.state !== 'available' ? (
        <p className="py-4 text-xs text-text-muted">
          {content.state !== 'not_recorded'
            ? contentStateLabel(content.state, tr)
            : tr(
                'SDK 未提供这部分内容，无法判断是否未采集或已脱敏。',
                'The SDK did not provide this content. Capture and redaction status are unknown.',
              )}
        </p>
      ) : serialized ? (
        <CodeValue value={serializeContent(value, format)} />
      ) : messages ? (
        <div className="agent-messages">
          {messages
            .slice(0, allMessages ? undefined : 30)
            .map((message, index) => (
              <div
                className="agent-message"
                key={index}
                data-role={String(message.role)}
              >
                <div className="agent-message-heading">
                  {message.role === 'user' ? (
                    <User size={13} />
                  ) : message.role === 'assistant' ? (
                    <Bot size={13} />
                  ) : message.role === 'tool' ? (
                    <Wrench size={13} />
                  ) : (
                    <Circle size={13} />
                  )}
                  <span className="agent-message-role">
                    {roleLabels[String(message.role)] || String(message.role)}
                  </span>
                  {typeof message.name === 'string' && (
                    <span className="min-w-0 truncate" title={message.name}>
                      {message.name}
                    </span>
                  )}
                  <span className="ml-auto shrink-0">
                    <CopyButton
                      value={pretty(
                        message.content ?? message.parts ?? message,
                      )}
                      label={tr(
                        `复制消息 ${index + 1}`,
                        `Copy message ${index + 1}`,
                      )}
                    />
                  </span>
                </div>
                <ReadableValue
                  value={message.content ?? message.parts ?? message}
                  format={format}
                />
                {Object.keys(message).some(
                  (key) => !['role', 'content', 'parts', 'name'].includes(key),
                ) && (
                  <details className="agent-additional-fields">
                    <summary>{tr('附加字段', 'Additional fields')}</summary>
                    <AttributeTable
                      values={Object.fromEntries(
                        Object.entries(message).filter(
                          ([key]) =>
                            !['role', 'content', 'parts', 'name'].includes(key),
                        ),
                      )}
                    />
                  </details>
                )}
              </div>
            ))}
          {!allMessages && messages.length > 30 && (
            <Button size="sm" onClick={() => setAllMessages(true)}>
              {tr('显示全部消息', 'Show all messages')} ({messages.length})
            </Button>
          )}
        </div>
      ) : typeof value === 'object' ? (
        <CodeValue value={value} />
      ) : (
        <ReadableValue value={value} format={format} />
      )}
      {content.truncated && (
        <p className="mt-2 text-xs text-text-muted">
          {tr(
            '采集时已截断，仅显示预览。',
            'Truncated during capture. Only a preview was retained.',
          )}
          {content.original_bytes != null && ` ${tr('原始大小', 'Original size')}: ${content.original_bytes.toLocaleString()} B`}
        </p>
      )}
      {content.omitted && content.omitted.length > 0 && (
        <p className="mt-2 text-xs text-text-muted">
          {tr('采集时已处理', 'Capture policy applied')}: {content.omitted.map((reason) => ({
            system_prompt: tr('系统提示词仅保留长度和哈希', 'System instructions: size and hash only'),
            earlier_messages: tr('较早消息已省略', 'Earlier messages omitted'),
            reasoning: tr('推理正文已省略', 'Reasoning content omitted'),
            media: tr('媒体内容已省略', 'Media omitted'),
            credentials: tr('凭据已脱敏', 'Credentials masked'),
            depth_limit: tr('过深结构已省略', 'Deeply nested content omitted'),
          }[reason] || reason)).join(tr('；', '; '))}
        </p>
      )}
      {!serialized && fields && Object.keys(fields).length > 0 && (
        <details className="agent-additional-fields">
          <summary>{tr('附加字段', 'Additional fields')}</summary>
          <AttributeTable values={fields} />
        </details>
      )}
      {content.source && (
        <div className="agent-content-source">
          {tr('来源', 'Source')}{' '}
          <span className="font-mono">{content.source}</span>
        </div>
      )}
    </InspectorSection>
  );
}

export function InspectorSection({
  title,
  open,
  onToggle,
  actions,
  count,
  children,
}: {
  title: string;
  open: boolean;
  onToggle(): void;
  actions?: ReactNode;
  count?: number;
  children: ReactNode;
}) {
  return (
    <section className="agent-content-block" aria-label={title}>
      <div className="agent-content-heading">
        <Button
          variant="subtle"
          size="sm"
          aria-expanded={open}
          onClick={onToggle}
        >
          {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
          <span className="font-medium">{title}</span>
          {count != null && <span className="agent-tab-count">{count}</span>}
        </Button>
        {actions}
      </div>
      {open && <div className="agent-content-body">{children}</div>}
    </section>
  );
}

export function AttributeTable({
  values,
}: {
  values: Record<string, unknown>;
}) {
  const { tr } = useI18n();
  if (Object.keys(values).length === 0)
    return (
      <p className="agent-attribute-empty">
        {tr('未上报字段', 'No fields reported')}
      </p>
    );
  return (
    <dl className="agent-attribute-table">
      {Object.entries(values).map(([key, value]) => (
        <div key={key}>
          <dt>{key}</dt>
          <dd>
            <div className="min-w-0 flex-1">
              {(value !== null && typeof value === 'object') ||
              (typeof value === 'string' &&
                (value.length > 160 || value.includes('\n'))) ? (
                <details>
                  <summary>
                    {typeof value === 'string'
                      ? `${value.split('\n', 1)[0].slice(0, 80)}…`
                      : Array.isArray(value)
                        ? tr(`${value.length} 项`, `${value.length} items`)
                        : tr(
                            `${Object.keys(value as object).length} 个字段`,
                            `${Object.keys(value as object).length} fields`,
                          )}
                  </summary>
                  <CodeValue value={value} />
                </details>
              ) : (
                <CodeValue value={value} />
              )}
            </div>
            <CopyButton
              value={pretty(value)}
              label={tr(`复制 ${key}`, `Copy ${key}`)}
            />
          </dd>
        </div>
      ))}
    </dl>
  );
}
