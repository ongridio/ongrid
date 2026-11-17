package resultanalysis

import (
	"fmt"
	"runtime/debug"

	"golang.org/x/sys/unix"
)

func restrictMemory() error {
	debug.SetMemoryLimit(256 << 20)
	for resource, limit := range map[int]uint64{unix.RLIMIT_CORE: 0, unix.RLIMIT_DATA: 384 << 20} {
		if err := unix.Setrlimit(resource, &unix.Rlimit{Cur: limit, Max: limit}); err != nil {
			return fmt.Errorf("set analysis resource limit: %w", err)
		}
	}
	return nil
}
