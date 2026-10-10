package autoapm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/k8s"
	"github.com/ongridio/ongrid/internal/edgeagent/plugins/traces"
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
		generation := p.resourceGeneration
		p.mu.Unlock()
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		samples, err := p.collectResources(rctx, spec, batch.Samples)
		cancel()
		p.mu.Lock()
		if p.resourceGeneration == generation {
			p.bindings = resourceBindings(samples)
		}
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
		// Enrich once per scrape, then split transport requests without limiting
		// the total sample count. Keep the scrape's final up sample last.
		for i, sample := range batch.Samples {
			if sample.Name == "up" && sample.Labels["plugin"] == "custommetrics" && sample.Labels["target_id"] == "autoapm" {
				copy(batch.Samples[i:], batch.Samples[i+1:])
				batch.Samples[len(batch.Samples)-1] = sample
				break
			}
		}
		// ponytail: a full scrape is retained for resource enrichment; use
		// scrape-scoped streaming identities if this becomes a memory bottleneck.
		accepted := 0
		for chunk := range slices.Chunk(batch.Samples, 1000) {
			request := batch
			request.Samples = chunk
			var result tunnel.PushPromSamplesResponse
			if err := p.pusher.Call(ctx, method, request, &result); err != nil {
				return err
			}
			accepted += result.Accepted
		}
		if result, ok := resp.(*tunnel.PushPromSamplesResponse); ok {
			result.Accepted = accepted
		}
		return nil
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
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, fmt.Errorf("read process boot identity: %w", err)
	}
	for _, labels := range identities {
		labels["process_boot_id"] = strings.TrimSpace(string(bootID))
	}
	var candidates []contract.Candidate
	var containers map[string]string
	var dockerErr error
	docker := map[string]traces.DockerProcess{}
	if spec.Kubernetes == nil {
		candidates, err = p.discover(ctx)
		if err == nil {
			for _, labels := range identities {
				name := labels["container_name"]
				if _, seen := docker[name]; name == "" || seen {
					continue
				}
				process, lookupErr := traces.DockerContainerProcess(ctx, name)
				if lookupErr != nil && !errors.Is(lookupErr, os.ErrNotExist) {
					if dockerErr == nil {
						dockerErr = lookupErr
					}
				}
				docker[name] = process
			}
		}
	} else {
		containers, err = k8s.NodeContainerIDs(ctx, os.Getenv("ONGRID_K8S_NODE_NAME"))
	}
	if err != nil {
		return nil, err
	}
	out, err := collectProcessResources(ctx, fs, spec, identities, candidates, containers, docker)
	return out, errors.Join(err, dockerErr)
}

// Retain resource identity only, never request paths, PID guesses from process
// names, or SDK/runtime dimensions. target_info also exists for idle services.
func resourceIdentities(samples []tunnel.PromSample) []map[string]string {
	out := []map[string]string{}
	seen := map[[7]string]bool{}
	for _, sample := range samples {
		if sample.Name != "target_info" || sample.Value <= 0 || sample.Labels["ongrid_instrumentation_source"] != "obi" {
			continue
		}
		labels := map[string]string{}
		for _, key := range []string{"service_name", "service_namespace", "deployment_environment_name", "service_instance_id", "service_version", "instance", "host_name", "container_name", "container_id", "cluster_id", "k8s_cluster_id", "k8s_pod_uid", "k8s_pod_name", "k8s_namespace_name", "k8s_container_name", "k8s_node_name", "k8s_deployment_name", "k8s_statefulset_name", "k8s_daemonset_name", "k8s_job_name", "k8s_cronjob_name"} {
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
		key := [7]string{labels["service_name"], labels["service_namespace"], labels["service_instance_id"], labels["service_version"], labels["deployment_environment_name"], labels["instance"], labels["container_id"]}
		if !seen[key] {
			seen[key] = true
			out = append(out, labels)
		}
	}
	return out
}

func collectProcessResources(ctx context.Context, fs procfs.FS, spec contract.Spec, identities []map[string]string, candidates []contract.Candidate, containers map[string]string, docker map[string]traces.DockerProcess) ([]tunnel.PromSample, error) {
	var firstErr error
	processIDs := map[int]string{}
	processErrors := map[int]error{}
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
		container := docker[labels["container_name"]]
		if labels["container_name"] != "" && container.PID <= 0 {
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
				pid := int(candidate.PID)
				if candidate.Executable != target.Executable || candidate.Port != target.Port || (container.PID > 0 && candidate.PID != container.PID) {
					continue
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				instanceID, read := processIDs[pid]
				if !read {
					proc, err := fs.Proc(pid)
					if err == nil {
						instanceID, err = processInstanceID(proc)
					}
					processIDs[pid], processErrors[pid] = instanceID, err
				}
				if err := processErrors[pid]; err != nil {
					if !errors.Is(err, os.ErrNotExist) && firstErr == nil {
						firstErr = fmt.Errorf("read PID %d resource identity: %w", pid, err)
					}
					continue
				}
				if instanceID != "" {
					if instanceID != labels["service_instance_id"] {
						continue
					}
				} else if container.PID == 0 {
					// OBI v0.12 exposes its process locator in instance; a custom
					// service.instance.id is opaque and must match the live environment.
					if labels["host_name"] == "" || labels["instance"] != fmt.Sprintf("%s:%d", labels["host_name"], pid) || labels["service_instance_id"] != labels["instance"] {
						continue
					}
				} else if labels["service_instance_id"] != labels["instance"] || (labels["container_id"] != "" && !strings.HasPrefix(container.ID, labels["container_id"])) {
					continue
				}
				bound := maps.Clone(labels)
				bound["instance"] = labels["service_instance_id"]
				bound["ongrid_target_id"] = target.ID()
				if container.ID != "" {
					// Resolve the live full ID; OBI target_info may retain a replaced container's short ID.
					bound["container_id"] = container.ID
					byContainer[container.ID] = bound
				} else if labels["container_name"] == "" {
					selected[pid] = bound
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

// Inspect only the SDK resource identity; never return or log application environment values.
func processInstanceID(proc procfs.Proc) (string, error) {
	environment, err := proc.Environ()
	if err != nil {
		return "", err
	}
	instanceID := ""
	for _, item := range environment {
		attributes, ok := strings.CutPrefix(item, "OTEL_RESOURCE_ATTRIBUTES=")
		if !ok {
			continue
		}
		for _, attribute := range strings.Split(attributes, ",") {
			key, value, ok := strings.Cut(attribute, "=")
			if ok && strings.TrimSpace(key) == "service.instance.id" {
				id, err := url.PathUnescape(strings.TrimSpace(value))
				if err != nil || len(id) > 1024 || strings.ContainsAny(id, "\x00\r\n") {
					return "", fmt.Errorf("invalid SDK service instance identity")
				}
				instanceID = id
			}
		}
	}
	return instanceID, nil
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
	labels["instance"] = labels["service_instance_id"]
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
