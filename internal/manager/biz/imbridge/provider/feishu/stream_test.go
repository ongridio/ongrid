package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// StreamClient.Run delegates to SDK Start. Exercise the real dependency so
// an SDK regression cannot silently restore the uninterruptible stream loop.
func TestStreamSDKCancellation(t *testing.T) {
	for _, state := range []string{"before_start", "bootstrap", "connected", "reconnecting"} {
		t.Run(state, func(t *testing.T) {
			var attempts atomic.Int32
			bootstrap := make(chan struct{}, 1)
			connected := make(chan *websocket.Conn, 1)
			disconnected := make(chan struct{}, 1)
			retrying := make(chan struct{}, 1)
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/callback/ws/endpoint":
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						t.Errorf("read bootstrap request: %v", err)
						return
					}
					attempt := attempts.Add(1)
					if state == "bootstrap" {
						bootstrap <- struct{}{}
						<-r.Context().Done()
						return
					}
					if attempt > 1 {
						http.Error(w, "retry later", http.StatusServiceUnavailable)
						select {
						case retrying <- struct{}{}:
						default:
						}
						return
					}
					if err := json.NewEncoder(w).Encode(larkws.EndpointResp{
						Data: &larkws.Endpoint{
							Url: "ws://" + r.Host + "/ws?device_id=test&service_id=1",
							ClientConfig: &larkws.ClientConfig{
								ReconnectCount: -1, ReconnectInterval: 3600, PingInterval: 3600,
							},
						},
					}); err != nil {
						t.Errorf("write endpoint: %v", err)
					}
				case "/ws":
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						t.Errorf("upgrade websocket: %v", err)
						return
					}
					t.Cleanup(func() { conn.Close() })
					defer conn.Close()
					// A ping proves that SDK Start completed connection setup.
					if _, _, err := conn.ReadMessage(); err != nil {
						t.Errorf("read initial ping: %v", err)
						return
					}
					connected <- conn
					for {
						if _, _, err := conn.ReadMessage(); err != nil {
							disconnected <- struct{}{}
							return
						}
					}
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			client := larkws.NewClient("test-app", "test-secret",
				larkws.WithDomain(srv.URL),
				larkws.WithEventHandler(dispatcher.NewEventDispatcher("", "")),
				larkws.WithAutoReconnect(true),
			)
			if state == "before_start" {
				cancel()
			}
			done := make(chan error, 1)
			go func() { done <- client.Start(ctx) }()
			switch state {
			case "bootstrap":
				waitStreamEvent(t, bootstrap)
			case "connected", "reconnecting":
				conn := waitStreamEvent(t, connected)
				if state == "reconnecting" {
					if err := conn.Close(); err != nil {
						t.Fatalf("disconnect websocket: %v", err)
					}
					waitStreamEvent(t, retrying)
				}
			}
			cancel()
			if err := waitStreamEvent(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("Start error = %v, want context.Canceled", err)
			}
			if state == "connected" || state == "reconnecting" {
				waitStreamEvent(t, disconnected)
			}
			if state == "before_start" && attempts.Load() != 0 {
				t.Fatal("cancelled client requested a connection endpoint")
			}
		})
	}
}

func waitStreamEvent[T any](t *testing.T, event <-chan T) T {
	t.Helper()
	select {
	case value := <-event:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream lifecycle event")
		var zero T
		return zero
	}
}
