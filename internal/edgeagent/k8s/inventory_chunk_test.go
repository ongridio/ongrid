package k8s

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestBuildInventorySnapshotChunksBoundsPayloadAndPreservesItems(t *testing.T) {
	const eventCount = 64
	snap := &inventorySnapshot{
		scope:            inventoryScopeCluster,
		resourceVersion:  "100",
		resourceVersions: map[string]string{"events": "100"},
	}
	message := strings.Repeat("x", 256<<10)
	for i := 0; i < eventCount; i++ {
		snap.events = append(snap.events, tunnel.KubernetesEventSnapshot{
			Namespace: "default",
			Name:      fmt.Sprintf("event-%d", i),
			UID:       fmt.Sprintf("uid-%d", i),
			Message:   message,
		})
	}
	chunks, err := buildInventorySnapshotChunks(tunnel.KubernetesInventoryRequest{
		ClusterID: 7,
		SyncType:  inventorySyncFull,
	}, snap)
	if err != nil {
		t.Fatalf("buildInventorySnapshotChunks() error = %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want multiple chunks", len(chunks))
	}
	var gotEvents int
	snapshotID := chunks[0].SnapshotID
	for i, chunk := range chunks {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			t.Fatalf("json.Marshal(chunk %d): %v", i, err)
		}
		if len(encoded) > maxInventoryChunkBytes {
			t.Fatalf("chunk %d bytes = %d, limit %d", i, len(encoded), maxInventoryChunkBytes)
		}
		if chunk.SnapshotID == "" || chunk.SnapshotID != snapshotID || chunk.ChunkIndex != i || chunk.ChunkCount != len(chunks) {
			t.Fatalf("chunk %d metadata = snapshot:%q index:%d count:%d", i, chunk.SnapshotID, chunk.ChunkIndex, chunk.ChunkCount)
		}
		gotEvents += len(chunk.Events)
	}
	if gotEvents != eventCount {
		t.Fatalf("events across chunks = %d, want %d", gotEvents, eventCount)
	}
}
