package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/ongridio/ongrid/internal/pkg/httpserver"
)

func runEdgeMetrics(ctx context.Context, handler http.Handler, log *slog.Logger) error {
	addr := strings.TrimSpace(os.Getenv("ONGRID_EDGE_METRICS_ADDR"))
	automatic := addr == ""
	if automatic {
		addr = edgeMetricsAddr
	}
	ln, err := listenEdgeMetrics(ctx, addr, automatic, log)
	if err != nil {
		return err
	}
	return httpserver.New(addr, handler, log).StartListener(ctx, ln)
}

func listenEdgeMetrics(ctx context.Context, addr string, automatic bool, log *slog.Logger) (net.Listener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("edge metrics address %q: %w", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("edge metrics address %q: port must be 1..65535", addr)
	}
	mode := "fixed"
	if automatic {
		mode = "auto"
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	reason := ""
	// Windows sockets report WSAEADDRINUSE, distinct from syscall.EADDRINUSE.
	inUse := errors.Is(err, syscall.EADDRINUSE) || (runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10048)))
	if automatic && inUse {
		bindErr := err
		ln, err = lc.Listen(ctx, "tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return nil, fmt.Errorf("edge metrics bind %q and automatic allocation failed: %w", addr, errors.Join(bindErr, err))
		}
		reason = "address_in_use"
	}
	if err != nil {
		return nil, fmt.Errorf("edge metrics bind %q (%s): %w", addr, mode, err)
	}
	log.Info("edge diagnostics listener bound", "requested_addr", addr,
		"actual_addr", ln.Addr().String(), "mode", mode, "fallback_reason", reason)
	return ln, nil
}
