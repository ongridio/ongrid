package k8s

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type inventoryWatchAccumulator struct {
	mu      sync.Mutex
	pending inventoryWatchTrigger
	wake    chan struct{}
}

func newInventoryWatchAccumulator() *inventoryWatchAccumulator {
	return &inventoryWatchAccumulator{wake: make(chan struct{}, 1)}
}

func (a *inventoryWatchAccumulator) add(trigger inventoryWatchTrigger) {
	if trigger.isEmpty() {
		return
	}
	a.mu.Lock()
	a.pending = mergeInventoryWatchTrigger(a.pending, trigger)
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *inventoryWatchAccumulator) take() inventoryWatchTrigger {
	a.mu.Lock()
	defer a.mu.Unlock()
	trigger := a.pending
	a.pending = inventoryWatchTrigger{}
	return trigger
}

func (a *inventoryWatchAccumulator) notifications() <-chan struct{} {
	return a.wake
}

func mergeInventoryWatchTrigger(current, next inventoryWatchTrigger) inventoryWatchTrigger {
	current.reason = mergeWatchReason(current.reason, next.reason, current.count+next.count)
	if current.observedAt.IsZero() || (!next.observedAt.IsZero() && next.observedAt.Before(current.observedAt)) {
		current.observedAt = next.observedAt
	}
	current.count += next.count
	current.resourceVersion = newerResourceVersion(current.resourceVersion, next.resourceVersion)
	if current.resourceVersions == nil && len(next.resourceVersions) > 0 {
		current.resourceVersions = map[string]string{}
	}
	for key, rv := range next.resourceVersions {
		current.resourceVersions[key] = newerResourceVersion(current.resourceVersions[key], rv)
	}

	if current.fullResync || next.fullResync {
		current.fullResync = true
		current.syncType = inventorySyncFull
		current.nodes = nil
		current.workloads = nil
		current.pods = nil
		current.events = nil
		current.deletedNodes = nil
		current.deletedWorkloads = nil
		current.deletedPods = nil
		current.deletedEvents = nil
		return current
	}

	current.syncType = inventorySyncDelta
	current.nodes, current.deletedNodes = mergeFinalInventoryOperations(
		current.nodes, current.deletedNodes, next.nodes, next.deletedNodes,
		nodeSnapshotKey, nodeRefKey,
	)
	current.workloads, current.deletedWorkloads = mergeFinalInventoryOperations(
		current.workloads, current.deletedWorkloads, next.workloads, next.deletedWorkloads,
		workloadSnapshotKey, workloadRefKey,
	)
	current.pods, current.deletedPods = mergeFinalInventoryOperations(
		current.pods, current.deletedPods, next.pods, next.deletedPods,
		podSnapshotKey, podRefKey,
	)
	current.events, current.deletedEvents = mergeFinalInventoryOperations(
		current.events, current.deletedEvents, next.events, next.deletedEvents,
		eventSnapshotKey, eventRefKey,
	)
	return current
}

func mergeWatchReason(current, next string, count int) string {
	reason := strings.TrimSpace(strings.SplitN(current, " batch=", 2)[0])
	if reason == "" {
		reason = strings.TrimSpace(strings.SplitN(next, " batch=", 2)[0])
	}
	if count > 1 {
		return reason + " batch=" + strconv.Itoa(count)
	}
	return reason
}

func mergeFinalInventoryOperations[T any, R any](
	currentUpserts []T,
	currentDeletes []R,
	nextUpserts []T,
	nextDeletes []R,
	upsertKey func(T) string,
	deleteKey func(R) string,
) ([]T, []R) {
	upserts := make(map[string]T, len(currentUpserts)+len(nextUpserts))
	deletes := make(map[string]R, len(currentDeletes)+len(nextDeletes))
	applyUpserts := func(items []T) {
		for _, item := range items {
			key := upsertKey(item)
			if key == "" {
				continue
			}
			delete(deletes, key)
			upserts[key] = item
		}
	}
	applyDeletes := func(items []R) {
		for _, item := range items {
			key := deleteKey(item)
			if key == "" {
				continue
			}
			delete(upserts, key)
			deletes[key] = item
		}
	}
	applyUpserts(currentUpserts)
	applyDeletes(currentDeletes)
	applyUpserts(nextUpserts)
	applyDeletes(nextDeletes)

	upsertKeys := make([]string, 0, len(upserts))
	for key := range upserts {
		upsertKeys = append(upsertKeys, key)
	}
	sort.Strings(upsertKeys)
	mergedUpserts := make([]T, 0, len(upsertKeys))
	for _, key := range upsertKeys {
		mergedUpserts = append(mergedUpserts, upserts[key])
	}

	deleteKeys := make([]string, 0, len(deletes))
	for key := range deletes {
		deleteKeys = append(deleteKeys, key)
	}
	sort.Strings(deleteKeys)
	mergedDeletes := make([]R, 0, len(deleteKeys))
	for _, key := range deleteKeys {
		mergedDeletes = append(mergedDeletes, deletes[key])
	}
	return mergedUpserts, mergedDeletes
}

func waitForWatchDebounce(ctx context.Context, accumulator *inventoryWatchAccumulator, delay time.Duration) (inventoryWatchTrigger, bool) {
	trigger := accumulator.take()
	if delay <= 0 {
		return trigger, true
	}
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return trigger, false
		case <-accumulator.notifications():
			trigger = mergeInventoryWatchTrigger(trigger, accumulator.take())
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		case <-timer.C:
			return trigger, true
		}
	}
}
