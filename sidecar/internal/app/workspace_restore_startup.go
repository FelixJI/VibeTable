package app

import (
	"context"
	"fmt"

	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

type pendingJobResumer interface {
	Context() context.Context
	ResumePending(context.Context) error
}

type workspaceBackgroundStarter interface {
	StartBackgroundWorkers(context.Context, bool) error
}

// completeRestoreBeforeResumingJobs closes the installed-but-not-finalized
// snapshot restore window before any durable background job may mutate the
// restored PocketBase authority.
func completeRestoreBeforeResumingJobs(
	runtime *workspacev2.Runtime,
	ready func(*workspacev2.Runtime) error,
	jobs pendingJobResumer,
) error {
	var starter workspaceBackgroundStarter
	if runtime != nil {
		starter = runtime
	}
	return completeRestoreWithStarter(runtime, starter, ready, jobs)
}

func completeRestoreWithStarter(
	runtime *workspacev2.Runtime,
	starter workspaceBackgroundStarter,
	ready func(*workspacev2.Runtime) error,
	jobs pendingJobResumer,
) error {
	if ready != nil {
		if err := ready(runtime); err != nil {
			return err
		}
	}
	if starter != nil {
		if err := starter.StartBackgroundWorkers(jobs.Context(), false); err != nil {
			return fmt.Errorf("resume workspace background workers: %w", err)
		}
	}
	if err := jobs.ResumePending(jobs.Context()); err != nil {
		return fmt.Errorf("resume coordinated workspace jobs: %w", err)
	}
	return nil
}
