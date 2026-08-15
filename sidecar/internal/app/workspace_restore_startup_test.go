package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

type recordingJobResumer struct {
	steps []string
	err   error
}

type recordingBackgroundStarter struct {
	steps *[]string
	err   error
}

func (starter recordingBackgroundStarter) StartBackgroundWorkers(context.Context, bool) error {
	*starter.steps = append(*starter.steps, "workers")
	return starter.err
}

func (resumer *recordingJobResumer) Context() context.Context {
	return context.Background()
}

func (resumer *recordingJobResumer) ResumePending(context.Context) error {
	resumer.steps = append(resumer.steps, "resume")
	return resumer.err
}

func TestCompleteRestoreBeforeResumingJobsOrdersRecoveryFirst(t *testing.T) {
	resumer := &recordingJobResumer{}
	err := completeRestoreWithStarter(
		nil,
		recordingBackgroundStarter{steps: &resumer.steps},
		func(*workspacev2.Runtime) error {
			resumer.steps = append(resumer.steps, "restore")
			return nil
		},
		resumer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumer.steps, []string{"restore", "workers", "resume"}) {
		t.Fatalf("startup order = %#v", resumer.steps)
	}
}

func TestCompleteRestoreBeforeResumingJobsDoesNotResumeAfterWorkerFailure(t *testing.T) {
	resumer := &recordingJobResumer{}
	workerErr := errors.New("watcher failed")
	err := completeRestoreWithStarter(
		nil,
		recordingBackgroundStarter{steps: &resumer.steps, err: workerErr},
		func(*workspacev2.Runtime) error {
			resumer.steps = append(resumer.steps, "restore")
			return nil
		},
		resumer,
	)
	if !errors.Is(err, workerErr) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(resumer.steps, []string{"restore", "workers"}) {
		t.Fatalf("jobs resumed before workers were ready: %#v", resumer.steps)
	}
}

func TestCompleteRestoreBeforeResumingJobsDoesNotResumeAfterRecoveryFailure(
	t *testing.T,
) {
	resumer := &recordingJobResumer{}
	recoveryErr := errors.New("restore rollback failed")
	err := completeRestoreBeforeResumingJobs(
		nil,
		func(*workspacev2.Runtime) error {
			resumer.steps = append(resumer.steps, "restore")
			return recoveryErr
		},
		resumer,
	)
	if !errors.Is(err, recoveryErr) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(resumer.steps, []string{"restore"}) {
		t.Fatalf("jobs resumed before recovery completed: %#v", resumer.steps)
	}
}
