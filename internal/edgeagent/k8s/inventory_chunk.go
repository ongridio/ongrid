package k8s

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	targetInventoryChunkBytes = 4 << 20
	maxInventoryChunkBytes    = 8 << 20
)

func buildInventorySnapshotChunks(base tunnel.KubernetesInventoryRequest, snap *inventorySnapshot) ([]tunnel.KubernetesInventoryRequest, error) {
	if snap == nil {
		return nil, fmt.Errorf("build kubernetes inventory chunks: snapshot is required")
	}
	snapshotID, err := newInventorySnapshotID()
	if err != nil {
		return nil, err
	}
	base.SnapshotID = snapshotID
	base.ChunkIndex = 0
	base.ChunkCount = 0
	base.Nodes = nil
	base.Workloads = nil
	base.Pods = nil
	base.Events = nil

	baseJSON, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("encode kubernetes inventory chunk header: %w", err)
	}
	chunks := []tunnel.KubernetesInventoryRequest{base}
	sizes := []int{len(baseJSON)}
	appendItem := func(encodedSize int, appendTo func(*tunnel.KubernetesInventoryRequest)) {
		index := len(chunks) - 1
		if inventoryRequestHasItems(chunks[index]) && sizes[index]+encodedSize+1 > targetInventoryChunkBytes {
			chunks = append(chunks, base)
			sizes = append(sizes, len(baseJSON))
			index++
		}
		appendTo(&chunks[index])
		sizes[index] += encodedSize + 1
	}
	for _, item := range snap.nodes {
		size, err := inventoryItemJSONSize(item)
		if err != nil {
			return nil, err
		}
		item := item
		appendItem(size, func(req *tunnel.KubernetesInventoryRequest) { req.Nodes = append(req.Nodes, item) })
	}
	for _, item := range snap.workloads {
		size, err := inventoryItemJSONSize(item)
		if err != nil {
			return nil, err
		}
		item := item
		appendItem(size, func(req *tunnel.KubernetesInventoryRequest) { req.Workloads = append(req.Workloads, item) })
	}
	for _, item := range snap.pods {
		size, err := inventoryItemJSONSize(item)
		if err != nil {
			return nil, err
		}
		item := item
		appendItem(size, func(req *tunnel.KubernetesInventoryRequest) { req.Pods = append(req.Pods, item) })
	}
	for _, item := range snap.events {
		size, err := inventoryItemJSONSize(item)
		if err != nil {
			return nil, err
		}
		item := item
		appendItem(size, func(req *tunnel.KubernetesInventoryRequest) { req.Events = append(req.Events, item) })
	}

	for i := range chunks {
		chunks[i].ChunkIndex = i
		chunks[i].ChunkCount = len(chunks)
		encoded, err := json.Marshal(chunks[i])
		if err != nil {
			return nil, fmt.Errorf("encode kubernetes inventory chunk %d: %w", i, err)
		}
		if len(encoded) > maxInventoryChunkBytes {
			return nil, fmt.Errorf("kubernetes inventory chunk %d is %d bytes, limit is %d", i, len(encoded), maxInventoryChunkBytes)
		}
	}
	return chunks, nil
}

func inventoryRequestHasItems(req tunnel.KubernetesInventoryRequest) bool {
	return len(req.Nodes)+len(req.Workloads)+len(req.Pods)+len(req.Events) > 0
}

func inventoryItemJSONSize(item any) (int, error) {
	encoded, err := json.Marshal(item)
	if err != nil {
		return 0, fmt.Errorf("encode kubernetes inventory item: %w", err)
	}
	return len(encoded), nil
}

func newInventorySnapshotID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate kubernetes inventory snapshot ID: %w", err)
	}
	return hex.EncodeToString(random), nil
}
