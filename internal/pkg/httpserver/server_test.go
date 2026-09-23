package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestListenerLifecycle(t *testing.T) {
	for _, preCancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		s := New(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), log)
		if preCancelled {
			cancel()
		}
		done := make(chan error, 1)
		go func() { done <- s.StartListener(ctx, ln) }()
		if !preCancelled {
			client := &http.Client{Timeout: time.Second}
			res, err := client.Get("http://" + addr)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			res.Body.Close()
			client.CloseIdleConnections()
			if res.StatusCode != 204 {
				cancel()
				t.Fatal(res.StatusCode)
			}
			// Existing Start callers still fail on occupied fixed addresses.
			if err := New(addr, nil, log).Start(ctx); err == nil {
				cancel()
				t.Fatal("occupied address accepted")
			}
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("shutdown timed out")
		}
		rebound, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		rebound.Close()
	}
}
