package tunnel

import "encoding/json"

// This file hand-mirrors api/tunnel/v1/tunnel.proto message shapes as Go
// structs with JSON tags. the tunnel body wire format is JSON
// in MVP; we deliberately avoid generating protobuf Go types for these
// payloads so internal/pkg/tunnel/ stays dependency-free (no protobuf
// import, no generated-code directory). When (if) we switch to protobuf
// binary in Phase 2, this file is the seam: swap types here, keep
// callers unchanged.
//
// Field names MUST stay in sync with tunnel.proto's json_name annotations.

// Method names used on the wire. Exposing them as constants keeps
// callers spell-safe.
const (
	MethodRegisterEdge        = "register_edge"
	MethodHeartbeat           = "heartbeat"
	MethodPushHostMetrics     = "push_host_metrics"
	MethodPushPromSamples     = "push_prom_samples"
	MethodPushK8sInventory    = "push_k8s_inventory"
	MethodDescribeK8sResource = "describe_k8s_resource"
	MethodQueryK8sLogs        = "query_k8s_logs"
	MethodExecuteK8sAction    = "execute_k8s_action"
	MethodGetHostLoad         = "get_host_load"
	MethodGetProcessList      = "get_process_list"
	MethodGetNetstat          = "get_netstat"
	// MethodExecuteSkill is the single dispatcher RPC for the skill
	// framework. Edge agent registers one handler that looks up the
	// skill key in its local registry — no per-skill wire method.
	MethodExecuteSkill = "execute_skill"
	// MethodGetPluginConfigs is edge → manager: pull this edge's full
	// plugin config snapshot. Edge calls on startup, on receiving a
	// MethodPluginConfigsChanged push, and every 60s as safety net.
	MethodGetPluginConfigs = "get_plugin_configs"
	// MethodPluginConfigsChanged is manager → edge: notification that
	// the configs changed. Body is empty; edge re-fetches via
	// MethodGetPluginConfigs. (real-time push.)
	MethodPluginConfigsChanged = "plugin_configs_changed"
	// MethodWriteDatabaseMetricsSecret is manager → edge: write a
	// databasemetrics credential file on the edge host. The manager sends
	// this only during a user-initiated save; the normal plugin config
	// snapshot still carries only non-secret metadata.
	MethodWriteDatabaseMetricsSecret = "write_database_metrics_secret"

	// WebSSH (manager → edge): edge agent acts as an SSH client into
	// the host's local sshd. Each browser session is identified by a
	// uuid SessionID; multiple concurrent sessions per edge are fine.
	//   shell_open start session (SSH dial + Shell())
	//   shell_input one stdin chunk
	//   shell_resize update pty window size
	//   shell_close terminate session (manager-side close)
	// (edge → manager):
	//   shell_output one stdout/stderr chunk
	//   shell_exit terminal frame with exit code
	MethodShellOpen   = "shell_open"
	MethodShellInput  = "shell_input"
	MethodShellResize = "shell_resize"
	MethodShellClose  = "shell_close"
	MethodShellOutput = "shell_output"
	MethodShellExit   = "shell_exit"

	// MethodAgentUpgrade (manager → edge): swap the running edge binary
	// to the version at URL after verifying SHA256. Edge stages the new
	// binary in its own writable area, exits cleanly; systemd's
	// ExecStartPre script atomically swaps it into /usr/local/bin/.
	MethodAgentUpgrade = "agent_upgrade"

	// MethodFetchPackage (manager → edge,): fetch the whole edge
	// release bundle (agent + plugins + apply script) as a tarball,
	// verify outer SHA256, extract, verify each file from MANIFEST.txt.
	// Edge returns "staged" without restarting; the manager calls
	// MethodApplyPackage when it's ready to flip the swap.
	MethodFetchPackage = "fetch_package"

	// MethodApplyPackage (manager → edge,): signal the agent to
	// exit so systemd restarts it, at which point the ExecStartPre
	// apply-pending-upgrade.sh script swaps every staged file into its
	// declared dest. Edge ACKs first, then exits — the ACK is what
	// tells the manager "swap is happening now, watch for the new
	// agent_version on next register".
	MethodApplyPackage = "apply_package"
)

// ---------------------------------------------------------------------
// webssh
// ---------------------------------------------------------------------

// ShellOpenRequest is the manager-to-edge request that establishes a
// new WebSSH session. SSHPass is one-shot and wiped from edge memory
// after Dial; never logged, never stored.
type ShellOpenRequest struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
	Term      string `json:"term"`     // e.g. "xterm-256color"
	SSHHost   string `json:"ssh_host"` // default "127.0.0.1:22"
	SSHUser   string `json:"ssh_user"`
	SSHPass   string `json:"ssh_pass"` // wiped after Dial
}

// ShellOpenResponse acks the SSH session is up. On failure Err is set.
type ShellOpenResponse struct {
	Ok  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

// ShellInputRequest carries a stdin chunk.
type ShellInputRequest struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}

// ShellInputResponse is empty.
type ShellInputResponse struct{}

// ShellResizeRequest updates pty window size.
type ShellResizeRequest struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

// ShellResizeResponse is empty.
type ShellResizeResponse struct{}

// ShellCloseRequest signals manager-side wants the session torn down.
type ShellCloseRequest struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

// ShellCloseResponse is empty.
type ShellCloseResponse struct{}

// ShellOutputRequest is the edge-to-manager push of one stdout chunk.
// stderr is PTY-merged so the browser sees a single stream.
type ShellOutputRequest struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}

// ShellOutputResponse is empty.
type ShellOutputResponse struct{}

// ShellExitRequest is the terminal edge-to-manager frame. After
// ShellExit no further outputs for this SessionID are valid.
type ShellExitRequest struct {
	SessionID string `json:"session_id"`
	ExitCode  int    `json:"exit_code"`
	Err       string `json:"err,omitempty"`
}

// ShellExitResponse is empty.
type ShellExitResponse struct{}

// GetPluginConfigsResponse is the wire snapshot served on
// MethodGetPluginConfigs. Mirrors biz/edge.WireSnapshot — duplicated
// here to keep internal/pkg/tunnel free of biz imports.
type GetPluginConfigsResponse struct {
	EdgeID  uint64                           `json:"edge_id"`
	Configs map[string]GetPluginConfigsEntry `json:"configs"`
}

// GetPluginConfigsEntry is one plugin's slice of the snapshot.
type GetPluginConfigsEntry struct {
	Enabled  bool                   `json:"enabled"`
	Endpoint string                 `json:"endpoint,omitempty"`
	Spec     map[string]interface{} `json:"spec,omitempty"`
}

// WriteDatabaseMetricsSecretRequest carries one edge-local credential file.
// Content is secret material; do not log it and do not persist it on the
// manager side.
type WriteDatabaseMetricsSecretRequest struct {
	SourceID         string                 `json:"source_id"`
	Path             string                 `json:"path"`
	Content          string                 `json:"content,omitempty"`
	DBType           string                 `json:"db_type,omitempty"`
	Credentials      map[string]interface{} `json:"credentials,omitempty"`
	PreservePassword bool                   `json:"preserve_password,omitempty"`
	Delete           bool                   `json:"delete,omitempty"`
}

// WriteDatabaseMetricsSecretsRequest batches edge-local credential writes so
// the edge can stage every secret before replacing any existing file.
type WriteDatabaseMetricsSecretsRequest struct {
	Secrets []WriteDatabaseMetricsSecretRequest `json:"secrets"`
}

// WriteDatabaseMetricsSecretResponse acknowledges that the edge wrote the
// requested credential file.
type WriteDatabaseMetricsSecretResponse struct {
	OK bool `json:"ok"`
}

// ExecuteSkillRequest is the cloud->edge skill invocation envelope.
// Key identifies the skill in the registry; Params is the JSON-encoded
// param object that the skill's Executor decodes.
type ExecuteSkillRequest struct {
	Key    string          `json:"key"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ExecuteSkillResponse carries either the JSON result blob (on success)
// or an Error string. Manager surfaces Error verbatim to the caller.
type ExecuteSkillResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// ---------------------------------------------------------------------
// register_edge (edge -> cloud)
// ---------------------------------------------------------------------

// HostInfo is the static host description carried in RegisterEdgeRequest.
//
// Fingerprint is the per-host stable id (typically /etc/machine-id on
// Linux, IOPlatformUUID on macOS, MachineGuid on Windows). The cloud
// uses it to dedupe Device rows so an edge agent can be uninstalled
// and reinstalled without losing the host's identity. Empty string is
// allowed (older agents, or platforms where the id is unavailable);
// the cloud falls back to a hashed-hostname fingerprint in that case.
type HostInfo struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	KernelVersion string `json:"kernel_version"`
	CPUCount      int    `json:"cpu_count"`
	MemTotalBytes uint64 `json:"mem_total_bytes"`
	Fingerprint   string `json:"fingerprint,omitempty"`

	// HardwareFingerprint is a clone-resistant hardware identity computed
	// edge-side from physical-NIC MACs + CPU model + disk serials (see
	// internal/edgeagent/collector hardwareFingerprint). Unlike Fingerprint
	// (gopsutil HostID), it does NOT collapse cloned Linux VMs, which share a
	// single SMBIOS product_uuid (issue #96): a hypervisor regenerates the NIC
	// MAC per clone. The cloud prefers this when non-empty and falls back to
	// Fingerprint otherwise (older agents / hosts with no physical NIC). Both
	// fields are sent together so the cloud can migrate a device from its old
	// HostID-derived fingerprint to this one in place. Empty is allowed.
	HardwareFingerprint string `json:"hardware_fingerprint,omitempty"`

	// IPAddress is the primary IPv4 address of the host, collected
	// edge-side during register_edge. Used for display in the device
	// list/detail so operators can identify hosts by IP without
	// cross-referencing external tools. Empty string when the agent
	// cannot determine a suitable address (e.g. no non-loopback
	// interface found).
	IPAddress string `json:"ip_address,omitempty"`
}

// KubernetesInfo is optional context carried by Kubernetes-deployed edges.
// Node mode still maps to a host Device; controller mode represents the
// cluster control plane and must not create host Devices.
type KubernetesInfo struct {
	Mode        string `json:"mode,omitempty"`
	ClusterID   uint64 `json:"cluster_id,omitempty"`
	ClusterUID  string `json:"cluster_uid,omitempty"`
	ClusterName string `json:"cluster_name,omitempty"`
	Role        string `json:"role,omitempty"`
	NodeName    string `json:"node_name,omitempty"`
	NodeUID     string `json:"node_uid,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	PodName     string `json:"pod_name,omitempty"`
}

// RegisterEdgeRequest is the first RPC the edge sends after connecting.
type RegisterEdgeRequest struct {
	AccessKey    string          `json:"access_key"`
	SecretKey    string          `json:"secret_key"`
	HostInfo     HostInfo        `json:"host_info"`
	AgentVersion string          `json:"agent_version,omitempty"`
	Kubernetes   *KubernetesInfo `json:"kubernetes,omitempty"`
}

// RegisterEdgeResponse is what the cloud answers on successful register.
type RegisterEdgeResponse struct {
	EdgeID     uint64 `json:"edge_id"`
	ServerTime int64  `json:"server_time"` // unix seconds UTC
}

// ---------------------------------------------------------------------
// heartbeat (edge -> cloud)
// ---------------------------------------------------------------------

// HeartbeatRequest carries a client-side timestamp. Server compares its
// own clock to detect major skew. Plugins piggybacks the per-plugin
// runtime health so the manager / UI can see "agent is up but the logs
// plugin crashed (binary missing)" instead of silent empty telemetry.
type HeartbeatRequest struct {
	EdgeID      uint64             `json:"edge_id,omitempty"`
	Ts          int64              `json:"ts"` // unix seconds
	StatusFlags map[string]string  `json:"status_flags,omitempty"`
	Plugins     []PluginHealthWire `json:"plugins,omitempty"`
}

// PluginHealthWire is one plugin's runtime health on the heartbeat wire.
// Mirrors edgeagent/plugins.PluginHealth; the edge maps between the two so
// the plugin runtime stays decoupled from the tunnel protocol. State is one
// of stopped|starting|running|crashed. LastError is set when a plugin can't
// start (e.g. "subprocess binary missing") — that string is the whole point
// of this field: it turns a silent failure into an operator-visible reason.
type PluginHealthWire struct {
	Name         string                   `json:"name"`
	State        string                   `json:"state"`
	LastError    string                   `json:"last_error,omitempty"`
	RestartCount int                      `json:"restart_count,omitempty"`
	PID          int                      `json:"pid,omitempty"`
	StartedAt    int64                    `json:"started_at,omitempty"` // unix sec, 0 if never started
	UpdatedAt    int64                    `json:"updated_at,omitempty"` // unix sec
	Targets      []PluginTargetHealthWire `json:"targets,omitempty"`
}

// PluginTargetHealthWire is the source-level health carried inside a plugin
// heartbeat entry. Multi-target metric plugins use this for individual scrape
// targets / database sources.
type PluginTargetHealthWire struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	Kind          string `json:"kind,omitempty"`
	State         string `json:"state"`
	LastError     string `json:"last_error,omitempty"`
	Samples       int    `json:"samples,omitempty"`
	LastSuccessAt int64  `json:"last_success_at,omitempty"`
	UpdatedAt     int64  `json:"updated_at,omitempty"`
}

// HeartbeatResponse is empty but kept as a typed value so callers can
// evolve the payload without changing the Call signature.
type HeartbeatResponse struct{}

// ---------------------------------------------------------------------
// push_host_metrics (edge -> cloud)
// ---------------------------------------------------------------------

// HostMetricPoint is one sample in a metrics batch.
type HostMetricPoint struct {
	Ts          int64   `json:"ts"` // unix seconds
	CPUPct      float64 `json:"cpu_pct"`
	MemPct      float64 `json:"mem_pct"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	NetRxBps    uint64  `json:"net_rx_bps"`
	NetTxBps    uint64  `json:"net_tx_bps"`
	DiskUsedPct float64 `json:"disk_used_pct"`
}

// PushHostMetricsRequest pushes a batch of points (ring-buffered edge side).
type PushHostMetricsRequest struct {
	EdgeID uint64            `json:"edge_id,omitempty"`
	Points []HostMetricPoint `json:"points"`
}

// PushHostMetricsResponse reports how many points were accepted
// (after server-side dedup / rejection).
type PushHostMetricsResponse struct {
	Accepted uint32 `json:"accepted"`
}

// ---------------------------------------------------------------------
// push_prom_samples (edge -> cloud)
// ---------------------------------------------------------------------

// PromSample is one (metric_name, labels, value, ts) tuple, mirroring
// Prometheus's text-format/protobuf model. The cloud-side handler
// forwards these to Prometheus via remote_write.
type PromSample struct {
	Name   string            `json:"name"`             // e.g. "node_cpu_seconds_total"
	Labels map[string]string `json:"labels,omitempty"` // dimension labels (mode=, device=, ...)
	Value  float64           `json:"value"`
	TsMs   int64             `json:"ts_ms"` // unix milliseconds
}

// PushPromSamplesRequest is one push of open-set samples. Source identifies
// the producer: "embedded" for in-process host collection,
// "scrape:<target_name>" for an HTTP-scraped host target, or
// "k8s:<target_name>" for a Kubernetes controller-scoped target.
type PushPromSamplesRequest struct {
	EdgeID  uint64       `json:"edge_id,omitempty"`
	Source  string       `json:"source"`
	Samples []PromSample `json:"samples"`
}

// PushPromSamplesResponse reports how many samples the cloud accepted.
type PushPromSamplesResponse struct {
	Accepted int `json:"accepted"`
}

// ---------------------------------------------------------------------
// push_k8s_inventory (edge controller -> cloud)
// ---------------------------------------------------------------------

// KubernetesInventoryRequest is the controller's current Kubernetes object
// snapshot. Scope tells the manager whether omitted workload/pod objects are
// absent from the whole cluster or only from a Namespace, so stale snapshots
// can be pruned without crossing the declared inventory boundary.
type KubernetesInventoryRequest struct {
	EdgeID               uint64                       `json:"edge_id,omitempty"`
	ClusterID            uint64                       `json:"cluster_id"`
	Mode                 string                       `json:"mode,omitempty"`
	Role                 string                       `json:"role,omitempty"`
	Scope                string                       `json:"scope,omitempty"`     // cluster or namespace
	Namespace            string                       `json:"namespace,omitempty"` // required when Scope=namespace
	Ts                   int64                        `json:"ts"`
	ResourceVersion      string                       `json:"resource_version,omitempty"`
	ResourceVersions     map[string]string            `json:"resource_versions,omitempty"`
	CollectDurationMS    int64                        `json:"collect_duration_ms,omitempty"`
	WatchEventObservedAt int64                        `json:"watch_event_observed_at,omitempty"`
	WatchTriggerReason   string                       `json:"watch_trigger_reason,omitempty"`
	SyncType             string                       `json:"sync_type,omitempty"`
	Nodes                []KubernetesNodeSnapshot     `json:"nodes,omitempty"`
	Workloads            []KubernetesWorkloadSnapshot `json:"workloads,omitempty"`
	Pods                 []KubernetesPodSnapshot      `json:"pods,omitempty"`
	Events               []KubernetesEventSnapshot    `json:"events,omitempty"`
	DeletedNodes         []KubernetesNodeRef          `json:"deleted_nodes,omitempty"`
	DeletedWorkloads     []KubernetesWorkloadRef      `json:"deleted_workloads,omitempty"`
	DeletedPods          []KubernetesPodRef           `json:"deleted_pods,omitempty"`
	DeletedEvents        []KubernetesEventRef         `json:"deleted_events,omitempty"`
}

type KubernetesNodeSnapshot struct {
	Name           string              `json:"name"`
	UID            string              `json:"uid,omitempty"`
	ProviderID     string              `json:"provider_id,omitempty"`
	Labels         map[string]string   `json:"labels,omitempty"`
	Taints         []map[string]any    `json:"taints,omitempty"`
	Conditions     []map[string]string `json:"conditions,omitempty"`
	Capacity       map[string]string   `json:"capacity,omitempty"`
	Allocatable    map[string]string   `json:"allocatable,omitempty"`
	KubeletVersion string              `json:"kubelet_version,omitempty"`
}

type KubernetesWorkloadSnapshot struct {
	Kind            string              `json:"kind"`
	Namespace       string              `json:"namespace,omitempty"`
	Name            string              `json:"name"`
	UID             string              `json:"uid,omitempty"`
	DesiredReplicas int                 `json:"desired_replicas,omitempty"`
	ReadyReplicas   int                 `json:"ready_replicas,omitempty"`
	Labels          map[string]string   `json:"labels,omitempty"`
	Annotations     map[string]string   `json:"annotations,omitempty"`
	Conditions      []map[string]string `json:"conditions,omitempty"`
}

type KubernetesPodSnapshot struct {
	Namespace    string `json:"namespace,omitempty"`
	Name         string `json:"name"`
	UID          string `json:"uid,omitempty"`
	NodeName     string `json:"node_name,omitempty"`
	Phase        string `json:"phase,omitempty"`
	OwnerKind    string `json:"owner_kind,omitempty"`
	OwnerName    string `json:"owner_name,omitempty"`
	RestartCount int    `json:"restart_count,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type KubernetesNodeRef struct {
	Name string `json:"name,omitempty"`
	UID  string `json:"uid,omitempty"`
}

type KubernetesWorkloadRef struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

type KubernetesPodRef struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

type KubernetesEventRef struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

type KubernetesEventSnapshot struct {
	Namespace           string `json:"namespace,omitempty"`
	Name                string `json:"name"`
	UID                 string `json:"uid,omitempty"`
	Type                string `json:"type,omitempty"`
	Reason              string `json:"reason,omitempty"`
	Message             string `json:"message,omitempty"`
	InvolvedKind        string `json:"involved_kind,omitempty"`
	InvolvedNamespace   string `json:"involved_namespace,omitempty"`
	InvolvedName        string `json:"involved_name,omitempty"`
	InvolvedUID         string `json:"involved_uid,omitempty"`
	SourceComponent     string `json:"source_component,omitempty"`
	SourceHost          string `json:"source_host,omitempty"`
	ReportingController string `json:"reporting_controller,omitempty"`
	ReportingInstance   string `json:"reporting_instance,omitempty"`
	Action              string `json:"action,omitempty"`
	Count               int    `json:"count,omitempty"`
	FirstTimestamp      string `json:"first_timestamp,omitempty"`
	LastTimestamp       string `json:"last_timestamp,omitempty"`
	EventTime           string `json:"event_time,omitempty"`
}

type KubernetesInventoryResponse struct {
	AcceptedNodes     int `json:"accepted_nodes"`
	AcceptedWorkloads int `json:"accepted_workloads"`
	AcceptedPods      int `json:"accepted_pods"`
	AcceptedEvents    int `json:"accepted_events"`
}

// ---------------------------------------------------------------------
// describe_k8s_resource (cloud -> edge controller)
// ---------------------------------------------------------------------

// KubernetesDescribeResourceRequest asks the cluster controller to read one
// Kubernetes object from the live Kubernetes API. It is intentionally
// read-only; writes must use a separate audited action method.
type KubernetesDescribeResourceRequest struct {
	ClusterID     uint64 `json:"cluster_id"`
	Kind          string `json:"kind"`
	APIVersion    string `json:"api_version,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	Name          string `json:"name"`
	IncludeEvents bool   `json:"include_events,omitempty"`
	EventsLimit   int    `json:"events_limit,omitempty"`
}

// KubernetesDescribeResourceResponse carries a sanitized object JSON and a
// bounded set of related Kubernetes Events. Object omits noisy managedFields
// and annotations so the LLM sees the current state without huge metadata.
type KubernetesDescribeResourceResponse struct {
	ClusterID       uint64                    `json:"cluster_id"`
	Kind            string                    `json:"kind"`
	APIVersion      string                    `json:"api_version,omitempty"`
	Namespace       string                    `json:"namespace,omitempty"`
	Name            string                    `json:"name"`
	UID             string                    `json:"uid,omitempty"`
	ResourceVersion string                    `json:"resource_version,omitempty"`
	FetchedAt       int64                     `json:"fetched_at"`
	Object          json.RawMessage           `json:"object,omitempty"`
	Events          []KubernetesEventSnapshot `json:"events,omitempty"`
}

// ---------------------------------------------------------------------
// query_k8s_logs (cloud -> edge controller)
// ---------------------------------------------------------------------

// KubernetesPodLogsRequest asks the controller to read a bounded slice of one
// Pod's logs through the Kubernetes pods/log subresource. This is a small
// troubleshooting fallback, not the production log ingestion path.
type KubernetesPodLogsRequest struct {
	ClusterID    uint64 `json:"cluster_id"`
	Namespace    string `json:"namespace"`
	Pod          string `json:"pod"`
	Container    string `json:"container,omitempty"`
	Previous     bool   `json:"previous,omitempty"`
	SinceSeconds int64  `json:"since_seconds,omitempty"`
	TailLines    int    `json:"tail_lines,omitempty"`
	LimitBytes   int    `json:"limit_bytes,omitempty"`
	Timestamps   bool   `json:"timestamps,omitempty"`
}

// KubernetesPodLogsResponse carries bounded raw log text. Timestamps are left
// in the text when requested, matching the Kubernetes API output.
type KubernetesPodLogsResponse struct {
	ClusterID    uint64 `json:"cluster_id"`
	Namespace    string `json:"namespace"`
	Pod          string `json:"pod"`
	Container    string `json:"container,omitempty"`
	Previous     bool   `json:"previous,omitempty"`
	SinceSeconds int64  `json:"since_seconds,omitempty"`
	TailLines    int    `json:"tail_lines,omitempty"`
	LimitBytes   int    `json:"limit_bytes,omitempty"`
	Timestamps   bool   `json:"timestamps,omitempty"`
	FetchedAt    int64  `json:"fetched_at"`
	Bytes        int    `json:"bytes"`
	LineCount    int    `json:"line_count"`
	Truncated    bool   `json:"truncated,omitempty"`
	Logs         string `json:"logs"`
}

// ---------------------------------------------------------------------
// execute_k8s_action (cloud -> edge controller)
// ---------------------------------------------------------------------

// KubernetesActionRequest asks the cluster controller to perform one scoped
// Kubernetes write through the Kubernetes API. The manager-side tool is
// Class="write" so ReviewGate must approve before this request is dispatched.
type KubernetesActionRequest struct {
	ClusterID               uint64 `json:"cluster_id"`
	Action                  string `json:"action"` // rollout_restart, scale, delete_pod, evict_pod, cordon, uncordon, drain
	Kind                    string `json:"kind,omitempty"`
	APIVersion              string `json:"api_version,omitempty"`
	Namespace               string `json:"namespace,omitempty"`
	Name                    string `json:"name"`
	Replicas                *int   `json:"replicas,omitempty"`
	ExpectedResourceVersion string `json:"expected_resource_version,omitempty"`
	DryRun                  bool   `json:"dry_run,omitempty"`
	Reason                  string `json:"reason,omitempty"`
	GracePeriodSeconds      *int   `json:"grace_period_seconds,omitempty"`
	DrainTimeoutSeconds     int    `json:"drain_timeout_seconds,omitempty"`
	DrainRetrySeconds       int    `json:"drain_retry_seconds,omitempty"`
	IgnoreDaemonSets        *bool  `json:"ignore_daemonsets,omitempty"`
	DeleteEmptyDirData      bool   `json:"delete_emptydir_data,omitempty"`
	Force                   bool   `json:"force,omitempty"`
	DisableEviction         bool   `json:"disable_eviction,omitempty"`
}

// KubernetesActionPreflight is the live object metadata observed before a
// write. It lets the caller and audit trail prove the target still existed and
// which resourceVersion was checked for conflict protection.
type KubernetesActionPreflight struct {
	Kind            string `json:"kind"`
	APIVersion      string `json:"api_version,omitempty"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resource_version,omitempty"`
	Exists          bool   `json:"exists"`
}

// KubernetesActionPodResult captures per-pod drain outcomes so an audit record
// can explain which Pods were skipped or forced without storing full Pod specs.
type KubernetesActionPodResult struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Action    string `json:"action"`
	Reason    string `json:"reason,omitempty"`
}

// KubernetesActionResponse reports whether the controller only preflighted
// (dry_run) or applied a Kubernetes API write.
type KubernetesActionResponse struct {
	ClusterID             uint64                      `json:"cluster_id"`
	Action                string                      `json:"action"`
	Kind                  string                      `json:"kind"`
	APIVersion            string                      `json:"api_version,omitempty"`
	Namespace             string                      `json:"namespace,omitempty"`
	Name                  string                      `json:"name"`
	DryRun                bool                        `json:"dry_run,omitempty"`
	Applied               bool                        `json:"applied"`
	Preflight             KubernetesActionPreflight   `json:"preflight"`
	ResultResourceVersion string                      `json:"result_resource_version,omitempty"`
	StartedAt             int64                       `json:"started_at"`
	EndedAt               int64                       `json:"ended_at"`
	Message               string                      `json:"message,omitempty"`
	EvictedPodCount       int                         `json:"evicted_pod_count,omitempty"`
	DeletedPodCount       int                         `json:"deleted_pod_count,omitempty"`
	SkippedPodCount       int                         `json:"skipped_pod_count,omitempty"`
	SkippedPods           []KubernetesActionPodResult `json:"skipped_pods,omitempty"`
}

// ---------------------------------------------------------------------
// get_host_load (cloud -> edge)
// ---------------------------------------------------------------------

// GetHostLoadRequest has no fields; kept typed for symmetry.
type GetHostLoadRequest struct{}

// GetHostLoadResponse is a real-time load snapshot.
type GetHostLoadResponse struct {
	CPUPct float64 `json:"cpu_pct"`
	MemPct float64 `json:"mem_pct"`
	// DiskUsedPct is the root-filesystem usage percent (0..100).
	// Sourced from HostMetricPoint.DiskUsedPct (gopsutil disk usage on /
	// for embedded collector; node_filesystem_*_bytes for the scrape-mode
	// collector). Added to plug a real-world LLM mis-read where models
	// answered "disk usage = mem_pct" because no disk field was present
	// here — see session a16dec3d-1a3f-40b6-8fed-553b7b6cb9b9.
	DiskUsedPct float64 `json:"disk_used_pct"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	SampledAt   int64   `json:"sampled_at"` // unix seconds
}

// ---------------------------------------------------------------------
// get_process_list (cloud -> edge)
// ---------------------------------------------------------------------

// ProcessSortBy mirrors the proto enum names (string on wire).
const (
	ProcessSortByCPU = "cpu"
	ProcessSortByMem = "mem"
)

// ProcessInfo is one row in the top-N processes result.
type ProcessInfo struct {
	PID     int32   `json:"pid"`
	Name    string  `json:"name"`
	Cmdline string  `json:"cmdline"`
	CPUPct  float64 `json:"cpu_pct"`
	MemPct  float64 `json:"mem_pct"`
	User    string  `json:"user"`
}

// GetProcessListRequest asks for the top N processes sorted by cpu/mem.
type GetProcessListRequest struct {
	TopN   uint32 `json:"top_n"`
	SortBy string `json:"sort_by"` // "cpu" | "mem"
}

// GetProcessListResponse is the top-N result.
type GetProcessListResponse struct {
	Processes []ProcessInfo `json:"processes"`
	SampledAt int64         `json:"sampled_at"`
}

// ---------------------------------------------------------------------
// agent upgrade (manager -> edge)
// ---------------------------------------------------------------------

// AgentUpgradeRequest tells the edge agent to swap its own binary to
// the artifact at URL. The agent stream-downloads to a private staging
// area, verifies the artifact's sha256, atomically renames it to
// `/var/lib/ongrid-edge/.upgrade/pending`, then exits cleanly.
//
// On the next process start (driven by systemd Restart=always), the
// unit's ExecStartPre script (running as root) renames the staged
// binary over `/usr/local/bin/ongrid-edge`, backs up the previous
// binary to `.upgrade/previous`, and lets ExecStart run the new code.
//
// SHA256 is required and lower-hex (64 chars). URL is fetched without
// authentication today — the artifacts live behind nginx on the same
// manager the agent already trusts; revisit if we ever expose a CDN.
type AgentUpgradeRequest struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// AgentUpgradeResponse acks that the new binary is staged. The edge
// returns this BEFORE exiting so the manager sees a clean response;
// the actual restart is implicit (Restart=always picks the agent back
// up within ~5 s). Bytes is informational, useful for logs.
type AgentUpgradeResponse struct {
	StagedPath string `json:"staged_path"`
	Bytes      int64  `json:"bytes"`
}

// FetchPackageRequest tells the edge to download the full
// release tarball at URL, verify its outer SHA256, extract, then
// per-file sha-check every entry listed in the bundle's MANIFEST.txt.
// On success the edge has the new bundle fully staged under
// /var/lib/ongrid-edge/.upgrade/incoming/ but the swap hasn't happened
// yet — that's done by MethodApplyPackage so the manager can stagger
// stage and apply (e.g. stage all edges, then apply when the user
// clicks "go").
type FetchPackageRequest struct {
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`            // sha of the tarball
	Version string `json:"version,omitempty"` // optional; used by manifest VERSION file + ack
}

// FetchPackageResponse acks staging. ManifestFiles is the count of
// files the manifest declared; useful for the manager to surface
// "staged 6 files" in the UI without round-tripping again.
type FetchPackageResponse struct {
	StagedPath    string `json:"staged_path"`    // /var/lib/ongrid-edge/.upgrade/incoming
	Bytes         int64  `json:"bytes"`          // size of the tarball
	ManifestFiles int    `json:"manifest_files"` // entries in MANIFEST.txt
	Version       string `json:"version,omitempty"`
}

// ApplyPackageRequest signals the edge to exit so systemd's
// ExecStartPre apply-pending-upgrade.sh swaps the staged bundle in.
// Empty body — the staged bundle is the implicit target.
type ApplyPackageRequest struct{}

// ApplyPackageResponse acks that the edge has accepted the apply
// signal and will exit shortly. Apply is fire-and-forget from the
// manager's POV; the eventual outcome is observed via the new
// agent_version reported on next register (and the next-tick rollback
// if anything's broken).
type ApplyPackageResponse struct {
	Accepted bool `json:"accepted"`
}

// ---------------------------------------------------------------------
// meta (handshake blob, edge -> cloud only; never an RPC body)
// ---------------------------------------------------------------------

// Meta is what the client serializes into geminio's opaque Meta bytes
// on connect; the server decodes it before calling AuthFunc.
type Meta struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}
