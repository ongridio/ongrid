package k8s

import (
	"fmt"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	maxInventorySnapshotIDLength = 128
	maxInventoryChunkCount       = 10000
	inventoryTimestampPrecision  = time.Millisecond
)

type inventoryUploadState struct {
	snapshotID string
	chunkCount int
	nextChunk  int
	startedAt  time.Time
}

func (u *Usecase) prepareInventoryChunk(in tunnel.KubernetesInventoryRequest, receivedAt time.Time) (time.Time, bool, error) {
	receivedAt = receivedAt.UTC().Truncate(inventoryTimestampPrecision)
	snapshotID := strings.TrimSpace(in.SnapshotID)
	if normalizeInventorySyncType(in.SyncType) == inventorySyncDelta {
		if snapshotID != "" || in.ChunkIndex != 0 || in.ChunkCount != 0 {
			return time.Time{}, false, fmt.Errorf("%w: delta inventory cannot contain snapshot chunks", errs.ErrInvalid)
		}
		return receivedAt, true, nil
	}
	if snapshotID == "" {
		if in.ChunkIndex != 0 || in.ChunkCount != 0 {
			return time.Time{}, false, fmt.Errorf("%w: snapshot_id is required for chunked inventory", errs.ErrInvalid)
		}
		return receivedAt, true, nil
	}
	if len(snapshotID) > maxInventorySnapshotIDLength {
		return time.Time{}, false, fmt.Errorf("%w: snapshot_id is too long", errs.ErrInvalid)
	}
	if in.ChunkCount <= 0 || in.ChunkCount > maxInventoryChunkCount || in.ChunkIndex < 0 || in.ChunkIndex >= in.ChunkCount {
		return time.Time{}, false, fmt.Errorf("%w: invalid inventory chunk %d/%d", errs.ErrInvalid, in.ChunkIndex, in.ChunkCount)
	}

	u.inventoryUploadsMu.Lock()
	defer u.inventoryUploadsMu.Unlock()
	state, ok := u.inventoryUploads[in.ClusterID]
	if in.ChunkIndex == 0 {
		state = inventoryUploadState{
			snapshotID: snapshotID,
			chunkCount: in.ChunkCount,
			nextChunk:  0,
			startedAt:  receivedAt,
		}
		u.inventoryUploads[in.ClusterID] = state
		ok = true
	}
	if !ok || state.snapshotID != snapshotID || state.chunkCount != in.ChunkCount || state.nextChunk != in.ChunkIndex {
		return time.Time{}, false, fmt.Errorf("%w: inventory snapshot chunk is stale or out of order", errs.ErrConflict)
	}
	return state.startedAt, in.ChunkIndex == in.ChunkCount-1, nil
}

func (u *Usecase) completeInventoryChunk(in tunnel.KubernetesInventoryRequest) error {
	snapshotID := strings.TrimSpace(in.SnapshotID)
	if snapshotID == "" {
		return nil
	}
	u.inventoryUploadsMu.Lock()
	defer u.inventoryUploadsMu.Unlock()
	state, ok := u.inventoryUploads[in.ClusterID]
	if !ok || state.snapshotID != snapshotID || state.nextChunk != in.ChunkIndex {
		return fmt.Errorf("%w: inventory snapshot changed while applying chunk", errs.ErrConflict)
	}
	if in.ChunkIndex == in.ChunkCount-1 {
		delete(u.inventoryUploads, in.ClusterID)
		return nil
	}
	state.nextChunk++
	u.inventoryUploads[in.ClusterID] = state
	return nil
}
