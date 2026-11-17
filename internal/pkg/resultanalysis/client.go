package resultanalysis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/runner"
)

const WorkerArg = "--internal-result-analysis"
const OutputLimitExitCode = 65
const Timeout = 5 * time.Second

type Client struct{ binary string }

func NewClient() (*Client, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("result analysis requires Linux memory limits")
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Client{binary: binary}, nil
}

func (c *Client) Analyze(ctx context.Context, request Request) (runner.Result, error) {
	if err := request.Validate(); err != nil {
		return runner.Result{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return runner.Result{}, err
	}
	if len(body)+1 > MaxHeaderBytes {
		return runner.Result{}, fmt.Errorf("analysis header exceeds limit")
	}
	readers := []io.Reader{bytes.NewReader(append(body, '\n'))}
	for _, input := range request.Inputs {
		if input.Content == nil {
			return runner.Result{}, fmt.Errorf("missing analysis input stream")
		}
		readers = append(readers, io.LimitReader(input.Content, input.Bytes))
	}
	res, err := runner.NewShellRunner().Run(ctx, runner.Spec{
		Argv: []string{c.binary, WorkerArg}, StdinReader: io.MultiReader(readers...),
		// Existing binary dependencies initialize caches before main; keep them in Runner's private cwd.
		Env:     map[string]string{"HOME": ".", "GOMEMLIMIT": "256MiB", "GOMAXPROCS": "1", "GOTRACEBACK": "none"},
		Timeout: Timeout, MaxOutputBytes: MaxOutputBytes,
	})
	if res.ExitCode == OutputLimitExitCode {
		res.Truncated = true
	}
	if res.ExitCode != 0 && (strings.Contains(res.Stderr, "out of memory") || strings.Contains(res.Stderr, "cannot allocate memory") || strings.Contains(res.Stderr, "pthread_create failed")) {
		res.Stderr = "Analysis exceeded its memory allocation limit. Repeated updates to a growing jq object can copy the object. For distinct keys or joins, collect only required fields and use group_by(key); do not repeat the same object-update plan in parallel."
	}
	return res, err
}
