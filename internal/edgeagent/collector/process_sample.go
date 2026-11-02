package collector

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/process"
)

func waitCPUSample(ctx context.Context) error {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// 进程 CPU 使用整个主机的逻辑 CPU 容量为分母，与主机利用率保持同一基准。
func processCPUPercent(before, after, seconds float64, cores int) (float64, string) {
	if seconds <= 0 || cores <= 0 || after < before {
		return 0, "unavailable"
	}
	return (after - before) / seconds / float64(cores) * 100, "valid"
}

func collectHostProcesses(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error) {
	resp := tunnel.GetProcessListResponse{CPUBasis: "host_total", Processes: []tunnel.ProcessInfo{}}
	if err := ctx.Err(); err != nil {
		return resp, err
	}
	cores, err := cpu.CountsWithContext(ctx, true)
	if err != nil {
		return resp, fmt.Errorf("CPU count: %w", err)
	}
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return resp, fmt.Errorf("processes: %w", err)
	}
	type sample struct {
		pid     int32
		created int64
		total   float64
		at      time.Time
	}
	samples := make([]sample, 0, len(procs))
	for _, p := range procs {
		created, err := p.CreateTimeWithContext(ctx)
		if err != nil {
			continue
		} // 进程可能已退出或不可读。
		times, err := p.TimesWithContext(ctx)
		if err != nil {
			continue
		}
		samples = append(samples, sample{p.Pid, created, times.User + times.System, time.Now()})
	}
	if err := waitCPUSample(ctx); err != nil {
		return resp, err
	}
	type measured struct {
		p   *process.Process
		row tunnel.ProcessInfo
	}
	measuredRows := make([]measured, 0, len(samples))
	for _, before := range samples {
		if err := ctx.Err(); err != nil {
			return resp, err
		}
		p, err := process.NewProcessWithContext(ctx, before.pid)
		if err != nil {
			continue
		}
		created, err := p.CreateTimeWithContext(ctx)
		if err != nil || created != before.created {
			continue
		} // PID 复用不能拼接成同一进程。
		times, err := p.TimesWithContext(ctx)
		if err != nil {
			continue
		}
		elapsed := time.Since(before.at).Seconds()
		value, status := processCPUPercent(before.total, times.User+times.System, elapsed, cores)
		if elapsed > resp.CPUWindowSeconds {
			resp.CPUWindowSeconds = elapsed
		}
		measuredRows = append(measuredRows, measured{p, tunnel.ProcessInfo{PID: p.Pid, CPUPct: value, CPUSampleStatus: status}})
	}
	// 先结束全部 CPU 采样，再读较慢的命令行和用户名，避免窗口被元数据读取拉长。
	for _, m := range measuredRows {
		if err := ctx.Err(); err != nil {
			return resp, err
		}
		if value, err := m.p.NameWithContext(ctx); err == nil {
			m.row.Name = value
		}
		if value, err := m.p.CmdlineWithContext(ctx); err == nil {
			m.row.Cmdline = value
		}
		if value, err := m.p.UsernameWithContext(ctx); err == nil {
			m.row.User = value
		}
		if value, err := m.p.MemoryPercentWithContext(ctx); err == nil {
			m.row.MemPct = float64(value)
		}
		resp.Processes = append(resp.Processes, m.row)
	}
	sort.Slice(resp.Processes, func(i, j int) bool {
		a, b := resp.Processes[i], resp.Processes[j]
		if sortBy == tunnel.ProcessSortByMem {
			if a.MemPct != b.MemPct {
				return a.MemPct > b.MemPct
			}
		} else if a.CPUPct != b.CPUPct {
			return a.CPUPct > b.CPUPct
		}
		return a.PID < b.PID
	})
	if topN <= 0 {
		topN = 20
	}
	if len(resp.Processes) > topN {
		resp.Processes = resp.Processes[:topN]
	}
	resp.SampledAt = time.Now().Unix()
	return resp, nil
}
