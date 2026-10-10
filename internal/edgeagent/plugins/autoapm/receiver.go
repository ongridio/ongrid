package autoapm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/traces"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"github.com/prometheus/procfs"
)

type processBinding struct {
	PID           int32
	StartTicks    uint64
	TargetID      string
	ContainerName string
	ObservedAt    int64
}

func resourceBindings(samples []tunnel.PromSample) map[[4]string][]processBinding {
	out := map[[4]string][]processBinding{}
	for _, sample := range samples {
		if sample.Name != "ongrid_apm_process_scrape_success" || sample.Value != 1 || sample.Labels["ongrid_target_id"] == "" {
			continue
		}
		labels := sample.Labels
		pid, pidErr := strconv.ParseInt(labels["process_pid"], 10, 32)
		start, startErr := strconv.ParseUint(labels["process_start_ticks"], 10, 64)
		if pidErr != nil || startErr != nil || pid <= 0 {
			continue
		}
		key := [4]string{labels["service_name"], labels["service_namespace"], labels["deployment_environment_name"], labels["service_instance_id"]}
		out[key] = append(out[key], processBinding{PID: int32(pid), StartTicks: start, TargetID: labels["ongrid_target_id"], ContainerName: labels["container_name"], ObservedAt: sample.TsMs})
	}
	return out
}

// ApplicationReceiver uses the current scrape's process binding, never an ID's string format.
func (p *Plugin) ApplicationReceiver(ctx context.Context, configPath string, request tunnel.ApplicationReceiverRequest) (tunnel.ApplicationReceiverResponse, error) {
	fs, err := procfs.NewFS("/proc")
	if err != nil {
		return tunnel.ApplicationReceiverResponse{}, err
	}
	processID, targetID, start, err := p.receiverProcess(ctx, fs, request)
	if errors.Is(err, os.ErrNotExist) {
		return tunnel.ApplicationReceiverResponse{Reason: "process_unavailable"}, nil
	}
	if err != nil {
		return tunnel.ApplicationReceiverResponse{}, err
	}
	receiver, err := traces.ApplicationReceiver(ctx, configPath, tunnel.ApplicationReceiverRequest{ProcessID: processID})
	if err != nil {
		return receiver, err
	}
	proc, err := fs.Proc(int(processID))
	if err == nil {
		var stat procfs.ProcStat
		stat, err = proc.Stat()
		if err == nil && stat.Starttime != start {
			err = os.ErrNotExist
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return tunnel.ApplicationReceiverResponse{Reason: "process_unavailable"}, nil
	}
	if err != nil {
		return tunnel.ApplicationReceiverResponse{}, fmt.Errorf("verify receiver process: %w", err)
	}
	receiver.TargetID = targetID
	return receiver, nil
}

func (p *Plugin) receiverProcess(ctx context.Context, fs procfs.FS, request tunnel.ApplicationReceiverRequest) (int32, string, uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", 0, err
	}
	key := [4]string{request.ServiceName, request.Namespace, request.Environment, request.InstanceID}
	p.mu.Lock()
	bindings := append([]processBinding(nil), p.bindings[key]...)
	p.mu.Unlock()
	if len(bindings) == 0 {
		return 0, "", 0, os.ErrNotExist
	}
	containerName := bindings[0].ContainerName
	if request.ContainerName != "" && request.ContainerName != containerName {
		return 0, "", 0, os.ErrNotExist
	}
	var container traces.DockerProcess
	if containerName != "" {
		var err error
		container, err = traces.DockerContainerProcess(ctx, containerName)
		if err != nil {
			return 0, "", 0, err
		}
	} else if len(bindings) != 1 {
		// A shared SDK instance ID cannot identify independent native workers.
		return 0, "", 0, os.ErrNotExist
	}
	for _, binding := range bindings {
		if binding.ContainerName != containerName || (containerName != "" && container.PID != binding.PID) || time.Since(time.UnixMilli(binding.ObservedAt)) > 30*time.Second {
			continue
		}
		proc, err := fs.Proc(int(binding.PID))
		if err != nil {
			return 0, "", 0, err
		}
		stat, err := proc.Stat()
		if err != nil {
			return 0, "", 0, err
		}
		if stat.Starttime == binding.StartTicks {
			return binding.PID, binding.TargetID, binding.StartTicks, nil
		}
	}
	return 0, "", 0, os.ErrNotExist
}
