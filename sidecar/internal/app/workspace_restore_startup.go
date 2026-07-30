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

// completeRestoreBeforeResumingJobs closes the installed-but-not-finalized
// snapshot restore window before any durable background job may mutate the
// restored PocketBase authority.
func completeRestoreBeforeResumingJobs(
	runtime *workspacev2.Runtime,
	ready func(*workspacev2.Runtime) error,
	jobs pendingJobResumer,
) error {
	if ready != nil {
		if err := ready(runtime); err != nil {
			return err
		}
	}
	if err := jobs.ResumePending(jobs.Context()); err != nil {
		return fmt.Errorf("resume coordinated workspace jobs: %w", err)
	}
	return nil
}
