import { request } from './client';
import { getToken } from '@/store/auth';
import { tr as trInline } from '@/i18n/locale';

// id is a string because qdrant point IDs are md5-derived uint64 values
// that overflow JS Number's 2^53 safe-integer range. Backend sends them
// as JSON strings (uint64 + `,string` tag) — see server/knowledge/http.go.
export type DocSource = 'manual' | 'repo' | 'url' | 'vault' | 'upload';

export type KnowledgeDoc = {
  id: string;
  source_type: DocSource;
  // repo_id is also string-encoded for the same reason (repo IDs aren't
  // currently huge but the wire type is uniform).
  repo_id?: string | null;
  url?: string;
  title: string;
  // title_en is an optional English overlay. When the SPA locale is
  // en-US and this field is non-empty, list / search / drawer / edit
  // views show title_en in place of title. Falls back to title (the
  // original language) otherwise. Lets a primarily-Chinese vault
  // stay readable in English without lossy auto-translation.
  title_en?: string;
  content?: string;
  path?: string; // "/"-separated breadcrumb, e.g. "网络/DNS"
  tags?: string[];
  created_at: string;
  updated_at: string;
};

export type KnowledgeRepo = {
  id: number;
  url: string;
  branch: string;
  description?: string;
  last_synced_at?: string | null;
  last_sync_error?: string;
  file_count: number;
  // Server-set: marks the embedded platform vault (url == builtin://vault).
  // Use isBuiltinVault() rather than substring-matching the URL — the URL
  // scheme has changed before (ongridio/vault → builtin://vault) and silently
  // broke both the Repos-list filter and the Knowledge sync button.
  is_builtin?: boolean;
  created_at: string;
  updated_at: string;
};

// isBuiltinVault is the single source of truth for "is this the built-in
// platform vault row?". Prefers the server's is_builtin flag; falls back to
// the builtin:// URL scheme (and the legacy ongridio/vault form) so it still
// works against an older manager that doesn't send the flag yet.
export function isBuiltinVault(repo: Pick<KnowledgeRepo, 'url' | 'is_builtin'>): boolean {
  if (repo.is_builtin) return true;
  const u = (repo.url ?? '').trim();
  return u.startsWith('builtin://') || u.includes('ongridio/vault');
}

export type SearchHit = { doc: KnowledgeDoc; score: number };

export type PathRow = { path: string; count: number };

export function listDocs(params?: {
  source_type?: 'manual' | 'repo';
  repo_id?: number;
  path?: string;
  path_prefix?: string;
  tag?: string;
}) {
  const q = new URLSearchParams();
  if (params?.source_type) q.set('source_type', params.source_type);
  if (params?.repo_id != null) q.set('repo_id', String(params.repo_id));
  if (params?.path) q.set('path', params.path);
  if (params?.path_prefix) q.set('path_prefix', params.path_prefix);
  if (params?.tag) q.set('tag', params.tag);
  const qs = q.toString();
  return request<{ items: KnowledgeDoc[]; total: number }>(
    'GET',
    `/knowledge/docs${qs ? `?${qs}` : ''}`,
  );
}

export function getDoc(id: string) {
  return request<KnowledgeDoc>('GET', `/knowledge/docs/${id}`);
}

export function createDoc(input: {
  title: string;
  title_en?: string;
  content: string;
  url?: string;
  path?: string;
  tags?: string[];
}) {
  return request<KnowledgeDoc>('POST', '/knowledge/docs', input);
}

export function updateDoc(
  id: string,
  input: { title: string; title_en?: string; content: string; path?: string; tags?: string[] },
) {
  return request<KnowledgeDoc>('PATCH', `/knowledge/docs/${id}`, input);
}

export function deleteDoc(id: string) {
  return request<void>('DELETE', `/knowledge/docs/${id}`);
}

// moveDoc relocates an org doc (manual/upload) into a different folder
// (ADR-029) — the drag-drop target on the 组织 tree. Path-only; "" = root.
export function moveDoc(id: string, path: string) {
  return request<KnowledgeDoc>('PATCH', `/knowledge/docs/${id}/move`, { path });
}

export function searchKnowledge(
  q: string,
  opts?: { limit?: number; path?: string; pathPrefix?: string; tags?: string[] },
) {
  const params = new URLSearchParams();
  params.set('q', q);
  params.set('limit', String(opts?.limit ?? 10));
  if (opts?.path) params.set('path', opts.path);
  if (opts?.pathPrefix) params.set('path_prefix', opts.pathPrefix);
  for (const t of opts?.tags ?? []) params.append('tag', t);
  return request<{ items: SearchHit[]; total: number }>(
    'GET',
    `/knowledge/search?${params.toString()}`,
  );
}

export function listPaths() {
  return request<{ items: PathRow[]; total: number }>('GET', '/knowledge/paths');
}

export function listRepos() {
  return request<{ items: KnowledgeRepo[]; total: number }>('GET', '/knowledge/repos');
}

export function createRepo(input: { url: string; branch?: string; description?: string }) {
  return request<KnowledgeRepo>('POST', '/knowledge/repos', input);
}

export function syncRepo(id: number) {
  return request<KnowledgeRepo>('POST', `/knowledge/repos/${id}/sync`, {});
}

// syncVault refreshes the platform vault in qdrant (ADR-029): a live clone of
// the public github vault, falling back to the embedded snapshot when github
// is unreachable. The vault is NOT a repo row (never appears in the Repos
// list), so it has its own endpoint. `source` reports which path ran:
// "cloud" (github reachable) or "embedded" (offline fallback).
export function syncVault() {
  return request<{ file_count: number; source: 'cloud' | 'embedded'; synced_at: string }>(
    'POST',
    '/knowledge/vault/sync',
    {},
  );
}

// uploadDoc ingests one org file (ADR-028) into the 组织知识库 tree
// (source_type=upload). multipart; phase-1 accepts .md / .txt. The request
// helper sets JSON headers, so we hit fetch directly with FormData here.
export async function uploadDoc(
  file: File,
  opts?: { title?: string; path?: string; tags?: string[] },
): Promise<KnowledgeDoc> {
  const fd = new FormData();
  fd.append('file', file);
  if (opts?.title) fd.append('title', opts.title);
  if (opts?.path) fd.append('path', opts.path);
  if (opts?.tags && opts.tags.length) fd.append('tags', opts.tags.join(','));
  const token = getToken();
  const res = await fetch('/api/v1/knowledge/upload', {
    method: 'POST',
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    body: fd,
  });
  if (!res.ok) {
    let msg = `upload failed (${res.status})`;
    try {
      const j = await res.json();
      msg = j.message || j.error || msg;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(msg);
  }
  return res.json();
}

export function deleteRepo(id: number) {
  return request<void>('DELETE', `/knowledge/repos/${id}`);
}

// ----- SSH identities -----
//
// One row = one stored SSH private key + the host patterns it's
// allowed to authenticate against. The private_key is write-only;
// after creation the API only ever surfaces the public_key +
// fingerprint.

export type SSHIdentity = {
  id: number;
  name: string;
  public_key: string;
  fingerprint: string;       // SHA256:xxx
  hosts: string[];           // host glob patterns
  known_hosts?: string;
  last_used_at?: string | null;
  created_at: string;
  updated_at: string;
};

export function listSSHIdentities() {
  return request<{ items: SSHIdentity[]; total: number }>('GET', '/knowledge/ssh-identities');
}

export function createSSHIdentity(input: {
  name: string;
  private_key: string;
  hosts: string[];
  known_hosts?: string;
}) {
  return request<SSHIdentity>('POST', '/knowledge/ssh-identities', input);
}

// generateSSHIdentity asks the manager to create a fresh ed25519
// keypair server-side. The private key never leaves the server; the
// admin copies the returned public_key into the host's Deploy keys.
export function generateSSHIdentity(input: {
  name: string;
  hosts: string[];
  known_hosts?: string;
}) {
  return request<SSHIdentity>('POST', '/knowledge/ssh-identities/generate', input);
}

export function updateSSHIdentity(id: number, input: {
  name: string;
  hosts: string[];
  known_hosts: string;
}) {
  return request<SSHIdentity>('PATCH', `/knowledge/ssh-identities/${id}`, input);
}

export function deleteSSHIdentity(id: number) {
  return request<void>('DELETE', `/knowledge/ssh-identities/${id}`);
}

// ----- HTTPS credentials API -----
//
// One row = one stored HTTPS PAT credential + the host patterns it
// covers + the username (e.g. "oauth2" for GitLab PAT). The token is
// write-only; after creation/update the API only surfaces has_token
// (bool). No token field in the type — token 只写不读，对齐后端 DTO.

export type HTTPSCredential = {
  id: number;
  name: string;
  hosts: string[];            // host glob patterns
  username: string;           // e.g. "oauth2"
  has_token: boolean;         // true = token is configured
  last_used_at?: string | null;
  created_at: string;
  updated_at: string;
  // Note: no `token` field — token is write-only; never echoed back by the API.
};

export function listHTTPSCredentials() {
  return request<{ items: HTTPSCredential[]; total: number }>('GET', '/knowledge/https-credentials');
}

export function createHTTPSCredential(input: {
  name: string;
  hosts: string[];
  username: string;
  token: string;
}) {
  return request<HTTPSCredential>('POST', '/knowledge/https-credentials', input);
}

export function updateHTTPSCredential(id: number, input: {
  name: string;
  hosts: string[];
  username: string;
  token: string;   // empty string = keep existing token (server nil-means-keep semantics)
}) {
  return request<HTTPSCredential>('PATCH', `/knowledge/https-credentials/${id}`, input);
}

export function deleteHTTPSCredential(id: number) {
  return request<void>('DELETE', `/knowledge/https-credentials/${id}`);
}

// ----- i18n localizer for built-in seed content -----
//
// The 38 docs currently in qdrant (seeded 2026-05-09 from the network/
// storage/scheduling/k8s playbook set) carry Chinese titles + path
// segments. They aren't truly "user content" — they ship with the
// platform — so the Knowledge UI must flip them to English when the
// SPA locale is English. Same shape as BUILTIN_SKILL_I18N / agents /
// rule names: lookup table by Chinese key returning `{zh,en}`.
//
// When the future `ongridio/vault` auto-loader replaces these seeds
// with English-source content, this table will become a thin no-op
// for those docs (unseen keys pass through) and can be retired.

// Path-segment translations — used segment-by-segment so e.g.
// "网络/conntrack" -> "Network/conntrack" works even when a subfolder
// has a Chinese parent and an English leaf.
// KNOWLEDGE_PATH_SEGMENTS keys are the folder names that actually appear
// in the data (either Chinese — legacy Chinese-named vaults — or English
// — the current ongridio/vault layout). Each entry carries both
// renderings so localizedPathSegment() can deliver the right label in
// whichever locale the user picked, regardless of the source-language
// folder name.
const KNOWLEDGE_PATH_SEGMENTS: Record<string, { zh: string; en: string }> = {
  // ongridio/vault current English-named tree
  concepts: { zh: '概念', en: 'Concepts' },
  diagnostics: { zh: '诊断 playbook', en: 'Diagnostics' },
  systems: { zh: '系统', en: 'Systems' },
  alerts: { zh: '告警', en: 'Alerts' },
  reference: { zh: '参考', en: 'Reference' },
  external: { zh: '外部资料', en: 'External' },
  index: { zh: '阅读索引', en: 'Index' },
  network: { zh: '网络', en: 'Network' },
  observability: { zh: '可观测', en: 'Observability' },
  'observability-stack': { zh: '可观测栈', en: 'Observability stack' },
  kernel: { zh: '内核', en: 'Kernel' },
  compute: { zh: '计算 / 内存', en: 'Compute' },
  kubernetes: { zh: 'Kubernetes', en: 'Kubernetes' },
  container: { zh: '容器', en: 'Container' },
  disk: { zh: '磁盘', en: 'Disk' },
  ebpf: { zh: 'eBPF', en: 'eBPF' },
  database: { zh: '数据库', en: 'Database' },
  dns: { zh: 'DNS', en: 'DNS' },
  systemd: { zh: 'systemd', en: 'systemd' },
  methodology: { zh: '方法论', en: 'Methodology' },
  storage: { zh: '存储', en: 'Storage' },
  tls: { zh: 'TLS', en: 'TLS' },
  shell: { zh: 'Shell', en: 'Shell' },
  http: { zh: 'HTTP', en: 'HTTP' },
  tracing: { zh: '追踪', en: 'Tracing' },
  performance: { zh: '性能', en: 'Performance' },
  scheduling: { zh: '调度', en: 'Scheduling' },
  linux: { zh: 'Linux', en: 'Linux' },
  // Legacy Chinese-keyed entries — kept so older Chinese-named vaults
  // still translate correctly when synced into the same UI.
  存储: { zh: '存储', en: 'Storage' },
  磁盘: { zh: '磁盘', en: 'Disk' },
  调度: { zh: '调度', en: 'Scheduling' },
  时区: { zh: '时区', en: 'Timezone' },
  容器: { zh: '容器', en: 'Container' },
  网络: { zh: '网络', en: 'Network' },
  连通性: { zh: '连通性', en: 'Connectivity' },
  网卡: { zh: '网卡', en: 'NIC' },
  控制面: { zh: '控制面', en: 'Control plane' },
  Linux系统: { zh: 'Linux 系统', en: 'Linux' },
  进程: { zh: '进程', en: 'Processes' },
  资源: { zh: '资源', en: 'Resources' },
};

// Title translations. Key = exact Chinese title string from qdrant.
// Operators/users who add new docs will simply pass through.
const KNOWLEDGE_TITLES: Record<string, string> = {
  'XFS vs ext4 选型与常见坑 playbook': 'XFS vs ext4 — selection & common pitfalls (playbook)',
  'Redis 内存淘汰与 maxmemory 排查 playbook': 'Redis eviction & maxmemory diagnosis (playbook)',
  'K8s Pod CrashLoopBackOff 排查 playbook': 'K8s Pod CrashLoopBackOff diagnosis (playbook)',
  'MySQL 慢查询排查 playbook': 'MySQL slow-query diagnosis (playbook)',
  '容器 cgroup 限制与 systemd-cgls 排查 playbook': 'Container cgroup limits & systemd-cgls diagnosis (playbook)',
  'K8s PVC 绑定失败排查 playbook': 'K8s PVC binding failure diagnosis (playbook)',
  'conntrack 表满 + NAT 排查': 'conntrack table full + NAT diagnosis',
  'eBPF 网络丢包排查': 'eBPF network packet-loss diagnosis',
  'ping / traceroute / mtr 三件套': 'ping / traceroute / mtr — the three-piece set',
  'K8s Pod OOMKilled 排查 playbook': 'K8s Pod OOMKilled diagnosis (playbook)',
  'Linux OOM Killer 排查 playbook': 'Linux OOM Killer diagnosis (playbook)',
  'Kubernetes etcd 性能撞墙排查 playbook': 'Kubernetes etcd performance-wall diagnosis (playbook)',
  'Linux 进程 D state 排查 playbook': 'Linux D-state process diagnosis (playbook)',
  'K8s Pod 卡 Pending 排查 playbook': 'K8s Pod stuck Pending diagnosis (playbook)',
  'Linux 服务器时区错乱排查 playbook': 'Linux server timezone misconfiguration diagnosis (playbook)',
  'Linux load 高但 CPU 空闲排查 playbook': 'Linux high-load with idle-CPU diagnosis (playbook)',
  'Linux dmesg ringbuffer 用法与坑 playbook': 'Linux dmesg ring buffer — usage & pitfalls (playbook)',
  'MySQL 连接数打满排查 playbook': 'MySQL max-connections exhaustion diagnosis (playbook)',
  'MTU / MSS / PMTUD 黑洞排查': 'MTU / MSS / PMTUD black-hole diagnosis',
  'K8s pod 网络连不通 - netshoot 排查': 'K8s pod network unreachable — netshoot diagnosis',
  '磁盘 inode 占满排查 playbook': 'Disk inode exhaustion diagnosis (playbook)',
  'systemd timer 入门与替代 cron playbook': 'systemd timer primer & cron replacement (playbook)',
  'strace 入门排查 playbook': 'strace primer for diagnostics (playbook)',
  'K8s ImagePullBackOff 排查 playbook': 'K8s ImagePullBackOff diagnosis (playbook)',
  'TCP RST 排查 playbook': 'TCP RST diagnosis (playbook)',
  'iostat 解读与磁盘 IO 瓶颈定位 playbook': 'iostat interpretation & disk IO bottleneck (playbook)',
  'Docker overlay2 占满磁盘排查 playbook': 'Docker overlay2 disk exhaustion diagnosis (playbook)',
  'cron 没跑 / 不执行排查 playbook': 'cron not running / not executing diagnosis (playbook)',
  'Docker 启动慢 / hang 排查 playbook': 'Docker slow startup / hang diagnosis (playbook)',
  'Linux 网络命名空间排查思路': 'Linux network namespace diagnosis approach',
  'Linux fork bomb 防护与排查 playbook': 'Linux fork-bomb protection & diagnosis (playbook)',
  'Linux 网卡丢包 / 性能瓶颈排查': 'Linux NIC packet-loss / performance bottleneck diagnosis',
  'Linux DNS 解析故障排查': 'Linux DNS resolution failure diagnosis',
  'MongoDB Oplog 与复制集延迟排查 playbook': 'MongoDB Oplog & replica-set lag diagnosis (playbook)',
  'Linux ulimit 与文件句柄耗尽排查 playbook': 'Linux ulimit & fd exhaustion diagnosis (playbook)',
  'TLS 握手失败诊断（openssl s_client）': 'TLS handshake failure diagnosis (openssl s_client)',
  'lsof 找文件占用与已删除大文件 playbook': 'lsof — finding file usage & deleted big files (playbook)',
  'PostgreSQL VACUUM 与膨胀排查 playbook': 'PostgreSQL VACUUM & bloat diagnosis (playbook)',
  // First-party diagnostic playbooks shipped Chinese-only (diagnostics/*-cn.md);
  // map their titles so en-US mode shows English instead of the raw Chinese.
  '集群异常 / 告警风暴怎么排查': 'Cluster anomaly / alert-storm diagnosis',
  '磁盘满了怎么排查': 'Disk full diagnosis',
  '内存高 / OOM 怎么排查': 'High memory / OOM diagnosis',
  '网络慢 / 连不通怎么排查': 'Network slow / unreachable diagnosis',
  '服务挂了 / systemd 失败怎么排查': 'Service down / systemd failure diagnosis',
  '权威技术文档参考索引': 'Authoritative technical docs index',
};

/**
 * Returns the operator-facing title for a doc. Resolution order in
 * en-US locale:
 *   1. doc.title_en (per-doc operator-supplied English overlay)
 *   2. KNOWLEDGE_TITLES static map (seed translations for the bundled
 *      vault — covers the first batch of playbooks)
 *   3. doc.title (original, source-language)
 * In zh-CN locale always returns doc.title (the source-language string).
 *
 * Accepts either a Doc object (preferred — uses title_en) or a bare
 * string (legacy callers that only have a title — only the static map
 * applies).
 */
export function localizedDocTitle(input: KnowledgeDoc | string): string {
  if (typeof input === 'string') {
    const en = KNOWLEDGE_TITLES[input];
    return en ? trInline(input, en) : input;
  }
  if (input.title_en && input.title_en.trim() !== '') {
    return trInline(input.title, input.title_en);
  }
  const en = KNOWLEDGE_TITLES[input.title];
  return en ? trInline(input.title, en) : input.title;
}

/** Returns the user-locale path, translating each segment if known. */
export function localizedPath(path: string | null | undefined): string {
  if (!path) return path ?? '';
  return path.split('/').map(localizedPathSegment).join('/');
}

/** Single-segment helper for the tree view (no slashes). */
export function localizedPathSegment(seg: string): string {
  const m = KNOWLEDGE_PATH_SEGMENTS[seg];
  return m ? trInline(m.zh, m.en) : seg;
}
