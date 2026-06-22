import { request } from './client';

// MCP servers API client — talks to /v1/mcp/servers/* (HLD-018 P3).
// The backend handler lives at internal/manager/server/mcp/http.go; the
// model is internal/manager/model/mcp.Server.
//
// Wire shapes (mind the asymmetry — same trap as marketplace.ts):
//   - create/update accept a snake_case-ish editable subset via `serverInput`
//     (json tags: name / transport / endpoint / command / args_json /
//     credential / header_template_json / trusted / enabled).
//   - list/get return model.Server WITHOUT json tags → Go-style PascalCase
//     (ID / Name / Transport / Endpoint / Credential / HeaderTemplateJSON /
//     Trusted / Enabled / Status / LastError / ToolsCacheJSON).
//   - test returns { tools: Tool[], count } with json-tagged Tool
//     (name / description / inputSchema).
//
// We normalise the server shape into a flat snake_case TS type, accepting
// either PascalCase (real server) or snake_case (fixtures / future json
// tags), exactly like marketplace.ts/normalisePack.

export interface McpServer {
  id: number;
  name: string;
  transport: 'http' | 'stdio';
  endpoint: string;
  /** credential-vault NAME whose fields fill the header template; '' = none. */
  credential: string;
  /** JSON map[string]string of HTTP headers with {{field}} placeholders. */
  header_template: string;
  /** Trusted servers skip the human-approval gate on tool calls. */
  trusted: boolean;
  enabled: boolean;
  /** last probe outcome: 'ok' | 'error' | '' (never probed). */
  status: string;
  last_error: string;
  /** JSON-encoded []McpTool snapshot from the last successful probe. */
  tools_cache: string;
}

export interface McpTool {
  name: string;
  description: string;
}

/** The editable subset accepted on create / update. */
export interface McpServerInput {
  name: string;
  transport: 'http' | 'stdio';
  endpoint: string;
  credential: string;
  header_template: string;
  trusted: boolean;
  enabled: boolean;
}

// ---------- internal wire shapes ------------------------------------------

type RawMcpServer = {
  ID?: number;
  id?: number;
  Name?: string;
  name?: string;
  Transport?: string;
  transport?: string;
  Endpoint?: string;
  endpoint?: string;
  Credential?: string;
  credential?: string;
  HeaderTemplateJSON?: string;
  header_template_json?: string;
  header_template?: string;
  Trusted?: boolean;
  trusted?: boolean;
  Enabled?: boolean;
  enabled?: boolean;
  Status?: string;
  status?: string;
  LastError?: string;
  last_error?: string;
  ToolsCacheJSON?: string;
  tools_cache_json?: string;
  tools_cache?: string;
};

function pick<T>(...candidates: Array<T | undefined | null>): T | undefined {
  for (const c of candidates) {
    if (c !== undefined && c !== null) return c;
  }
  return undefined;
}

function normaliseServer(raw: RawMcpServer): McpServer {
  const transport = pick(raw.transport, raw.Transport) ?? 'http';
  return {
    id: pick(raw.id, raw.ID) ?? 0,
    name: pick(raw.name, raw.Name) ?? '',
    transport: transport === 'stdio' ? 'stdio' : 'http',
    endpoint: pick(raw.endpoint, raw.Endpoint) ?? '',
    credential: pick(raw.credential, raw.Credential) ?? '',
    header_template:
      pick(raw.header_template, raw.header_template_json, raw.HeaderTemplateJSON) ?? '',
    trusted: pick(raw.trusted, raw.Trusted) ?? false,
    enabled: pick(raw.enabled, raw.Enabled) ?? false,
    status: pick(raw.status, raw.Status) ?? '',
    last_error: pick(raw.last_error, raw.LastError) ?? '',
    tools_cache: pick(raw.tools_cache, raw.tools_cache_json, raw.ToolsCacheJSON) ?? '',
  };
}

/** Map the flat TS input to the backend's create/update json body. */
function toWire(input: McpServerInput): Record<string, unknown> {
  return {
    name: input.name,
    transport: input.transport,
    endpoint: input.endpoint,
    credential: input.credential,
    header_template_json: input.header_template,
    trusted: input.trusted,
    enabled: input.enabled,
  };
}

/** Parse the tools_cache JSON string into a tool list (best-effort). */
export function parseToolsCache(raw: string): McpTool[] {
  if (!raw) return [];
  try {
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr)) return [];
    return arr
      .filter((t) => t && typeof t === 'object')
      .map((t) => ({
        name: String((t as Record<string, unknown>).name ?? (t as Record<string, unknown>).Name ?? ''),
        description: String(
          (t as Record<string, unknown>).description ?? (t as Record<string, unknown>).Description ?? '',
        ),
      }));
  } catch {
    return [];
  }
}

// ---------- public surface ------------------------------------------------

type ListResp = {
  items?: RawMcpServer[] | null;
  Items?: RawMcpServer[] | null;
  total?: number;
  Total?: number;
};

export async function listMcpServers(): Promise<McpServer[]> {
  const r = await request<ListResp>('GET', '/mcp/servers');
  const items = r.items ?? r.Items ?? [];
  return items.map(normaliseServer);
}

export async function createMcpServer(input: McpServerInput): Promise<McpServer> {
  const raw = await request<RawMcpServer>('POST', '/mcp/servers', toWire(input));
  return normaliseServer(raw);
}

export async function updateMcpServer(id: number, input: McpServerInput): Promise<void> {
  await request<{ ok: boolean }>('PUT', `/mcp/servers/${id}`, toWire(input));
}

export async function deleteMcpServer(id: number): Promise<void> {
  await request<void>('DELETE', `/mcp/servers/${id}`);
}

type TestResp = {
  tools?: McpTool[] | null;
  Tools?: McpTool[] | null;
  count?: number;
  Count?: number;
};

export async function testMcpServer(id: number): Promise<McpTool[]> {
  const r = await request<TestResp>('POST', `/mcp/servers/${id}/test`);
  const tools = r.tools ?? r.Tools ?? [];
  return tools.map((t) => ({
    name: t.name ?? '',
    description: t.description ?? '',
  }));
}
