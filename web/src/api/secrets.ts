import { request } from './client';

// Secrets/credentials API client — /v1/secrets/* + /v1/credential-types
// (HLD-017 credential vault). A credential is a NAMED, TYPED, MULTI-FIELD
// instance (n8n model). The TYPE declares the fields + how they inject;
// "custom" = free-form fields injected as same-named env vars. Field VALUES
// are write-only: the list returns field_keys only, never the values.

export interface SecretView {
  id: number;
  name: string;
  type: string;
  description: string;
  field_keys: string[];
  created_at: string;
  updated_at: string;
}

export interface CredField {
  key: string;
  label: string;
  secret: boolean;
}

export interface CredType {
  name: string;
  label: string;
  fields: CredField[];
  inject_env?: Record<string, string>;
  builtin: boolean;
}

export function listSecrets() {
  return request<{ items: SecretView[] }>('GET', '/secrets');
}

export function listCredentialTypes() {
  return request<{ items: CredType[] }>('GET', '/credential-types');
}

export function createSecret(input: { name: string; type: string; description?: string; fields: Record<string, string> }) {
  return request<SecretView>('POST', '/secrets', input);
}

export function updateSecret(id: number, input: { description?: string; fields?: Record<string, string> }) {
  return request<{ ok: boolean }>('PUT', `/secrets/${id}`, input);
}

export function deleteSecret(id: number) {
  return request<{ ok: boolean }>('DELETE', `/secrets/${id}`);
}
