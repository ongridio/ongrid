package autoapm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/k8s"
	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"github.com/prometheus/procfs"
)

const maxResourceProcesses = 1000

// Call decorates only this plugin's local OBI scrape. Resource collection uses
// the existing scraper lifecycle, timeout, tunnel, and push health reporting.
// The interface's any values are the tunnel request/response SDK boundary.
func (p *Plugin) Call(ctx context.Context, method string, req, resp any) error {
	if batch, ok := req.(tunnel.PushPromSamplesRequest); ok && method == tunnel.MethodPushPromSamples && batch.Source == "obi" {
		p.mu.Lock()
		spec := p.resourceSpec
		p.mu.Unlock()
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		samples, err := p.collectResources(rctx, spec, batch.Samples)
		cancel()
		p.mu.Lock()
		p.resourceError = ""
		if err != nil {
			p.resourceError = "process resources: " + err.Error()
		}
		p.mu.Unlock()
		// A permission/race error must not discard HTTP/RPC/runtime metrics or
		// other readable resources. The heartbeat and success metric expose it.
		batch.Samples = append(batch.Samples, samples...)
		success := 1.0
		if err != nil {
			success = 0
		}
		batch.Samples = append(batch.Samples, tunnel.PromSample{Name: "ongrid_apm_resource_collection_success", Value: success, TsMs: time.Now().UnixMilli()})
		req = batch
	}
	return p.pusher.Call(ctx, method, req, resp)
}

func (p *Plugin) collectResources(ctx context.Context, spec contract.Spec, samples []tunnel.PromSample) ([]tunnel.PromSample, error) {
	identities := resourceIdentities(samples)
	if !spec.Selected() || len(identities) == 0 {
		return nil, nil
	}
	fs, err := procfs.NewFS("/proc")
	if err != nil {
		return nil, err
	}
	var candidates []contract.Candidate
	var containers map[string]string
	if spec.Kubernetes == nil {
		candidates, err = p.discover(ctx)
	} else {
		containers, err = k8s.NodeContainerIDs(ctx, os.Getenv("ONGRID_K8S_NODE_NAME"))
	}
	if err != nil {
		return nil, err
	}
	return collectProcessResources(ctx, fs, spec, identities, candidates, containers)
}

// Retain resource identity only, never request paths, PID guesses from process
// names, or SDK/runtime dimensions. target_info also exists for idle services.
func resourceIdentities(samples []tunnel.PromSample) []map[string]string {
	out := []map[string]string{}
	seen := map[[5]string]bool{}
	for _, sample := range samples {
		if sample.Name != "target_info" || sample.Value <= 0 || sample.Labels["ongrid_instrumentation_source"] != "obi" {
			continue
		}
		labels := map[string]string{}
		for _, key := range []string{"service_name", "service_namespace", "deployment_environment_name", "service_instance_id", "service_version", "instance", "host_name", "cluster_id", "k8s_cluster_id", "k8s_pod_uid", "k8s_pod_name", "k8s_namespace_name", "k8s_container_name", "k8s_node_name", "k8s_deployment_name", "k8s_statefulset_name", "k8s_daemonset_name", "k8s_job_name", "k8s_cronjob_name"} {
			if v := sample.Labels[key]; v != "" {
				labels[key] = v
			}
		}
		if labels["service_instance_id"] == "" {
			labels["service_instance_id"] = labels["instance"]
		}
		if labels["service_name"] == "" {
			ns, name, found := strings.Cut(sample.Labels["job"], "/")
			if found {
				labels["service_namespace"], labels["service_name"] = ns, name
			} else {
				labels["service_name"] = ns
			}
		}
		if labels["k8s_namespace_name"] != "" {
			labels["service_namespace"] = labels["k8s_namespace_name"]
		}
		if labels["service_name"] == "" || labels["service_instance_id"] == "" {
			continue
		}
		labels["instance"] = labels["service_instance_id"]
		key := [5]string{labels["service_name"], labels["service_namespace"], labels["service_instance_id"], labels["service_version"], labels["deployment_environment_name"]}
		if !seen[key] {
			seen[key] = true
			out = append(out, labels)
		}
	}
	return out
}

func collectProcessResources(ctx context.Context, fs procfs.FS, spec contract.Spec, identities []map[string]string, candidates []contract.Candidate, containers map[string]string) ([]tunnel.PromSample, error) {
	selected := map[int]map[string]string{}
	executables := map[int]string{}
	byContainer := map[string]map[string]string{}
	for _, labels := range identities {
		if spec.Kubernetes != nil {
			if labels["deployment_environment_name"] != spec.Environment {
				continue
			}
			for _, rule := range spec.Kubernetes.Rules {
				attr := strings.ReplaceAll(contract.WorkloadAttribute(rule.WorkloadKind), ".", "_")
				if labels["k8s_namespace_name"] != rule.Namespace || (attr != "" && labels[attr] != rule.WorkloadName) {
					continue
				}
				if id := containers[labels["k8s_pod_uid"]+"/"+labels["k8s_container_name"]]; id != "" {
					byContainer[id] = labels
				}
			}
			continue
		}
		// Host instance IDs are emitted by OBI as hostname:PID. Cross-check the
		// PID against fresh executable+port discovery, including interpreter apps.
		host := labels["host_name"]
		if host == "" || !strings.HasPrefix(labels["service_instance_id"], host+":") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(labels["service_instance_id"], host+":"))
		if err != nil || pid <= 0 {
			continue
		}
		for _, target := range spec.Targets {
			environment := spec.Environment
			if target.Environment != "" {
				environment = target.Environment
			}
			if labels["deployment_environment_name"] != environment {
				continue
			}
			if target.ServiceName != labels["service_name"] || target.ServiceNamespace != labels["service_namespace"] {
				continue
			}
			for _, candidate := range candidates {
				if int(candidate.PID) == pid && candidate.Executable == target.Executable && candidate.Port == target.Port {
					selected[pid] = labels
					executables[pid] = target.Executable
				}
			}
		}
	}
	if len(byContainer) > 0 {
		procs, err := fs.AllProcs()
		if err != nil {
			return nil, fmt.Errorf("list container processes: %w", err)
		}
		for _, proc := range procs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			groups, err := proc.Cgroups()
			if errors.Is(err, os.ErrNotExist) {
				continue
			} // process exited during scan
			if err != nil {
				return nil, fmt.Errorf("read PID %d cgroup: %w", proc.PID, err)
			}
			for _, group := range groups {
				for _, part := range strings.Split(group.Path, "/") {
					// Both cgroupfs and systemd scope layouts; match a full runtime
					// container ID, never a pod-level cgroup or a name prefix.
					id := strings.TrimSuffix(part, ".scope")
					if at := strings.LastIndexByte(id, '-'); at >= 0 {
						id = id[at+1:]
					}
					if labels := byContainer[id]; labels != nil {
						selected[proc.PID] = labels
					}
				}
			}
		}
	}
	if len(selected) > maxResourceProcesses {
		return nil, fmt.Errorf("process resource limit exceeded: %d", len(selected))
	}
	out := []tunnel.PromSample{}
	var firstErr error
	now := time.Now().UnixMilli()
	for pid, labels := range selected {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		proc, err := fs.Proc(pid)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				firstErr = err
			}
			continue
		}
		if expected := executables[pid]; expected != "" {
			exe, err := proc.Executable()
			if err != nil || exe != expected {
				continue
			} // exited or reused after discovery
		}
		samples, err := readProcessResources(ctx, proc, labels, now)
		out = append(out, samples...)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("PID %d: %w", pid, err)
		}
	}
	return out, firstErr
}

func readProcessResources(ctx context.Context, proc procfs.Proc, identity map[string]string, now int64) ([]tunnel.PromSample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stat, err := proc.Stat()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	labels := maps.Clone(identity)
	labels["process_pid"] = strconv.Itoa(proc.PID)
	// A reused PID must start a different counter series, including when a
	// container keeps the same service instance name across a process restart.
	labels["process_start_ticks"] = strconv.FormatUint(stat.Starttime, 10)
	out := []tunnel.PromSample{}
	add := func(name string, value float64) {
		out = append(out, tunnel.PromSample{Name: "ongrid_apm_process_" + name, Labels: labels, Value: value, TsMs: now})
	}
	add("cpu_seconds_total", stat.CPUTime())
	add("resident_memory_bytes", float64(stat.ResidentMemory()))
	add("virtual_memory_bytes", float64(stat.VirtualMemory()))
	add("threads", float64(stat.NumThreads))
	fds, fdErr := proc.FileDescriptorsLen()
	if fdErr == nil {
		add("open_fds", float64(fds))
	}
	io, ioErr := proc.IO()
	if ioErr == nil {
		add("io_read_bytes_total", float64(io.ReadBytes))
		add("io_write_bytes_total", float64(io.WriteBytes))
	}
	end, err := proc.Stat()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if end.Starttime != stat.Starttime {
		return nil, nil
	} // PID reused during read
	err = errors.Join(fdErr, ioErr)
	if err == nil {
		add("scrape_success", 1)
	} else {
		add("scrape_success", 0)
	}
	return out, err
}
