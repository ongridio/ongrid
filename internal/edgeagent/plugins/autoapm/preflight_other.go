//go:build !linux

package autoapm

import (
	"context"
	"fmt"
)

func checkCaptureEnvironment(context.Context) error {
	return fmt.Errorf("OBI capture requires Linux")
}
