// Package httpserver wraps net/http.Server with graceful shutdown honoring
// a parent context. Used by cmd/* for the API, metrics and debug listeners.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Server composes *http.Server with the logger used for lifecycle events.
type Server struct {
	*http.Server
	log *slog.Logger
}

// New constructs a Server. The returned *Server is ready to be Start'd.
func New(addr string, h http.Handler, log *slog.Logger) *Server {
	return &Server{
		Server: &http.Server{
			Addr:              addr,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
		},
		log: log,
	}
}

// Start blocks until serving returns an error or ctx is
// cancelled. On ctx cancel it triggers Shutdown with a 10s deadline.
// Returns nil on clean shutdown.
func (s *Server) Start(ctx context.Context) error {
	addr := s.Addr
	if addr == "" {
		addr = ":http"
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return s.StartListener(ctx, ln)
}

// StartListener takes ownership of an already-bound listener and shuts it down
// with ctx. The listener is never released and rebound between selection and serving.
func (s *Server) StartListener(ctx context.Context, ln net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", slog.String("addr", ln.Addr().String()))
		err := s.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Shutdown(shutdownCtx); err != nil {
			s.log.Error("http server shutdown error", slog.String("addr", s.Addr), slog.Any("err", err))
			return err
		}
		// Serve also closes ln when cancellation wins before it starts.
		<-errCh
		s.log.Info("http server shutdown complete", slog.String("addr", s.Addr))
		return nil
	case err := <-errCh:
		return err
	}
}
