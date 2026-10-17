package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListPaginationAndToolChangeNotification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.Header.Get("Mcp-Session-Id") != "session" {
				t.Error("watch lost session")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if _, err := fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n"); err != nil {
				t.Error(err)
			}
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session")
			writeJSONRPC(w, req.ID, map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{"listChanged": true}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if req.Params.Cursor == "" {
				writeJSONRPC(w, req.ID, map[string]any{"tools": []Tool{{Name: "first"}}, "nextCursor": "page2"})
			} else {
				writeJSONRPC(w, req.ID, map[string]any{"tools": []Tool{{Name: "second"}}})
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := NewHTTP(server.URL, nil, 0)
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil || len(tools) != 2 || tools[1].Name != "second" {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
	changes := 0
	if err := client.WatchToolsChanged(ctx, func() { changes++ }); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("changes=%d", changes)
	}
}

func TestNotificationWatchCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewHTTP(server.URL, nil, 0)
	client.listChanged = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := client.WatchToolsChanged(ctx, nil); err == nil {
		t.Fatal("watch ignored cancellation")
	}
}
