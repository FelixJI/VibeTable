package snapshot

import (
	"sync"
	"time"
)

type ScheduleDecision struct {
	Due     bool
	Trigger Trigger
	At      time.Time
}

type Scheduler struct {
	mu sync.Mutex

	idleDelay        time.Duration
	maxEditInterval  time.Duration
	changedAt        time.Time
	lastActivityAt   time.Time
	lastCaptureAt    time.Time
	mutationRevision uint64
	capturedRevision uint64
	failures         int
	nextAttemptAt    time.Time
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		idleDelay:       5 * time.Minute,
		maxEditInterval: 30 * time.Minute,
	}
}

func (scheduler *Scheduler) Changed(now time.Time, mutationRevision uint64) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if mutationRevision == scheduler.mutationRevision {
		scheduler.lastActivityAt = now
		return
	}
	if scheduler.mutationRevision == scheduler.capturedRevision {
		scheduler.changedAt = now
	}
	scheduler.mutationRevision = mutationRevision
	scheduler.lastActivityAt = now
}

func (scheduler *Scheduler) Due(now time.Time) ScheduleDecision {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if scheduler.mutationRevision == scheduler.capturedRevision ||
		now.Before(scheduler.nextAttemptAt) {
		return ScheduleDecision{}
	}
	idleAt := scheduler.lastActivityAt.Add(scheduler.idleDelay)
	maxAt := scheduler.changedAt.Add(scheduler.maxEditInterval)
	at := idleAt
	if maxAt.Before(at) {
		at = maxAt
	}
	if now.Before(at) {
		return ScheduleDecision{At: at}
	}
	return ScheduleDecision{Due: true, Trigger: TriggerAutomatic, At: at}
}

func (scheduler *Scheduler) Force(now time.Time, trigger Trigger) ScheduleDecision {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if scheduler.mutationRevision == scheduler.capturedRevision {
		return ScheduleDecision{}
	}
	return ScheduleDecision{Due: true, Trigger: trigger, At: now}
}

func (scheduler *Scheduler) Succeeded(now time.Time, mutationRevision uint64) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if mutationRevision > scheduler.capturedRevision {
		scheduler.capturedRevision = mutationRevision
	}
	scheduler.lastCaptureAt = now
	scheduler.failures = 0
	scheduler.nextAttemptAt = time.Time{}
}

func (scheduler *Scheduler) Failed(now time.Time) time.Time {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.failures++
	exponent := min(scheduler.failures, 8)
	delay := time.Second * time.Duration(1<<exponent)
	scheduler.nextAttemptAt = now.Add(delay)
	return scheduler.nextAttemptAt
}

func (scheduler *Scheduler) Deferred(now time.Time, delay time.Duration) time.Time {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if delay <= 0 {
		delay = time.Hour
	}
	scheduler.nextAttemptAt = now.Add(delay)
	return scheduler.nextAttemptAt
}
