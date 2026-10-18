package tools

import (
	"context"
	"fmt"
	"sync"
)

// batch_helper.go is N+15 of the batch-first BaseTool refactor — see
// MEMORY.md "Project:" + N+14 host_files batch landing.
// Six per-id BaseTools (get_host_load / get_process_list /
// get_host_summary / get_incident_detail / correlate_incident / bash)
// were upgraded to take an ID array and fan out manager-side; this file
// is the shared concurrency primitive.
//
// Why a helper, not a per-tool loop:
//
//   - Six tools, identical fan-out shape (semaphore + waitgroup + ordered
//     result slice). Diverging copies rot at different speeds; one
//     primitive keeps them in lockstep.
//   - Generics let the helper stay type-safe end-to-end. fn returns the
//     tool-specific ResultEntry directly, and the result slice carries
//     the same type — no any-shaped boxing, no per-tool reflection.
//   - The semaphore is a hard ceiling so a 16-id call doesn't try to
//     light up 16 tunnel sessions in parallel. 4 in flight is the same
//     ceiling the edge host_files batch handler picked; we mirror it
//     here so the shape of "manager fan-out → edge serialization"
//     matches.
//
// Per-call partial-success is handled INSIDE fn: each invocation should
// catch its own error and return a ResultEntry{Error: "..."} rather than
// propagating; that way the slice is always full-length and the caller
// can compute success/error counts by walking it once. The helper itself
// has no error return — fan-out cannot "fail" globally as long as each
// child resolves to a typed entry.

// batchMaxIDs is the manager-side hard upper bound on len(ids) for every
// batch-flavoured BaseTool. The schema's maxItems=16 already enforces
// this LLM-side; this is server-side defense for hand-crafted args /
// schema-validator bypass. Mirrors hostFilesMaxBatchPaths so the LLM
// sees one ceiling no matter which tool it picks.
const batchMaxIDs = 16

// batchConcurrency caps how many fan-out children run at once. 4 was
// picked to match the edge-side host_files concurrency (the original
// reference batch) — beyond 4 we'd be queueing past the typical edge's
// ability to service in parallel and burning manager goroutines for no
// throughput gain. Tunable later if a new batch tool has a faster
// upstream.
const batchConcurrency = 4

// runBatch fans fn out across ids concurrently with a fixed-size
// semaphore (batchConcurrency) and returns the per-id results in input
// order. fn is expected to be self-contained: it MUST NOT return an
// error — partial failure is encoded inside R (typically as a string
// Error field on the entry struct) so the caller can still hand back a
// full-length results slice.
//
// Generics: ID and R are independent type parameters. ID is the input
// (uint64 in every current tool, but the helper doesn't care); R is the
// per-id result entry type (e.g. HostLoadResultEntry,
// IncidentDetailResultEntry). The helper preserves order — results[i]
// corresponds to ids[i] regardless of completion order.
//
// Cancellation: the helper does not propagate ctx.Done() into a fast
// shutdown — fn already runs under a derived ctx in the caller, and the
// per-tool timeout (e.g. hostSummaryCallTimeout) governs how long each
// child can take. The wait-group join is unconditional so the slice is
// always written to before we return.
func runBatch[ID any, R any](
	ctx context.Context,
	ids []ID,
	fn func(ctx context.Context, id ID) R,
) []R {
	results := make([]R, len(ids))
	if len(ids) == 0 {
		return results
	}
	sem := make(chan struct{}, batchConcurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		i, id := i, id
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fn(ctx, id)
		}()
	}
	wg.Wait()
	return results
}

// validateBatchIDs enforces the 1..batchMaxIDs constraint at the
// BaseTool layer. The schema's minItems/maxItems already covers the
// LLM-supplied happy path; this is a belt-and-braces check for the rare
// case where the LLM emits an empty array or the schema validator is
// bypassed (e.g. test harnesses, hand-crafted argsJSON). idLabel is the
// field name surfaced in the error so the LLM sees "device_ids" /
// "incident_ids" instead of a generic "ids".
func validateBatchIDs[T any](idLabel string, ids []T) error {
	if len(ids) == 0 {
		return fmt.Errorf("%s: must contain at least 1 element", idLabel)
	}
	if len(ids) > batchMaxIDs {
		return fmt.Errorf("%s: too many (%d > max %d)", idLabel, len(ids), batchMaxIDs)
	}
	return nil
}
