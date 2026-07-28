package snapshot

import (
	"testing"
	"time"
)

func TestSchedulerDebouncesIdleButCapsContinuousEditing(t *testing.T) {
	start := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	scheduler := NewScheduler()
	scheduler.Changed(start, 1)
	for minute := 1; minute <= 29; minute++ {
		scheduler.Changed(start.Add(time.Duration(minute)*time.Minute), uint64(minute+1))
		if decision := scheduler.Due(start.Add(time.Duration(minute) * time.Minute)); decision.Due {
			t.Fatalf("captured too early at minute %d", minute)
		}
	}
	decision := scheduler.Due(start.Add(30 * time.Minute))
	if !decision.Due || decision.Trigger != TriggerAutomatic {
		t.Fatalf("continuous edit cap not enforced: %#v", decision)
	}
}

func TestSchedulerSkipsUnchangedRevisionAndBacksOffFailures(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	scheduler := NewScheduler()
	scheduler.Changed(now, 1)
	scheduler.Succeeded(now, 1)
	if scheduler.Force(now, TriggerSwitch).Due {
		t.Fatal("unchanged revision produced duplicate snapshot")
	}
	scheduler.Changed(now.Add(time.Minute), 2)
	retryAt := scheduler.Failed(now.Add(time.Minute))
	if scheduler.Due(retryAt.Add(-time.Millisecond)).Due {
		t.Fatal("failure backoff ignored")
	}
}

func TestSchedulerDefersQuotaWarningWithoutMarkingRevisionCaptured(t *testing.T) {
	start := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	scheduler := NewScheduler()
	scheduler.Changed(start, 1)
	dueAt := start.Add(5 * time.Minute)
	if !scheduler.Due(dueAt).Due {
		t.Fatal("automatic snapshot never became due")
	}
	retryAt := scheduler.Deferred(dueAt, time.Hour)
	if decision := scheduler.Due(
		dueAt.Add(30 * time.Minute),
	); decision.Due {
		t.Fatalf("quota deferral ignored: %#v", decision)
	}
	if !scheduler.Due(retryAt).Due {
		t.Fatal("deferred automatic snapshot was marked captured")
	}
}
