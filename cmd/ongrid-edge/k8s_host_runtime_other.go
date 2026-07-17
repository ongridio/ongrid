//go:build !linux

package main

import (
	"context"
	"fmt"
)

func enterK8sHost(context.Context, string, int, int) error {
	return fmt.Errorf("entering the kubernetes host is supported only on linux")
}
