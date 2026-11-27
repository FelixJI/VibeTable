package pluginmodel

import (
	"context"
	"database/sql"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

type Binding struct {
	TableID string            `json:"tableId"`
	Fields  map[string]string `json:"fields"`
}
type State struct {
	Contract             string             `json:"contract"`
	RequestID            string             `json:"requestId"`
	InstallationID       string             `json:"installationId"`
	InstallationRevision int64              `json:"installationRevision"`
	Model                Declaration        `json:"model"`
	Status               string             `json:"status"`
	Completed            int                `json:"completed"`
	Total                int                `json:"total"`
	Bindings             map[string]Binding `json:"bindings"`
	Revisions            map[string]string  `json:"revisions"`
	Diagnostics          []v2.Diagnostic    `json:"diagnostics,omitempty"`
	ErrorCode            string             `json:"errorCode,omitempty"`
}

// States reads only platform model progress in the existing immutable audit log.
// It deliberately survives disable, uninstall and private-storage cleanup.
func States(ctx context.Context, app core.App, project, plugin string) ([]State, error) {
	history, err := History(ctx, app, project, plugin)
	if err != nil {
		return nil, err
	}
	states := make([]State, 0, len(history))
	for _, progress := range history {
		states = append(states, progress.State)
	}
	return states, nil
}

// Progress is one validated model progress state and the audit sequence of its record.
// The item key is deliberately not part of it: authority never depends on key spelling.
type Progress struct {
	Seq   int64 `json:"-"`
	State State
}

// History reads all model progress, oldest first, with each record's audit sequence.
func History(ctx context.Context, app core.App, project, plugin string) ([]Progress, error) {
	return readProgress(ctx, app, project, plugin, nil)
}

// StatesAfter reads the model progress recorded after an audit sequence, oldest first.
// Audit rows are appended with a per-project sequence, so the plugin event index
// serves the range directly: a caller that already validated the history up to
// `after` pays only for newer records. Validation is identical to History and no
// record inside the range is skipped, so a corrupt row still fails the read.
func StatesAfter(ctx context.Context, app core.App, project, plugin string, after int64) ([]Progress, error) {
	return readProgress(ctx, app, project, plugin, &after)
}

// CompletedStates reads the completed model progress, oldest first, validating
// every listed record exactly like History. In a context scoped by
// WithProgressCache it reuses the result of an unchanged history.
func CompletedStates(ctx context.Context, app core.App, project, plugin string) ([]State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := app.FindCollectionByNameOrId("vibetable_plugin_records"); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []State{}, nil
		}
		return nil, err
	}
	if cache, ok := ctx.Value(progressCacheKey{}).(*progressCache); ok {
		return cache.completedStates(ctx, app, project, plugin)
	}
	completed := []State{}
	err := listProgress(ctx, app, project, plugin, nil, func(_ string, _ int64, payload any) error {
		state, err := decodeProgress(payload)
		if err != nil {
			return err
		}
		if state.Contract != Contract {
			return errors.New("plugin model progress contract is invalid")
		}
		if state.Status == "completed" {
			completed = append(completed, state)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return completed, nil
}

func readProgress(ctx context.Context, app core.App, project, plugin string, after *int64) ([]Progress, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := app.FindCollectionByNameOrId("vibetable_plugin_records"); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	result := []Progress{}
	err := listProgress(ctx, app, project, plugin, after, func(_ string, seq int64, payload any) error {
		state, err := decodeProgress(payload)
		if err != nil {
			return err
		}
		if state.Contract != Contract {
			return errors.New("plugin model progress contract is invalid")
		}
		result = append(result, Progress{Seq: seq, State: state})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// listProgress pages through the model progress records the current snapshot
// lists, oldest first, and visits each with its record id, sequence and payload.
func listProgress(ctx context.Context, app core.App, project, plugin string, after *int64, visit func(id string, seq int64, payload any) error) error {
	filter := "kind='audit' && payload_json.eventType='model.progress'"
	params := dbx.Params{}
	if after != nil {
		filter += " && seq>{:after}"
		params["after"] = *after
	}
	if project != "" {
		filter += " && project_key={:project}"
		params["project"] = project
	}
	if plugin != "" {
		filter += " && plugin_id={:plugin}"
		params["plugin"] = plugin
	}
	for offset := 0; ; offset += 256 {
		rows, err := app.FindRecordsByFilter("vibetable_plugin_records", filter, "seq", 256, offset, params)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(row.Id, int64(row.GetInt("seq")), row.GetRaw("payload_json")); err != nil {
				return err
			}
		}
		if len(rows) < 256 {
			return nil
		}
	}
}

func decodeProgress(payload any) (State, error) {
	var event struct {
		Details State `json:"details"`
	}
	if stored, ok := payload.(types.JSONRaw); ok {
		raw, err := stored.MarshalJSON()
		if err == nil && jsonv2.Unmarshal(raw, &event, json.DefaultOptionsV1(), json.ReportErrorsWithLegacySemantics(false)) == nil {
			return event.Details, nil
		}
		event.Details = State{}
	}
	// Preserve legacy errors and accepted values for rejected or non-PocketBase payloads.
	raw, err := json.Marshal(payload)
	if err != nil {
		return State{}, err
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return State{}, err
	}
	return event.Details, nil
}

func ProtectTable(ctx context.Context, app core.App, table string) error {
	states, err := States(ctx, app, "", "")
	if err != nil {
		return err
	}
	for _, s := range states {
		for _, b := range s.Bindings {
			if b.TableID == table {
				return protected("tableId")
			}
		}
	}
	return nil
}

func ProtectField(ctx context.Context, app core.App, table, field string, before, after *v2.FieldDefinition) error {
	if field == "" || before == nil {
		return nil
	}
	states, err := States(ctx, app, "", "")
	if err != nil {
		return err
	}
	for _, s := range states {
		for _, b := range s.Bindings {
			if b.TableID != table {
				continue
			}
			for _, id := range b.Fields {
				if id != field {
					continue
				}
				if after == nil {
					return protected("fieldId")
				}
				// Display and help remain user-editable; value/storage/relations stay compatible.
				a, z := *before, *after
				a.DisplayName, a.Help, a.Display = z.DisplayName, z.Help, z.Display
				if !reflect.DeepEqual(a, z) {
					return protected("fieldId")
				}
			}
		}
	}
	return nil
}
func protected(path string) error {
	return &schemaerror.ProductError{Code: "plugin.model.core_protected", Path: path, Message: "field or table is bound to a plugin model; rename and user extension fields remain editable"}
}
