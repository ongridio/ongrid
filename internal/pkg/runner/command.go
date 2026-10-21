package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// StartCommand starts a context-bound command. The returned wait function must
// be called once, including when another command in a pipeline fails to start.
// Output writers must not block indefinitely and must be safe for concurrent
// writes if the same writer is used for both stdout and stderr.
func StartCommand(ctx context.Context, cmd *exec.Cmd) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configureProcessGroup(cmd)
	// Our cancellation stays active while draining output, even after the
	// direct child exits and os/exec stops watching its context.
	cmd.Cancel = nil
	cmd.WaitDelay = time.Second

	type outputPipe struct {
		reader, writer *os.File
		dst            io.Writer
	}
	var pipes []outputPipe
	closePipes := func() {
		for _, p := range pipes {
			// Closing already-closed private pipes is harmless during teardown.
			_ = p.reader.Close()
			_ = p.writer.Close()
		}
	}
	for _, dst := range []*io.Writer{&cmd.Stdout, &cmd.Stderr} {
		if *dst == nil {
			continue
		}
		if _, ok := (*dst).(*os.File); ok {
			continue
		}
		r, w, err := os.Pipe()
		if err != nil {
			closePipes()
			return nil, fmt.Errorf("runner: output pipe: %w", err)
		}
		pipes = append(pipes, outputPipe{reader: r, writer: w, dst: *dst})
		*dst = w
	}
	if err := cmd.Start(); err != nil {
		closePipes()
		return nil, err
	}
	copied := make(chan error, len(pipes))
	for _, p := range pipes {
		// Only the child owns the write end after Start.
		_ = p.writer.Close()
		go func() {
			_, err := io.Copy(p.dst, p.reader)
			copied <- err
		}()
	}

	done := make(chan struct{})
	canceled := make(chan struct{})
	var cancelErr error
	stop := context.AfterFunc(ctx, func() {
		defer close(canceled)
		cancelErr = killProcessGroup(cmd)
		timer := time.NewTimer(cmd.WaitDelay)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			// A descendant can leave the group but keep a pipe open. Bound
			// drainage only after cancellation, not after a normal shell exit.
			closePipes()
		}
	})
	return func() error {
		waitErr := cmd.Wait()
		for range pipes {
			if err := <-copied; err != nil {
				waitErr = errors.Join(waitErr, err)
			}
		}
		close(done)
		if !stop() {
			<-canceled
		}
		// Also clean up background children that redirected their output.
		cleanupErr := killProcessGroup(cmd)
		closePipes()
		if err := ctx.Err(); err != nil {
			return errors.Join(err, waitErr, cancelErr, cleanupErr)
		}
		if cancelErr != nil || cleanupErr != nil {
			return errors.Join(waitErr, cancelErr, cleanupErr)
		}
		return waitErr
	}, nil
}
