//go:build !linux

package hostruntime

import (
	"context"
	"fmt"
)

func Enter(context.Context, string, int, int) error {
	return fmt.Errorf("entering the kubernetes host is supported only on linux")
}

func PrepareOBIFilesystem(context.Context, string, int, int) error {
	return fmt.Errorf("OBI requires Linux bpffs")
}
