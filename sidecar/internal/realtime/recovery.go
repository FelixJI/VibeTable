package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

// MaxRecoveryBytes bounds the complete JSON recovery payload before delivery.
// This is a wire budget, not a process memory limit.
const MaxRecoveryBytes = 4 * 1024 * 1024

// RecoverySnapshot separates present authority from retained historical notices.
// A terminal notice must never overwrite an active task that has since resumed.
type RecoverySnapshot struct {
	ContractVersion       string             `json:"contractVersion"`
	Topic                 string             `json:"topic"`
	ActiveFormulaTasks    []FormulaTaskState `json:"activeFormulaTasks"`
	TerminalNotifications []TaskChangedEvent `json:"terminalNotifications"`
}

// FormulaTaskState is current state, not a synthetic event or a task history.
type FormulaTaskState struct {
	TaskID   string                 `json:"taskId"`
	State    string                 `json:"state"`
	Progress float64                `json:"progress"`
	Cursor   *string                `json:"cursor"`
	Error    *mutation.ProductError `json:"error"`
}

// SubscribeRecoverable resumes retained events or atomically starts with a full
// current projection when the caller has no cursor or its cursor has expired.
func (hub *Hub) SubscribeRecoverable(ctx context.Context, after string) (*Subscription, error) {
	return hub.subscribe(ctx, func() ([]Event, int64, error) {
		return hub.recoveryBacklog(ctx, after)
	})
}

func (hub *Hub) recoveryBacklog(ctx context.Context, after string) ([]Event, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if hub.app == nil {
		return nil, 0, &Error{Code: "realtime.unavailable", Message: "realtime storage is unavailable", Retryable: true}
	}
	var cursor int64
	if after != "" {
		var err error
		cursor, err = strconv.ParseInt(strings.TrimPrefix(after, "rt:"), 10, 64)
		if err != nil || cursor < 0 || after != fmt.Sprintf("rt:%d", cursor) {
			return nil, 0, &Error{Code: "realtime.cursor_invalid", Message: "realtime cursor is invalid"}
		}
	}
	var backlog []Event
	var highWater int64
	err := hub.app.RunInTransaction(func(txApp core.App) error {
		rows, err := readRows(txApp, `WHERE rowid IN (
			SELECT rowid FROM vibetable_outbox ORDER BY rowid DESC LIMIT {:limit}
		) ORDER BY rowid ASC`, dbx.Params{"limit": maxCatchupEvents})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			highWater = rows[len(rows)-1].RowID
		}
		if after != "" && cursor > highWater {
			return &Error{Code: "realtime.cursor_future", Message: "realtime cursor is ahead of durable storage"}
		}
		start := -1
		if after == "rt:0" && (len(rows) == 0 || rows[0].RowID == 1) {
			start = 0
		}
		events := make([]Event, 0, len(rows))
		for index, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			event, err := decodeOutboxRow(row)
			if err != nil {
				return err
			}
			events = append(events, event)
			if after != "" && cursor == row.RowID {
				start = index + 1
			}
		}
		if after != "" && start >= 0 {
			backlog = events[start:]
			return nil
		}
		if after != "" && (len(rows) == 0 || cursor >= rows[0].RowID) {
			return &Error{Code: "realtime.cursor_unknown", Message: "realtime cursor is unknown"}
		}
		snapshot := RecoverySnapshot{
			ContractVersion: mutation.ContractVersion, Topic: "realtime.recovered",
			ActiveFormulaTasks: []FormulaTaskState{}, TerminalNotifications: []TaskChangedEvent{},
		}
		active, err := jobs.ReadActiveSnapshots(ctx, txApp)
		if err != nil {
			var jobErr *jobs.JobError
			if errors.As(err, &jobErr) {
				code := "realtime.storage_failed"
				if jobErr.Code == "job.resume_limit" {
					code = "realtime.recovery_limit"
				} else if jobErr.Code == "job.storage_corrupt" {
					code = "realtime.storage_corrupt"
				}
				return &Error{Code: code, Message: jobErr.Message, Retryable: jobErr.Retryable}
			}
			return err
		}
		for _, task := range active {
			state := FormulaTaskState{TaskID: task.JobID, State: "running"}
			if task.State == "queued" {
				state.State = "pending"
			}
			if task.Progress.Total > 0 {
				state.Progress = float64(task.Progress.Completed) / float64(task.Progress.Total)
			}
			if task.Cursor.LastRecordID != "" {
				cursor := "row:" + task.Cursor.LastRecordID
				state.Cursor = &cursor
			}
			if task.Error != nil {
				state.Error = &mutation.ProductError{
					ContractVersion: mutation.ContractVersion, Code: task.Error.Code,
					Message: task.Error.Message, Retryable: task.Error.Retryable, Details: map[string]any{},
				}
			}
			snapshot.ActiveFormulaTasks = append(snapshot.ActiveFormulaTasks, state)
		}
		for _, event := range events {
			if event.Topic != "task.changed" {
				continue
			}
			var task TaskChangedEvent
			if err := decodeStrict(event.Payload, &task); err != nil || task.TaskType != "formulaBackfill" {
				return corruptOutbox()
			}
			switch task.State {
			case "succeeded", "failed", "cancelled":
				snapshot.TerminalNotifications = append(snapshot.TerminalNotifications, task)
			case "pending", "running":
			default:
				return corruptOutbox()
			}
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if len(raw) > MaxRecoveryBytes {
			return &Error{Code: "realtime.recovery_limit", Message: "complete realtime recovery exceeds the wire budget"}
		}
		backlog = []Event{{Topic: snapshot.Topic, Payload: raw, Cursor: fmt.Sprintf("rt:%d", highWater)}}
		return ctx.Err()
	})
	return backlog, highWater, err
}
