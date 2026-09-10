package report

import (
	"context"
	"fmt"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
)

// task.go — the unified-task usecase (HLD-022 Phase 2). Stored tasks are oneoff
// (run-once, triggered immediately from the task side); recurring tasks remain
// report_schedules and are unioned in at the server layer.

// CreateOneoffTaskAndRun creates a oneoff task and immediately generates its
// report, attributing the artifact back to the task via task_id="oneoff:<id>".
// This is the task-side replacement for the old products-side "立即生成".
func (u *Usecase) CreateOneoffTaskAndRun(ctx context.Context, createdBy uint64, title, kind, tz, scopeJSON, locale string, now time.Time) (*model.Task, error) {
	if u.read == nil {
		return nil, fmt.Errorf("task store not wired")
	}
	if strings.TrimSpace(tz) == "" {
		tz = "UTC"
	}
	loc, err := LoadLocation(tz)
	if err != nil {
		return nil, err
	}
	period, err := PeriodFor(kind, now, loc, time.Time{})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		title = TitleFor(kind, period, locale)
	}
	t := &model.Task{
		ID:         u.idGen(),
		Kind:       model.TaskKindOneoff,
		Title:      strings.TrimSpace(title),
		ReportKind: kind,
		ScopeJSON:  scopeJSON,
		Status:     "active",
		CreatedBy:  createdBy,
	}
	if err := u.read.CreateTask(ctx, t); err != nil {
		return nil, err
	}
	// Generate the report attributed to this task. Best-effort: the task row
	// already exists, so a generation failure still leaves a visible task whose
	// detail shows no artifact yet (the user can re-run).
	if _, err := u.GenerateNow(ctx, createdBy, kind, tz, scopeJSON, locale, model.OneoffTaskRef(t.ID), period); err != nil {
		return t, err
	}
	return t, nil
}

// RerunOneoffTask generates another report for an existing oneoff task.
func (u *Usecase) RerunOneoffTask(ctx context.Context, taskID, locale string, now time.Time) (*model.Task, error) {
	if u.read == nil {
		return nil, fmt.Errorf("task store not wired")
	}
	t, err := u.read.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	loc, err := LoadLocation("UTC")
	if err != nil {
		return nil, err
	}
	period, err := PeriodFor(t.ReportKind, now, loc, time.Time{})
	if err != nil {
		return nil, err
	}
	if _, err := u.GenerateNow(ctx, t.CreatedBy, t.ReportKind, "UTC", t.ScopeJSON, locale, model.OneoffTaskRef(t.ID), period); err != nil {
		return t, err
	}
	return t, nil
}

// ListOneoffTasks returns the stored (oneoff) tasks.
func (u *Usecase) ListOneoffTasks(ctx context.Context) ([]*model.Task, error) {
	if u.read == nil {
		return nil, nil
	}
	return u.read.ListTasks(ctx)
}

// GetTask returns one oneoff task by id.
func (u *Usecase) GetTask(ctx context.Context, id string) (*model.Task, error) {
	if u.read == nil {
		return nil, fmt.Errorf("task store not wired")
	}
	return u.read.GetTask(ctx, id)
}

// DeleteTask removes a oneoff task (its reports stay as standalone artifacts).
func (u *Usecase) DeleteTask(ctx context.Context, id string) error {
	if u.read == nil {
		return fmt.Errorf("task store not wired")
	}
	return u.read.DeleteTask(ctx, id)
}
