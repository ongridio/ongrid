import { request } from './client';

export type LoginResponse = {
  access_token: string;
  refresh_token?: string;
  token_type?: string;
  expires_in?: number;
  role?: string;
  email?: string;
  user?: { email?: string; role?: string; id?: string };
};

export type SelfResponse = {
  email: string;
  role: string;
  id?: string;
};

export function login(email: string, password: string) {
  return request<LoginResponse>('POST', '/auth/login', { email, password }, { noAuth: true });
}

export function refresh(refreshToken: string) {
  return request<LoginResponse>('POST', '/auth/refresh', { refresh_token: refreshToken }, { noAuth: true });
}

export function getSelf() {
  return request<SelfResponse>('GET', '/auth/self');
}
