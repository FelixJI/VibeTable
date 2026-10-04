// Package sourceimport owns bounded, create-only source migrations. Source
// models enter via the Host provider channel, never as renderer mutations.
package sourceimport

import (
	"fmt"
	"time"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const Contract = "vibetable.source-import.v1"

const (
	PolicyNative   = "native"
	PolicySnapshot = "snapshot"
	PolicySkip     = "skip"
	PolicyBlocked  = "blocked"
)

// Key is structured: labels and concatenated source strings are not identities.
type Key struct {
	Provider    string `json:"provider"`
	ContainerID string `json:"containerId"`
	TableID     string `json:"tableId"`
	FieldID     string `json:"fieldId,omitempty"`
	RecordID    string `json:"recordId,omitempty"`
	ObjectID    string `json:"objectId,omitempty"`
}

type ReadWindow struct {
	StartedAt   string `json:"startedAt"`
	FinishedAt  string `json:"finishedAt"`
	Consistency string `json:"consistency"`
}

type Snapshot struct {
	Contract    string       `json:"contract"`
	Provider    string       `json:"provider"`
	ContainerID string       `json:"containerId"`
	DisplayName string       `json:"displayName"`
	Version     string       `json:"version"`
	ReadWindow  ReadWindow   `json:"readWindow"`
	Tables      []Table      `json:"tables"`
	Attachments []Attachment `json:"attachments"`
}

type Table struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	PrimaryFieldID string   `json:"primaryFieldId"`
	Fields         []Field  `json:"fields"`
	Records        []Record `json:"records"`
}

// Field carries source semantics, not provider-shaped PocketBase definitions.
// Definition can retain a source expression, never connector credentials.
type Field struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	ValueKind    v2.LogicalType `json:"valueKind"`
	Required     bool           `json:"required"`
	Options      []Option       `json:"options"`
	Relation     *Relation      `json:"relation,omitempty"`
	NumberFormat *NumberFormat  `json:"numberFormat,omitempty"`
	Timezone     string         `json:"timezone"`
	Definition   string         `json:"definition"`
}

type NumberFormat struct {
	OnlyInt           bool    `json:"onlyInt"`
	DisplayScale      int     `json:"displayScale"`
	ScaleMode         string  `json:"scaleMode"`
	TrimTrailingZeros bool    `json:"trimTrailingZeros"`
	UseGrouping       bool    `json:"useGrouping"`
	Currency          string  `json:"currency"`
	PercentStorage    string  `json:"percentStorage"`
	Unit              *string `json:"unit"`
}

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Color string `json:"color"`
}

type Relation struct {
	TargetTableID string `json:"targetTableId"`
	TargetFieldID string `json:"targetFieldId"`
	Cardinality   string `json:"cardinality"`
}

type Record struct {
	ID     string         `json:"id"`
	Values map[string]any `json:"values"`
}

// Attachment retains safe metadata. Temporary URLs remain in Host memory;
// actual bytes use the existing controlled attachment upload capability.
type Attachment struct {
	ID       string `json:"id"`
	TableID  string `json:"tableId"`
	RecordID string `json:"recordId"`
	FieldID  string `json:"fieldId"`
	Name     string `json:"name"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
}

type Decision struct {
	TableID    string         `json:"tableId"`
	FieldID    string         `json:"fieldId"`
	Policy     string         `json:"policy"`
	TargetKind v2.LogicalType `json:"targetKind"`
	// TargetName renames the local target column for this field decision. For
	// one-sided source relations it names the auto-created reciprocal field
	// (default "迁移反向关联"). Names are explicit user decisions; identities
	// are never guessed from labels and source columns are never auto-renamed.
	TargetName string `json:"targetName,omitempty"`
	Confirmed  bool   `json:"confirmed"`
}

type TargetName struct {
	TableID string `json:"tableId"`
	Name    string `json:"name"`
}

type Options struct {
	SelectedTableIDs []string     `json:"selectedTableIds"`
	TargetNames      []TargetName `json:"targetNames"`
	Decisions        []Decision   `json:"decisions"`
	ConfirmReverse   bool         `json:"confirmReverse"`
}

type Diagnostic struct {
	Code     string `json:"code"`
	TableID  string `json:"tableId,omitempty"`
	FieldID  string `json:"fieldId,omitempty"`
	RecordID string `json:"recordId,omitempty"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

type FieldPlan struct {
	Source         Field         `json:"source"`
	Policy         string        `json:"policy"`
	Draft          v2.FieldDraft `json:"draft"`
	Deferred       bool          `json:"deferred"`
	ReciprocalName string        `json:"reciprocalName,omitempty"`
}

type TablePlan struct {
	SourceID       string      `json:"sourceId"`
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	PrimaryFieldID string      `json:"primaryFieldId"`
	Fields         []FieldPlan `json:"fields"`
	Records        []Record    `json:"records"`
}

type Plan struct {
	Contract    string       `json:"contract"`
	Provider    string       `json:"provider"`
	ContainerID string       `json:"containerId"`
	DisplayName string       `json:"displayName"`
	Version     string       `json:"version"`
	ReadWindow  ReadWindow   `json:"readWindow"`
	Tables      []TablePlan  `json:"tables"`
	Attachments []Attachment `json:"attachments"`
	// Fields mirrors the durable Result provenance summaries exactly, so the
	// persisted job records what was reviewed without rebuilding it later.
	Fields      []FieldSummary `json:"fields"`
	Diagnostics []Diagnostic   `json:"diagnostics"`
	CanApply    bool           `json:"canApply"`
}

type Observation struct {
	Version       string            `json:"version"`
	TableVersions map[string]string `json:"tableVersions"`
}

func (plan Plan) CheckObservation(observation Observation) error {
	if observation.Version != plan.Version {
		return &Error{Code: "source_import.source_changed", Message: "来源版本已变化，请重新预检"}
	}
	for _, table := range plan.Tables {
		if version, ok := observation.TableVersions[table.SourceID]; !ok || version != table.Version {
			return &Error{Code: "source_import.source_changed", Message: "来源表结构版本已变化，请重新预检"}
		}
	}
	return nil
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (err *Error) Error() string { return fmt.Sprintf("%s: %s", err.Code, err.Message) }

func validWindow(window ReadWindow) bool {
	start, err := time.Parse(time.RFC3339Nano, window.StartedAt)
	if err != nil {
		return false
	}
	end, err := time.Parse(time.RFC3339Nano, window.FinishedAt)
	return err == nil && !end.Before(start) && (window.Consistency == "snapshot" || window.Consistency == "window")
}
