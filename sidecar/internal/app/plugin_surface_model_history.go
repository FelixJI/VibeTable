package app

import (
	"context"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

type modelHistoryKey struct{}

// modelHistoryCursor is the model progress one request already validated.
// Audit rows are append-only and numbered by a per-project sequence, so the
// next read only has to validate what came after the highest sequence seen.
// The cursor never widens access: records are validated exactly once and a
// corrupt row inside the unread range fails the read like a full history read.
type modelHistoryCursor struct {
	project, plugin string
	seq             int64
	completed       []pluginmodel.State
}

// withModelHistory scopes a validated history to ctx. Only model commits that
// re-authenticate after every installed step use it; every other caller keeps
// the full History read, including its fail-closed validation.
func withModelHistory(ctx context.Context, project, plugin string, history []pluginmodel.Progress) context.Context {
	cursor := &modelHistoryCursor{project: project, plugin: plugin}
	for _, progress := range history {
		cursor.observe(progress)
	}
	return context.WithValue(ctx, modelHistoryKey{}, cursor)
}

func (c *modelHistoryCursor) observe(progress pluginmodel.Progress) {
	if progress.Seq > c.seq {
		c.seq = progress.Seq
	}
	if progress.State.Status == "completed" {
		c.completed = append(c.completed, progress.State)
	}
}

// completedModelHistory returns the validated completed states, oldest first.
// Without a matching cursor it reads the whole history; with one it reads only
// the progress recorded after the cursor's sequence. Item key spelling plays no
// part either way, so legacy and non-standard completed keys keep their authority.
func completedModelHistory(ctx context.Context, app core.App, project, plugin string) ([]pluginmodel.State, error) {
	cursor, _ := ctx.Value(modelHistoryKey{}).(*modelHistoryCursor)
	if cursor == nil || cursor.project != project || cursor.plugin != plugin {
		states, err := pluginmodel.States(ctx, app, project, plugin)
		if err != nil {
			return nil, err
		}
		completed := []pluginmodel.State{}
		for _, state := range states {
			if state.Status == "completed" {
				completed = append(completed, state)
			}
		}
		return completed, nil
	}
	newer, err := pluginmodel.StatesAfter(ctx, app, project, plugin, cursor.seq)
	if err != nil {
		// Nothing was validated; the next read repeats the same range.
		return nil, err
	}
	for _, progress := range newer {
		cursor.observe(progress)
	}
	return cursor.completed, nil
}
