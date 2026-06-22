import { request } from '@/api/client';

export type UpgradeCommand = {
  id: string;
  label: string;
  arch: string;
  command: string;
};

export type UpgradeInfo = {
  current_version: string;
  latest_version: string;
  update_available: boolean;
  comparison_supported: boolean;
  release_url?: string;
  published_at?: string;
  checked_at: string;
  commands: UpgradeCommand[];
};

export function checkSystemUpgrade() {
  return request<UpgradeInfo>('POST', '/system/upgrade/check');
}
