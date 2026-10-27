package pluginmodel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
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
	ErrorCode            string             `json:"errorCode,omitempty"`
}

// States reads only platform model progress in the existing immutable audit log.
// It deliberately survives disable, uninstall and private-storage cleanup.
func States(ctx context.Context, app core.App, project, plugin string) ([]State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := app.FindCollectionByNameOrId("vibetable_plugin_records"); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	filter := "kind='audit' && payload_json.eventType='model.progress'"
	params := dbx.Params{}
	if project != "" {
		filter += " && project_key={:project}"
		params["project"] = project
	}
	if plugin != "" {
		filter += " && plugin_id={:plugin}"
		params["plugin"] = plugin
	}
	result := []State{}
	for offset := 0; ; offset += 256 {
		rows, err := app.FindRecordsByFilter("vibetable_plugin_records", filter, "seq", 256, offset, params)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			raw, err := json.Marshal(row.GetRaw("payload_json"))
			if err != nil {
				return nil, err
			}
			var event struct {
				Details State `json:"details"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				return nil, err
			}
			if event.Details.Contract != Contract {
				return nil, errors.New("plugin model progress contract is invalid")
			}
			result = append(result, event.Details)
		}
		if len(rows) < 256 {
			return result, nil
		}
	}
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
