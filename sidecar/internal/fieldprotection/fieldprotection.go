// Package fieldprotection owns the host-side persistent field protection
// registry (#591). A workspace manager enables protection for the fields a
// plugin suggests; from then on only that plugin's own transactions may change
// those fields. Protection is recorded as immutable plugin audit events, so it
// survives plugin close, application restart, disable and even uninstall, and
// an administrator can always release it.
//
// The package deliberately imports neither mutation nor pluginstore: the
// mutation kernel enforces the registry, and the app package records events
// through pluginstore inside its own business transactions.
package fieldprotection

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

const (
	// Contract names the manifest declaration `permissions.fieldProtection`.
	Contract = "vibetable.plugin-field-protection.v1"
	// EventEnabled records that a manager enabled protection for explicit fields.
	EventEnabled = "fieldProtection.enabled"
	// EventReleased records that a manager released protection.
	EventReleased = "fieldProtection.released"

	// MaxProtectedFields bounds both the declaration and the fields one plugin
	// may protect at once. It equals the model quota (pluginmodel.MaxEntities
	// entities x 64 fields), so every valid model subset can be protected in
	// full and the real scale (#21: 45 entities, 527 fields) never has to drop
	// a write-owning candidate.
	MaxProtectedFields = pluginmodel.MaxEntities * 64
	// MaxEvents bounds the enable events one project may accumulate. Only
	// enabling is refused at the bound; releasing never consumes the budget and
	// always remains available.
	MaxEvents = 512
)

// DeclaredField is one model field the plugin suggests protecting.
type DeclaredField struct {
	Entity string `json:"entity"`
	Field  string `json:"field"`
}

// Declaration is the manifest `permissions.fieldProtection` object.
type Declaration struct {
	Contract string          `json:"contract"`
	Fields   []DeclaredField `json:"fields"`
}

// UnmarshalJSON keeps the declaration closed: unknown or missing keys fail.
func (d *Declaration) UnmarshalJSON(raw []byte) error {
	var decoded struct {
		Contract string          `json:"contract"`
		Fields   []DeclaredField `json:"fields"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("fieldProtection declaration has trailing data")
	}
	if decoded.Fields == nil {
		return errors.New("fieldProtection.fields is required")
	}
	for _, field := range decoded.Fields {
		if field.Entity == "" || field.Field == "" {
			return errors.New("fieldProtection fields need entity and field")
		}
	}
	*d = Declaration(decoded)
	return nil
}

// Error is a stable product error for declaration or registry failures.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func failure(code, message string) error { return &Error{Code: code, Message: message} }

// Validate checks the declaration against the sibling model declaration. Every
// referenced field must exist in the model on an entity the plugin writes;
// duplicates are refused. Computed projection fields and user-added extension
// fields are never part of the model, so they can never be declared.
func (d Declaration) Validate(model *pluginmodel.Declaration) error {
	if d.Contract != Contract {
		return failure("plugin_field_protection.contract_invalid", "field protection contract is not supported")
	}
	if model == nil {
		return failure("plugin_field_protection.model_required", "field protection requires a model declaration")
	}
	if len(d.Fields) == 0 || len(d.Fields) > MaxProtectedFields {
		return failure("plugin_field_protection.invalid", "field protection needs between 1 and "+itoa(MaxProtectedFields)+" fields")
	}
	seen := map[DeclaredField]struct{}{}
	for _, declared := range d.Fields {
		if declared.Entity == "" || declared.Field == "" {
			return failure("plugin_field_protection.invalid", "field protection entries need entity and field")
		}
		if _, duplicate := seen[declared]; duplicate {
			return failure("plugin_field_protection.duplicate", "a field is declared twice")
		}
		seen[declared] = struct{}{}
		entity := modelEntity(model, declared.Entity)
		if entity == nil {
			return failure("plugin_field_protection.field_unknown", "a declared field is not part of the model")
		}
		// Protection means "only this plugin may change the field"; on an entity
		// the plugin never writes it would freeze the field for everyone.
		if !entityWrites(*entity) {
			return failure("plugin_field_protection.entity_read_only", "field protection requires an entity the plugin writes")
		}
		if !modelEntityHasField(*entity, declared.Field) {
			return failure("plugin_field_protection.field_unknown", "a declared field is not part of the model")
		}
	}
	return nil
}

// Includes reports whether the declaration names the given model field.
func (d Declaration) Includes(entity, field string) bool {
	for _, declared := range d.Fields {
		if declared.Entity == entity && declared.Field == field {
			return true
		}
	}
	return false
}

func modelEntity(model *pluginmodel.Declaration, key string) *pluginmodel.Entity {
	for i := range model.Entities {
		if model.Entities[i].Key == key {
			return &model.Entities[i]
		}
	}
	return nil
}

// entityWrites reports whether the model grants the plugin any write operation.
func entityWrites(entity pluginmodel.Entity) bool {
	for _, operation := range entity.Operations {
		switch operation {
		case "create", "update", "delete":
			return true
		}
	}
	return false
}

func modelEntityHasField(entity pluginmodel.Entity, field string) bool {
	for _, candidate := range entity.Fields {
		if candidate.Key == field {
			return true
		}
	}
	return false
}

func itoa(value int) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// Owner identifies the plugin transaction that may write protected fields.
type Owner struct {
	PluginID       string `json:"pluginId"`
	InstallationID string `json:"installationId"`
}

// FieldRef is the stable identity of one protected field.
type FieldRef struct {
	TableID string `json:"tableId"`
	FieldID string `json:"fieldId"`
}

// Entry is one protected field and the installation that owns it.
type Entry struct {
	TableID        string `json:"tableId"`
	FieldID        string `json:"fieldId"`
	Entity         string `json:"entity"`
	Field          string `json:"field"`
	TableName      string `json:"tableName"`
	FieldName      string `json:"fieldName"`
	FieldType      string `json:"fieldType"`
	PluginID       string `json:"pluginId"`
	InstallationID string `json:"installationId"`
	// Orphaned is set by the manager view only: the table or field no longer
	// exists, so the entry only remains as an audit fact until it is released.
	Orphaned bool `json:"orphaned,omitempty"`
}

// Allows reports whether the writer is the owning installation of the entry.
func Allows(entry Entry, writer *Owner) bool {
	return writer != nil && writer.PluginID == entry.PluginID && writer.InstallationID == entry.InstallationID
}

// Registry is the replayed protection state of one project.
type Registry struct {
	byField    map[FieldRef]Entry
	events     int
	enables    int
	identities map[string]EventIdentity
}

// EventIdentity is the plugin version and package hash a plugin carried when
// it last changed protection. It lets the manager release fields after the
// plugin was uninstalled, when no installation record remains.
type EventIdentity struct {
	PluginVersion string
	PackageHash   string
}

func newRegistry() *Registry {
	return &Registry{byField: map[FieldRef]Entry{}, identities: map[string]EventIdentity{}}
}

// Identity returns the newest non-empty version and package hash recorded for
// a plugin by its protection events.
func (r *Registry) Identity(pluginID string) EventIdentity {
	if r == nil {
		return EventIdentity{}
	}
	return r.identities[pluginID]
}

// Protected returns the entry protecting a field, if any.
func (r *Registry) Protected(tableID, fieldID string) (Entry, bool) {
	if r == nil {
		return Entry{}, false
	}
	entry, ok := r.byField[FieldRef{tableID, fieldID}]
	return entry, ok
}

// Count returns the number of protected fields.
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	return len(r.byField)
}

// Events returns the number of protection events in the project.
func (r *Registry) Events() int {
	if r == nil {
		return 0
	}
	return r.events
}

// EnableEvents returns the number of enable events; only they consume the
// event budget so releasing never exhausts it.
func (r *Registry) EnableEvents() int {
	if r == nil {
		return 0
	}
	return r.enables
}

// Entries lists all protected fields in a stable order.
func (r *Registry) Entries() []Entry {
	return r.filter("")
}

// ForPlugin lists the fields protected by one plugin in a stable order.
func (r *Registry) ForPlugin(pluginID string) []Entry {
	return r.filter(pluginID)
}

func (r *Registry) filter(pluginID string) []Entry {
	result := []Entry{}
	if r == nil {
		return result
	}
	for _, entry := range r.byField {
		if pluginID == "" || entry.PluginID == pluginID {
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TableID != result[j].TableID {
			return result[i].TableID < result[j].TableID
		}
		return result[i].FieldID < result[j].FieldID
	})
	return result
}

// OwnerConflict returns the entry that already protects the field for another
// owner. A reinstall of the same plugin may adopt its own former fields.
func (r *Registry) OwnerConflict(ref FieldRef, pluginID string) (Entry, bool) {
	entry, ok := r.Protected(ref.TableID, ref.FieldID)
	if !ok || entry.PluginID == pluginID {
		return Entry{}, false
	}
	return entry, true
}

// EnabledDetails is the audit payload of an enable event.
type EnabledDetails struct {
	Contract             string  `json:"contract"`
	InstallationID       string  `json:"installationId"`
	InstallationRevision int64   `json:"installationRevision"`
	ModelID              string  `json:"modelId"`
	ModelVersion         int     `json:"modelVersion"`
	Fields               []Entry `json:"fields"`
}

// ReleasedDetails is the audit payload of a release event.
type ReleasedDetails struct {
	Fields []FieldRef `json:"fields"`
	All    bool       `json:"all"`
}

// AuditEvent is the plugin audit envelope shared with pluginstore.
type AuditEvent struct {
	EventID       string          `json:"eventId"`
	ProjectKey    string          `json:"projectKey"`
	PluginID      string          `json:"pluginId"`
	PluginVersion string          `json:"pluginVersion"`
	PackageHash   string          `json:"packageHash"`
	EventType     string          `json:"eventType"`
	Outcome       string          `json:"outcome"`
	Actor         string          `json:"actor"`
	StartedAt     string          `json:"startedAt"`
	Details       json.RawMessage `json:"details"`
}

// NewEnabledEvent builds the audit event appended when protection is enabled.
func NewEnabledEvent(eventID, projectKey, pluginID, pluginVersion, packageHash string, details EnabledDetails, startedAt string) (json.RawMessage, error) {
	details.Contract = Contract
	if details.Fields == nil {
		details.Fields = []Entry{}
	}
	return newEvent(eventID, projectKey, pluginID, pluginVersion, packageHash, EventEnabled, details, startedAt)
}

// NewReleasedEvent builds the audit event appended when protection is released.
func NewReleasedEvent(eventID, projectKey, pluginID, pluginVersion, packageHash string, details ReleasedDetails, startedAt string) (json.RawMessage, error) {
	if details.Fields == nil {
		details.Fields = []FieldRef{}
	}
	return newEvent(eventID, projectKey, pluginID, pluginVersion, packageHash, EventReleased, details, startedAt)
}

func newEvent(eventID, projectKey, pluginID, pluginVersion, packageHash, eventType string, details any, startedAt string) (json.RawMessage, error) {
	raw, err := json.Marshal(details)
	if err != nil {
		return nil, err
	}
	return json.Marshal(AuditEvent{
		EventID: eventID, ProjectKey: projectKey, PluginID: pluginID,
		PluginVersion: pluginVersion, PackageHash: packageHash,
		EventType: eventType, Outcome: "succeeded", Actor: "local-user",
		StartedAt: startedAt, Details: raw,
	})
}

// Load replays the immutable protection events of the whole project in audit
// order. The latest event per field wins; a corrupt event fails closed so the
// kernel never writes with unknown protection.
//
// Each event type is read with its own equality filter so the existing plugin
// event expression index (kind, eventType, project_key, plugin_id, seq)
// answers it: with protection off the two probes return no row and never scan
// the plugin audit history. The kernel loads at most once per transaction.
func Load(ctx context.Context, app core.App) (*Registry, error) {
	if _, err := app.FindCollectionByNameOrId("vibetable_plugin_records"); errors.Is(err, sql.ErrNoRows) {
		return newRegistry(), nil
	} else if err != nil {
		return nil, err
	}
	type staged struct {
		seq     int64
		payload any
	}
	events := []staged{}
	for _, eventType := range []string{EventEnabled, EventReleased} {
		for offset := 0; ; offset += 256 {
			rows, err := app.FindRecordsByFilter(
				"vibetable_plugin_records",
				"kind='audit' && payload_json.eventType={:eventType}",
				"seq", 256, offset,
				map[string]any{"eventType": eventType},
			)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				events = append(events, staged{seq: int64(row.GetInt("seq")), payload: row.GetRaw("payload_json")})
			}
			if len(rows) < 256 {
				break
			}
		}
	}
	// seq is the per-project audit sequence: replaying both event types in one
	// order makes the latest event per field win across enable/release pairs.
	sort.SliceStable(events, func(i, j int) bool { return events[i].seq < events[j].seq })
	registry := newRegistry()
	for _, event := range events {
		if err := registry.apply(event.payload); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) apply(payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return failure("field_protection.events_corrupt", "field protection event cannot be read")
	}
	var event AuditEvent
	if err := json.Unmarshal(raw, &event); err != nil || event.PluginID == "" ||
		event.Details == nil {
		return failure("field_protection.events_corrupt", "field protection event is invalid")
	}
	switch event.EventType {
	case EventEnabled:
		var details EnabledDetails
		if json.Unmarshal(event.Details, &details) != nil || details.InstallationID == "" ||
			details.ModelID == "" || len(details.Fields) == 0 || len(details.Fields) > MaxProtectedFields {
			return failure("field_protection.events_corrupt", "field protection event is invalid")
		}
		for _, entry := range details.Fields {
			if entry.TableID == "" || entry.FieldID == "" || entry.Entity == "" || entry.Field == "" {
				return failure("field_protection.events_corrupt", "field protection event is invalid")
			}
			entry.PluginID = event.PluginID
			entry.InstallationID = details.InstallationID
			r.byField[FieldRef{entry.TableID, entry.FieldID}] = entry
		}
		r.enables++
	case EventReleased:
		var details ReleasedDetails
		if json.Unmarshal(event.Details, &details) != nil || (!details.All && len(details.Fields) == 0) {
			return failure("field_protection.events_corrupt", "field protection event is invalid")
		}
		if details.All {
			for ref, entry := range r.byField {
				if entry.PluginID == event.PluginID {
					delete(r.byField, ref)
				}
			}
		} else {
			for _, ref := range details.Fields {
				if entry, ok := r.byField[ref]; ok && entry.PluginID == event.PluginID {
					delete(r.byField, ref)
				}
			}
		}
	default:
		return failure("field_protection.events_corrupt", "field protection event is unknown")
	}
	if event.PluginVersion != "" || event.PackageHash != "" {
		r.identities[event.PluginID] = EventIdentity{PluginVersion: event.PluginVersion, PackageHash: event.PackageHash}
	}
	r.events++
	return nil
}
