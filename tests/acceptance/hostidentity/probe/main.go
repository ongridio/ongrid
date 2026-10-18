// hostidentity probe uses a real authenticated tunnel with an isolated synthetic Host.
// It never executes shell commands; runtime responses identify the installation selected.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"os"
	"sync"
	"time"
)

type config struct {
	Address    string `json:"address"`
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	CA         string `json:"ca"`
	ServerName string `json:"server_name"`
	Marker     string `json:"marker"`
}
type command struct {
	Method string          `json:"method"`
	Body   json.RawMessage `json:"body"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	if !scanner.Scan() {
		return fmt.Errorf("configuration missing")
	}
	var cfg config
	if err := json.Unmarshal(scanner.Bytes(), &cfg); err != nil {
		return err
	}
	client := tunnel.NewClient(tunnel.ClientConfig{ServerAddr: cfg.Address, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, TLSCAFile: cfg.CA, TLSServerName: cfg.ServerName})
	defer func() {
		if err := client.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "close tunnel:", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := client.Dial(ctx); err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	var mu sync.Mutex
	calls := []string{}
	for _, method := range []string{tunnel.MethodGetHostLoad, tunnel.MethodGetProcessList, tunnel.MethodExecuteSkill} {
		client.RegisterHandler(method, func(_ context.Context, _ tunnel.Session, method string, _ []byte) ([]byte, error) {
			mu.Lock()
			calls = append(calls, method)
			mu.Unlock()
			switch method {
			case tunnel.MethodGetHostLoad:
				return json.Marshal(tunnel.GetHostLoadResponse{CPUPct: 23, MemPct: 41, DiskUsedPct: 17, SampledAt: time.Now().Unix()})
			case tunnel.MethodExecuteSkill:
				return json.Marshal(tunnel.ExecuteSkillResponse{Result: json.RawMessage(fmt.Sprintf(`{"installation":%q}`, cfg.Marker))})
			default:
				return json.Marshal(map[string]any{"processes": []any{}, "installation": cfg.Marker})
			}
		})
	}
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(map[string]bool{"ready": true}); err != nil {
		return err
	}
	for scanner.Scan() {
		var cmd command
		if err := json.Unmarshal(scanner.Bytes(), &cmd); err != nil {
			return err
		}
		if cmd.Method == "probe.calls" {
			mu.Lock()
			snapshot := append([]string{}, calls...)
			mu.Unlock()
			if err := encoder.Encode(snapshot); err != nil {
				return err
			}
			continue
		}
		callCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		var result json.RawMessage
		err := client.Call(callCtx, cmd.Method, cmd.Body, &result)
		done()
		out := map[string]any{"result": result}
		if err != nil {
			out["error"] = err.Error()
		}
		if err := encoder.Encode(out); err != nil {
			return err
		}
	}
	return scanner.Err()
}
