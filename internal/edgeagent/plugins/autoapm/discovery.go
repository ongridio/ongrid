package autoapm

import (
	"context"
	"fmt"
	"runtime"
	"sort"

	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

func discover(ctx context.Context) ([]contract.Candidate, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("automatic APM requires Linux")
	}
	connections, err := gnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, fmt.Errorf("discover listening processes: %w", err)
	}
	seen := map[string]bool{}
	paths := map[int32]string{}
	out := make([]contract.Candidate, 0)
	for _, conn := range connections {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if conn.Status != "LISTEN" || conn.Pid <= 0 || conn.Laddr.Port == 0 || conn.Laddr.Port > 65535 {
			continue
		}
		exe, ok := paths[conn.Pid]
		if !ok {
			p, err := process.NewProcessWithContext(ctx, conn.Pid)
			if err != nil {
				continue
			}
			exe, err = p.ExeWithContext(ctx)
			if err != nil {
				continue
			}
			paths[conn.Pid] = exe
		}
		if exe == "" || contract.Excluded(exe) {
			continue
		}
		key := fmt.Sprintf("%s:%d", exe, conn.Laddr.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, contract.Candidate{Executable: exe, Port: uint16(conn.Laddr.Port), PID: conn.Pid})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Executable != out[j].Executable {
			return out[i].Executable < out[j].Executable
		}
		return out[i].Port < out[j].Port
	})
	if len(out) > contract.MaxCandidates {
		return out[:contract.MaxCandidates], fmt.Errorf("discovery limited to %d executable/port groups", contract.MaxCandidates)
	}
	return out, nil
}
