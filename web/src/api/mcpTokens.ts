import { request } from './client';

export interface McpToken {
  public_id: string;
  name: string;
  scopes: string[];
  created_at: string;
  expires_at: string | null;
}
export interface McpTokenList {
  items: McpToken[];
  total: number;
  scopes: string[];
  enabled: boolean;
  writes_enabled: boolean;
  endpoint: string;
}
export interface CreateMcpToken {
  name: string;
  scopes: string[];
  expires_at: string | null;
}
export const listMcpTokens = (page = 0) => request<McpTokenList>('GET', `/iam/mcp-tokens?limit=20&offset=${page * 20}`);
export const createMcpToken = (input: CreateMcpToken) => request<{ item: McpToken; token: string }>('POST', '/iam/mcp-tokens', input);
export const updateMcpToken = (id: string, input: CreateMcpToken) => request<{ item: McpToken }>('PUT', `/iam/mcp-tokens/${encodeURIComponent(id)}`, input);
export const deleteMcpToken = (id: string) => request<void>('DELETE', `/iam/mcp-tokens/${encodeURIComponent(id)}`);
