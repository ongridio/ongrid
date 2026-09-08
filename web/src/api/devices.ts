import { request } from './client';

export type DeviceRole = 'host' | 'discovered';

export type Device = {
  id: number;
  name: string;
  hostname?: string;
  description?: string;
  ip_address?: string;
  os?: string;
  os_version?: string;
  arch?: string;
  kernel_version?: string;
  cpu_count?: number;
  mem_total_bytes?: number;
  disk_total_bytes?: number;
  cpu_usage_pct?: number;
  mem_usage_pct?: number;
  disk_usage_pct?: number;
  roles?: string[];
  scope?: DeviceRole;
  online?: boolean;
  last_seen_at?: string | null;
  // Host devices use online/last_seen_at (Edge heartbeat). SNMP-managed
  // network devices expose their separate probe state here.
  reachability_status?: string;
  last_reachable_at?: string | null;
  created_at?: string;
  updated_at?: string;
  // — points at the row in topology.nodes that fronts this
  // device. Null until topology.Migrate's backfill has run.
  node_id?: number | null;
};

export type DeviceEdgeLink = {
  edge_id: number;
  device_id: number;
  type: DeviceRole | 'unknown';
  created_at: string;
};

export type NetworkDiscoveryCandidate = {
  id: number;
  observer_edge_id: number;
  observer_edge_name?: string;
  observer_host_device_id?: number;
  observer_host_name?: string;
  observation_key: string;
  ip_address?: string;
  mac?: string;
  interface_name?: string;
  source: string;
  source_data?: Record<string, string>;
  interfaces?: unknown[];
  links?: unknown[];
  status: string;
  confidence: number;
  promoted_device_id?: number;
  first_seen_at: string;
  last_seen_at: string;
};

export type NetworkSNMPScanInput = {
  name?: string;
  address?: string;
  port?: number;
  version: 'v2c' | 'v3';
  community?: string;
  username?: string;
  auth_protocol?: string;
  auth_secret?: string;
  privacy_protocol?: string;
  privacy_secret?: string;
  timeout_ms?: number;
  retries?: number;
};

export type NetworkInterface = {
  if_index?: number;
  name?: string;
  mac?: string;
  interface_kind?: string;
  description?: string;
  admin_status?: string;
  oper_status?: string;
	 speed_bps?: number;
	 in_octets?: number;
	 out_octets?: number;
	 in_errors?: number;
	 out_errors?: number;
  addresses?: string[];
};

export type NetworkLink = {
  remote_chassis_id?: string;
  remote_chassis_subtype?: string;
  local_interface_name?: string;
  remote_interface_name?: string;
  link_kind?: string;
};

export type NetworkDeviceDetail = {
  device_id: number;
  device_kind: string;
  vendor?: string;
  model?: string;
  serial_number?: string;
  management_address?: string;
  sys_name?: string;
  sys_description?: string;
  snmp_engine_id?: string;
  lldp_chassis_id?: string;
  bridge_base_mac?: string;
  reachability_status: string;
  last_reachable_at?: string;
	 poll_enabled?: boolean;
	 poll_interval_seconds?: number;
	 poll_credential_name?: string;
	 poll_port?: number;
	 last_poll_at?: string;
	 last_poll_error?: string;
  discovery_source?: string;
  scanner_edge_id?: number;
  scanner_edge_name?: string;
  scanner_host_device_id?: number;
  scanner_host_name?: string;
  last_observed_at?: string;
  source_data?: Record<string, string>;
  interfaces?: NetworkInterface[];
  links?: NetworkLink[];
};

export function listNetworkCandidates(params?: { status?: string }) {
  const qs = params?.status
    ? `?${new URLSearchParams({ status: params.status }).toString()}`
    : '';
  return request<{ items: NetworkDiscoveryCandidate[]; total: number }>(
    'GET',
    `/network-discovery/candidates${qs}`,
  );
}

export type NetworkPollingInput = {
  enabled: boolean;
  interval_seconds?: number;
  credential_name?: string;
  port?: number;
};

export function scanNetworkCandidate(
  id: string | number,
  input: NetworkSNMPScanInput,
) {
  return request<Device>(
    'POST',
    `/network-discovery/candidates/${encodeURIComponent(String(id))}/snmp-scan`,
    input,
  );
}

export function listDevices(params?: { roles?: string }) {
  const qs = params?.roles
    ? `?${new URLSearchParams({ roles: params.roles }).toString()}`
    : '';
  return request<{ items: Device[]; total: number }>('GET', `/devices${qs}`);
}

export function getDevice(id: string | number) {
  return request<Device>('GET', `/devices/${encodeURIComponent(String(id))}`);
}

export function getNetworkDeviceDetail(id: string | number) {
  return request<NetworkDeviceDetail>(
    'GET',
    `/devices/${encodeURIComponent(String(id))}/network`,
  );
}

export function configureNetworkPolling(id: string | number, input: NetworkPollingInput) {
  return request<NetworkDeviceDetail>(
    'PATCH',
    `/devices/${encodeURIComponent(String(id))}/network/polling`,
    input,
  );
}

export function renameDevice(id: string | number, name: string) {
  return request<void>('PATCH', `/devices/${encodeURIComponent(String(id))}`, { name });
}

export function deleteDevice(id: string | number) {
  return request<void>('DELETE', `/devices/${encodeURIComponent(String(id))}`);
}

export function listDeviceEdges(id: string | number) {
  return request<{ items: DeviceEdgeLink[] }>(
    'GET',
    `/devices/${encodeURIComponent(String(id))}/edges`,
  );
}
