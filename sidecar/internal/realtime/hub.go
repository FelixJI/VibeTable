package realtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

const (
	maxSubscribers   = 32
	subscriberQueue  = 256
	maxCatchupEvents = 10_000
)

type Event struct {
	ID      string
	Topic   string
	Payload []byte
	Cursor  string
}

type TaskChangedEvent struct {
	ContractVersion string                 `json:"contractVersion"`
	Topic           string                 `json:"topic"`
	EventID         string                 `json:"eventId"`
	Sequence        int64                  `json:"sequence"`
	OccurredAt      string                 `json:"occurredAt"`
	TaskID          string                 `json:"taskId"`
	TaskType        string                 `json:"taskType"`
	State           string                 `json:"state"`
	Progress        float64                `json:"progress"`
	Cursor          *string                `json:"cursor"`
	Error           *mutation.ProductError `json:"error"`
}

type Subscription struct {
	Backlog []Event
	Events  <-chan Event

	cancel func()
}

func (subscription *Subscription) Close() {
	if subscription != nil && subscription.cancel != nil {
		subscription.cancel()
	}
}

type subscriber struct {
	events chan Event
}

type Hub struct {
	app core.App

	mu          sync.Mutex
	nextID      uint64
	highWater   int64
	subscribers map[uint64]*subscriber
}

func New(app core.App) *Hub {
	return &Hub{app: app, subscribers: map[uint64]*subscriber{}}
}

func (hub *Hub) Publish(
	ctx context.Context,
	event mutation.DataChangedEvent,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return hub.drainDurable()
}

func (hub *Hub) PublishEvent(event Event) error {
	if event.ID == "" || event.Topic == "" || len(event.Payload) == 0 {
		return errors.New("realtime event is incomplete")
	}
	if !json.Valid(event.Payload) {
		return errors.New("realtime event payload is invalid JSON")
	}
	hub.publish(Event{
		ID: event.ID, Topic: event.Topic,
		Payload: append([]byte(nil), event.Payload...),
	})
	return nil
}

func (hub *Hub) PublishTaskChanged(
	ctx context.Context,
	snapshot jobs.Snapshot,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := hub.PersistTaskChanged(ctx, hub.app, snapshot); err != nil {
		return err
	}
	return hub.drainDurable()
}

func (hub *Hub) PersistTaskChanged(
	ctx context.Context,
	app core.App,
	snapshot jobs.Snapshot,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state := "running"
	switch snapshot.State {
	case "queued":
		state = "pending"
	case "complete":
		state = "succeeded"
	case "failed":
		state = "failed"
	case "cancelled":
		state = "cancelled"
	}
	progress := 0.0
	if snapshot.Progress.Total > 0 {
		progress = float64(snapshot.Progress.Completed) /
			float64(snapshot.Progress.Total)
	}
	if state == "succeeded" {
		progress = 1
	}
	var cursor *string
	if snapshot.Cursor.LastRecordID != "" {
		value := "row:" + snapshot.Cursor.LastRecordID
		cursor = &value
	}
	var eventError *mutation.ProductError
	if snapshot.Error != nil {
		eventError = &mutation.ProductError{
			ContractVersion: mutation.ContractVersion,
			Code:            snapshot.Error.Code,
			Message:         snapshot.Error.Message,
			Details:         map[string]any{},
			Retryable:       snapshot.Error.Retryable,
		}
	}
	sequence := int64(2 + snapshot.Progress.Completed)
	if state == "pending" {
		sequence = 1
	}
	if state == "succeeded" || state == "failed" || state == "cancelled" {
		sequence++
	}
	identity := fmt.Sprintf(
		"%s\x00%s\x00%d\x00%s",
		snapshot.JobID,
		state,
		snapshot.Progress.Completed,
		snapshot.Cursor.LastRecordID,
	)
	sum := sha256.Sum256([]byte(identity))
	event := TaskChangedEvent{
		ContractVersion: mutation.ContractVersion,
		Topic:           "task.changed",
		EventID:         fmt.Sprintf("evt_task_%x", sum[:12]),
		Sequence:        sequence,
		OccurredAt:      time.Now().UTC().Format(time.RFC3339),
		TaskID:          snapshot.JobID,
		TaskType:        "formulaBackfill",
		State:           state,
		Progress:        progress,
		Cursor:          cursor,
		Error:           eventError,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if app == nil {
		return errors.New("realtime storage is unavailable")
	}
	if _, err := app.FindFirstRecordByFilter(
		"vibetable_outbox",
		"event_id={:event}",
		dbx.Params{"event": event.EventID},
	); errors.Is(err, sql.ErrNoRows) {
		collection, findErr := app.FindCollectionByNameOrId(
			"vibetable_outbox",
		)
		if findErr != nil {
			return findErr
		}
		record := core.NewRecord(collection)
		record.Set("event_id", event.EventID)
		record.Set("topic", event.Topic)
		record.Set("payload_json", types.JSONRaw(raw))
		record.Set("status", "pending")
		record.Set("attempts", 0)
		if err := app.Save(record); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (hub *Hub) publish(event Event) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.publishLocked(event)
}

func (hub *Hub) publishLocked(event Event) {
	for id, subscription := range hub.subscribers {
		select {
		case subscription.events <- event:
		default:
			close(subscription.events)
			delete(hub.subscribers, id)
		}
	}
}

func (hub *Hub) drainDurable() error {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	rows, err := hub.readRows(
		"WHERE rowid > {:highWater} ORDER BY rowid ASC",
		dbx.Params{"highWater": hub.highWater},
	)
	if err != nil {
		return err
	}
	for _, row := range rows {
		event, decodeErr := decodeOutboxRow(row)
		if decodeErr != nil {
			return decodeErr
		}
		hub.publishLocked(event)
		hub.highWater = row.RowID
	}
	return nil
}

func (hub *Hub) Subscribe(
	ctx context.Context,
	afterEventID string,
) (*Subscription, error) {
	hub.mu.Lock()
	if len(hub.subscribers) >= maxSubscribers {
		hub.mu.Unlock()
		return nil, &Error{
			Code:      "realtime.capacity",
			Message:   "realtime subscriber capacity is exhausted",
			Retryable: true,
		}
	}
	hub.nextID++
	id := hub.nextID
	entry := &subscriber{events: make(chan Event, subscriberQueue)}
	hub.subscribers[id] = entry
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			hub.mu.Lock()
			if current := hub.subscribers[id]; current != nil {
				delete(hub.subscribers, id)
				close(current.events)
			}
			hub.mu.Unlock()
		})
	}
	backlog, retainedHighWater, err := hub.catchup(afterEventID)
	if err != nil {
		delete(hub.subscribers, id)
		close(entry.events)
		hub.mu.Unlock()
		return nil, err
	}
	if retainedHighWater > hub.highWater {
		hub.highWater = retainedHighWater
	}
	hub.mu.Unlock()
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return &Subscription{
		Backlog: backlog, Events: entry.events, cancel: cancel,
	}, nil
}

func (hub *Hub) catchup(afterEventID string) ([]Event, int64, error) {
	if hub.app == nil {
		return nil, 0, &Error{
			Code:      "realtime.unavailable",
			Message:   "realtime storage is unavailable",
			Retryable: true,
		}
	}
	rows, err := hub.readRows(
		`WHERE rowid IN (
			SELECT rowid FROM vibetable_outbox
			ORDER BY rowid DESC LIMIT {:limit}
		) ORDER BY rowid ASC`,
		dbx.Params{"limit": maxCatchupEvents},
	)
	if err != nil {
		return nil, 0, &Error{
			Code:      "realtime.storage_failed",
			Message:   "realtime catch-up could not be read",
			Retryable: true,
		}
	}
	events := make([]Event, 0, len(rows))
	for _, row := range rows {
		event, decodeErr := decodeOutboxRow(row)
		if decodeErr != nil {
			return nil, 0, decodeErr
		}
		events = append(events, event)
	}
	start := 0
	if afterEventID != "" {
		found := false
		for index, event := range events {
			if event.ID == afterEventID || event.Cursor == afterEventID {
				start, found = index+1, true
				break
			}
		}
		if !found {
			if strings.HasPrefix(afterEventID, "rt:") {
				cursor, parseErr := parseCursor(afterEventID)
				if parseErr != nil {
					return nil, 0, &Error{
						Code:    "realtime.cursor_unknown",
						Message: "realtime cursor is invalid",
					}
				}
				if len(rows) > 0 && cursor < rows[0].RowID {
					return nil, 0, &Error{
						Code:    "realtime.cursor_expired",
						Message: "realtime cursor is outside the retained window",
					}
				}
				return nil, 0, &Error{
					Code:    "realtime.cursor_unknown",
					Message: "realtime cursor is unknown",
				}
			}
			if len(rows) == maxCatchupEvents {
				return nil, 0, &Error{
					Code:    "realtime.catchup_limit",
					Message: "legacy realtime cursor is outside the retained window",
				}
			}
			return nil, 0, &Error{
				Code:    "realtime.cursor_unknown",
				Message: "realtime cursor is unknown",
			}
		}
	}
	result := make([]Event, 0, len(events)-start)
	for _, event := range events[start:] {
		result = append(result, event)
	}
	var highWater int64
	if len(rows) > 0 {
		highWater = rows[len(rows)-1].RowID
	}
	return result, highWater, nil
}

type outboxRow struct {
	RowID       int64  `db:"row_id"`
	EventID     string `db:"event_id"`
	Topic       string `db:"topic"`
	PayloadJSON string `db:"payload_json"`
}

func (hub *Hub) readRows(where string, params dbx.Params) ([]outboxRow, error) {
	var rows []outboxRow
	query := `SELECT rowid AS row_id, event_id, topic, payload_json
		FROM vibetable_outbox ` + where
	if err := hub.app.DB().NewQuery(query).Bind(params).All(&rows); err != nil {
		return nil, &Error{
			Code: "realtime.storage_failed", Message: "realtime outbox could not be read",
			Retryable: true,
		}
	}
	return rows, nil
}

func decodeOutboxRow(row outboxRow) (Event, error) {
	raw := []byte(row.PayloadJSON)
	switch row.Topic {
	case "data.changed":
		var event mutation.DataChangedEvent
		if mutation.DecodeStrict(raw, &event) != nil ||
			event.EventID != row.EventID || event.Topic != row.Topic {
			return Event{}, corruptOutbox()
		}
	case "task.changed":
		var event TaskChangedEvent
		if decodeStrict(raw, &event) != nil ||
			event.ContractVersion != mutation.ContractVersion ||
			event.Topic != "task.changed" || event.EventID != row.EventID ||
			event.TaskID == "" || event.TaskType == "" || event.Sequence < 1 ||
			event.Progress < 0 || event.Progress > 1 {
			return Event{}, corruptOutbox()
		}
	default:
		return Event{}, corruptOutbox()
	}
	return Event{
		ID: row.EventID, Topic: row.Topic, Payload: raw,
		Cursor: fmt.Sprintf("rt:%d", row.RowID),
	}, nil
}

func parseCursor(value string) (int64, error) {
	if !strings.HasPrefix(value, "rt:") {
		return 0, errors.New("legacy cursor")
	}
	cursor, err := strconv.ParseInt(strings.TrimPrefix(value, "rt:"), 10, 64)
	if err != nil || cursor < 1 {
		return 0, errors.New("invalid cursor")
	}
	return cursor, nil
}

func corruptOutbox() *Error {
	return &Error{
		Code:    "realtime.storage_corrupt",
		Message: "realtime outbox contains an invalid event",
	}
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("event must contain one JSON value")
	}
	return nil
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (err *Error) Error() string {
	return err.Code + ": " + err.Message
}
