package autoapm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"

	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/prometheus/procfs"
)

func discover(ctx context.Context) ([]contract.Candidate, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("automatic APM requires Linux")
	}
	fs, err := procfs.NewFS("/proc")
	if err != nil {
		return nil, err
	}
	procs, err := fs.AllProcs()
	if err != nil {
		return nil, fmt.Errorf("discover listening processes: %w", err)
	}
	out := make([]contract.Candidate, 0)
	portsByNamespace := map[string]map[string]uint16{}
	for _, proc := range procs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		exe, err := proc.Executable()
		if err != nil || exe == "" || contract.Excluded(exe) {
			// Kernel threads, inaccessible processes and processes that exited
			// during discovery are not candidates.
			continue
		}
		fds, err := proc.FileDescriptorTargets()
		if err != nil {
			if os.IsNotExist(err) || os.IsPermission(err) {
				continue
			}
			return out, fmt.Errorf("read PID %d sockets: %w", proc.PID, err)
		}
		root := filepath.Join("/proc", strconv.Itoa(proc.PID))
		namespace, err := os.Readlink(filepath.Join(root, "ns/net"))
		if os.IsNotExist(err) || os.IsPermission(err) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("read PID %d network namespace: %w", proc.PID, err)
		}
		ports, ok := portsByNamespace[namespace]
		if !ok {
			// Read each network namespace once; host /proc/net excludes bridge containers.
			network, err := procfs.NewFS(root)
			if err != nil {
				if os.IsNotExist(err) || os.IsPermission(err) {
					continue
				}
				return out, err
			}
			ports, err = listeningSockets(network)
			if os.IsNotExist(err) || os.IsPermission(err) {
				continue
			}
			if err != nil {
				return out, err
			}
			portsByNamespace[namespace] = ports
		}
		seen := map[uint16]bool{}
		for _, fd := range fds {
			if port := ports[fd]; port != 0 && !seen[port] {
				seen[port] = true
				out = append(out, contract.Candidate{Executable: exe, Port: port, PID: int32(proc.PID)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Executable != out[j].Executable {
			return out[i].Executable < out[j].Executable
		}
		if out[i].PID != out[j].PID {
			return out[i].PID < out[j].PID
		}
		return out[i].Port < out[j].Port
	})
	return out, nil
}

func listeningSockets(fs procfs.FS) (map[string]uint16, error) {
	connections, err := fs.NetTCP()
	if err != nil {
		return nil, fmt.Errorf("read listening sockets: %w", err)
	}
	ipv6, err := fs.NetTCP6()
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read IPv6 listening sockets: %w", err)
	}
	// Keep every socket inode for SO_REUSEPORT and inherited worker sockets.
	ports := map[string]uint16{}
	for _, conn := range append(connections, ipv6...) {
		if conn.St == 0x0a && conn.LocalPort > 0 && conn.LocalPort <= 65535 {
			ports[fmt.Sprintf("socket:[%d]", conn.Inode)] = uint16(conn.LocalPort)
		}
	}
	return ports, nil
}
