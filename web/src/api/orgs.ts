// orgs.ts — typed wrappers for the organization + org-membership endpoints.
//
// Backend routes (manager v1):
//   GET    /api/v1/orgs
//   POST   /api/v1/orgs
//   PATCH  /api/v1/orgs/{id}
//   DELETE /api/v1/orgs/{id}
//   GET    /api/v1/orgs/{id}/members
//   POST   /api/v1/orgs/{id}/members
//   PATCH  /api/v1/orgs/{id}/members/{user_id}
//   DELETE /api/v1/orgs/{id}/members/{user_id}
//
// 组织角色固定 3 档：
//   org_admin = 该组织管理员（增删成员 / 改组织信息）
//   member    = 普通成员
//   viewer    = 只读
import { request } from './client';

export type OrgRole = 'org_admin' | 'member' | 'viewer';

export type Org = {
  id: number;
  name: string;
  description: string;
  // null = 顶级 org。前端 list 上自己构建 tree（每条记录都带 parent_id）。
  parent_id: number | null;
  created_at: string;
  updated_at: string;
};

export type OrgMember = {
  user_id: number;
  email: string;
  display_name: string;
  role: OrgRole;
};

export type OrgListResp = { items: Org[]; total: number };
export type OrgMemberListResp = { items: OrgMember[]; total: number };

export function listOrgs(): Promise<OrgListResp> {
  return request<OrgListResp>('GET', '/orgs');
}

export function createOrg(body: {
  name: string;
  description?: string;
  // null / 缺省 = 顶级 org
  parent_id?: number | null;
}): Promise<Org> {
  return request<Org>('POST', '/orgs', body);
}

// updateOrg: parent_id_set 控制是否动 parent 列。true + parent_id=null
// 提到顶级；true + parent_id=N 移动到 N 之下；false（默认）只改 name/desc。
export function updateOrg(
  id: number,
  body: {
    name: string;
    description?: string;
    parent_id_set?: boolean;
    parent_id?: number | null;
  }
): Promise<Org> {
  return request<Org>('PATCH', `/orgs/${id}`, body);
}

export function deleteOrg(id: number): Promise<void> {
  return request<void>('DELETE', `/orgs/${id}`);
}

export function listOrgMembers(orgId: number): Promise<OrgMemberListResp> {
  return request<OrgMemberListResp>('GET', `/orgs/${orgId}/members`);
}

export function addOrgMember(
  orgId: number,
  body: { user_id: number; role: OrgRole }
): Promise<OrgMember> {
  return request<OrgMember>('POST', `/orgs/${orgId}/members`, body);
}

export function setOrgMemberRole(
  orgId: number,
  userId: number,
  role: OrgRole
): Promise<void> {
  // The manager intentionally returns 204 after a successful role update;
  // there is no response body to decode into an OrgMember.
  return request<void>('PATCH', `/orgs/${orgId}/members/${userId}`, { role });
}

export function removeOrgMember(orgId: number, userId: number): Promise<void> {
  return request<void>('DELETE', `/orgs/${orgId}/members/${userId}`);
}
