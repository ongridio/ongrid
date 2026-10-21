//go:build windows

package runner

import (
	"errors"
	"os"
	"os/exec"
)

func configureProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
