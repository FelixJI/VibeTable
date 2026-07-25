package mutation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const ContractVersion = "1.0"

type OperationKind string

const (
	OperationInsert         OperationKind = "insert"
	OperationUpdate         OperationKind = "update"
	OperationArchive        OperationKind = "archive"
	OperationRestore        OperationKind = "restore"
	OperationDelete         OperationKind = "delete"
	OperationSetAttachments OperationKind = "setAttachments"
)

type Request struct {
	ContractVersion  string      `json:"contractVersion"`
	RequestID        string      `json:"requestId"`
	IdempotencyKey   string      `json:"idempotencyKey"`
	TableID          string      `json:"tableId"`
	SchemaRevision   string      `json:"schemaRevision"`
	Operations       []Operation `json:"operations"`
	Actor            Actor       `json:"actor"`
	ExpectedRevision *string     `json:"expectedRevision"`
	ExpectedDigest   *string     `json:"expectedDigest"`
}

type Actor struct {
	Type        string  `json:"type"`
	ID          string  `json:"id"`
	DisplayName *string `json:"displayName"`
}

func (actor *Actor) UnmarshalJSON(raw []byte) error {
	type actorWire Actor
	var decoded actorWire
	if err := decodeObject(raw, &decoded, "type", "id", "displayName"); err != nil {
		return err
	}
	*actor = Actor(decoded)
	return nil
}

type Operation struct {
	Kind OperationKind

	RecordID         *string
	Values           map[string]any
	ExpectedRevision *string
	ExpectedDigest   *string

	FieldID           string
	UploadHandles     []string
	RemoveStoredNames []string
}

func (operation Operation) MarshalJSON() ([]byte, error) {
	switch operation.Kind {
	case OperationInsert:
		return json.Marshal(struct {
			Kind     OperationKind  `json:"kind"`
			RecordID *string        `json:"recordId"`
			Values   map[string]any `json:"values"`
		}{operation.Kind, operation.RecordID, nonNilMap(operation.Values)})
	case OperationUpdate:
		return json.Marshal(struct {
			Kind             OperationKind  `json:"kind"`
			RecordID         string         `json:"recordId"`
			Values           map[string]any `json:"values"`
			ExpectedRevision *string        `json:"expectedRevision,omitempty"`
			ExpectedDigest   *string        `json:"expectedDigest,omitempty"`
		}{
			operation.Kind, dereference(operation.RecordID),
			nonNilMap(operation.Values), operation.ExpectedRevision,
			operation.ExpectedDigest,
		})
	case OperationArchive, OperationRestore, OperationDelete:
		return json.Marshal(struct {
			Kind             OperationKind `json:"kind"`
			RecordID         string        `json:"recordId"`
			ExpectedRevision *string       `json:"expectedRevision,omitempty"`
			ExpectedDigest   *string       `json:"expectedDigest,omitempty"`
		}{
			operation.Kind, dereference(operation.RecordID),
			operation.ExpectedRevision, operation.ExpectedDigest,
		})
	case OperationSetAttachments:
		return json.Marshal(struct {
			Kind              OperationKind `json:"kind"`
			RecordID          string        `json:"recordId"`
			FieldID           string        `json:"fieldId"`
			UploadHandles     []string      `json:"uploadHandles"`
			RemoveStoredNames []string      `json:"removeStoredNames"`
		}{
			operation.Kind, dereference(operation.RecordID), operation.FieldID,
			nonNilStrings(operation.UploadHandles), nonNilStrings(operation.RemoveStoredNames),
		})
	default:
		return nil, fmt.Errorf("unknown mutation operation kind %q", operation.Kind)
	}
}

func (operation *Operation) UnmarshalJSON(raw []byte) error {
	var header struct {
		Kind OperationKind `json:"kind"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	operation.Kind = header.Kind
	switch header.Kind {
	case OperationInsert:
		var decoded struct {
			Kind     OperationKind  `json:"kind"`
			RecordID *string        `json:"recordId"`
			Values   map[string]any `json:"values"`
		}
		if err := decodeObject(raw, &decoded, "kind", "recordId", "values"); err != nil {
			return err
		}
		if decoded.Values == nil {
			return fmt.Errorf("insert values must be an object")
		}
		operation.RecordID, operation.Values = decoded.RecordID, decoded.Values
	case OperationUpdate:
		var decoded struct {
			Kind             OperationKind  `json:"kind"`
			RecordID         string         `json:"recordId"`
			Values           map[string]any `json:"values"`
			ExpectedRevision *string        `json:"expectedRevision"`
			ExpectedDigest   *string        `json:"expectedDigest"`
		}
		if err := decodeObject(raw, &decoded, "kind", "recordId", "values"); err != nil {
			return err
		}
		if decoded.RecordID == "" || decoded.Values == nil {
			return fmt.Errorf("update recordId and values are required")
		}
		operation.RecordID, operation.Values = &decoded.RecordID, decoded.Values
		operation.ExpectedRevision = decoded.ExpectedRevision
		operation.ExpectedDigest = decoded.ExpectedDigest
	case OperationArchive, OperationRestore, OperationDelete:
		var decoded struct {
			Kind             OperationKind `json:"kind"`
			RecordID         string        `json:"recordId"`
			ExpectedRevision *string       `json:"expectedRevision"`
			ExpectedDigest   *string       `json:"expectedDigest"`
		}
		if err := decodeObject(raw, &decoded, "kind", "recordId"); err != nil {
			return err
		}
		if decoded.RecordID == "" {
			return fmt.Errorf("%s recordId is required", header.Kind)
		}
		operation.RecordID = &decoded.RecordID
		operation.ExpectedRevision = decoded.ExpectedRevision
		operation.ExpectedDigest = decoded.ExpectedDigest
	case OperationSetAttachments:
		var decoded struct {
			Kind              OperationKind `json:"kind"`
			RecordID          string        `json:"recordId"`
			FieldID           string        `json:"fieldId"`
			UploadHandles     []string      `json:"uploadHandles"`
			RemoveStoredNames []string      `json:"removeStoredNames"`
		}
		if err := decodeObject(raw, &decoded, "kind", "recordId", "fieldId", "uploadHandles", "removeStoredNames"); err != nil {
			return err
		}
		if decoded.RecordID == "" || decoded.FieldID == "" ||
			decoded.UploadHandles == nil || decoded.RemoveStoredNames == nil {
			return fmt.Errorf("setAttachments fields are required")
		}
		operation.RecordID, operation.FieldID = &decoded.RecordID, decoded.FieldID
		operation.UploadHandles, operation.RemoveStoredNames = decoded.UploadHandles, decoded.RemoveStoredNames
	default:
		return fmt.Errorf("unknown mutation operation kind %q", header.Kind)
	}
	return nil
}

type ReceiptStatus string

const (
	StatusApplied  ReceiptStatus = "applied"
	StatusReplayed ReceiptStatus = "replayed"
	StatusPending  ReceiptStatus = "pending"
	StatusRejected ReceiptStatus = "rejected"
)

type Receipt struct {
	ContractVersion string                    `json:"contractVersion"`
	Status          ReceiptStatus             `json:"status"`
	ChangeSetID     *string                   `json:"changeSetId"`
	AffectedRows    []AffectedRow             `json:"affectedRows"`
	ComputedFields  map[string]map[string]any `json:"computedFields"`
	NewRevision     *string                   `json:"newRevision"`
	EmittedEvents   []string                  `json:"emittedEvents"`
	Warnings        []ProductError            `json:"warnings"`
}

type AffectedRow struct {
	RecordID  string        `json:"recordId"`
	Operation OperationKind `json:"operation"`
	Revision  string        `json:"revision"`
	Digest    string        `json:"digest"`
}

type ProductError struct {
	ContractVersion string         `json:"contractVersion"`
	Code            string         `json:"code"`
	Path            *string        `json:"path"`
	Message         string         `json:"message"`
	Details         map[string]any `json:"details"`
	Retryable       bool           `json:"retryable"`
}

func (e *ProductError) Error() string {
	path := ""
	if e.Path != nil {
		path = *e.Path
	}
	return fmt.Sprintf("%s at %s: %s", e.Code, path, e.Message)
}

type DataChangedEvent struct {
	ContractVersion string              `json:"contractVersion"`
	Topic           string              `json:"topic"`
	EventID         string              `json:"eventId"`
	Sequence        int64               `json:"sequence"`
	OccurredAt      string              `json:"occurredAt"`
	SchemaRevision  string              `json:"schemaRevision"`
	DataRevision    string              `json:"dataRevision"`
	ChangeSetID     *string             `json:"changeSetId"`
	TableID         string              `json:"tableId"`
	RecordIDs       []string            `json:"recordIds"`
	Operation       DataChangeOperation `json:"operation"`
}

type DataChangeOperation string

const (
	DataChangeInsert  DataChangeOperation = "insert"
	DataChangeUpdate  DataChangeOperation = "update"
	DataChangeArchive DataChangeOperation = "archive"
	DataChangeRestore DataChangeOperation = "restore"
	DataChangeDelete  DataChangeOperation = "delete"
	DataChangeSchema  DataChangeOperation = "schema"
)

func DecodeStrict(raw []byte, target any) error {
	switch target.(type) {
	case *Request:
		if err := requireObjectFields(raw,
			"contractVersion", "requestId", "idempotencyKey", "tableId",
			"schemaRevision", "operations", "actor", "expectedRevision", "expectedDigest",
		); err != nil {
			return err
		}
	case *Receipt:
		if err := requireObjectFields(raw,
			"contractVersion", "status", "changeSetId", "affectedRows",
			"computedFields", "newRevision", "emittedEvents", "warnings",
		); err != nil {
			return err
		}
	case *DataChangedEvent:
		if err := requireObjectFields(raw,
			"contractVersion", "topic", "eventId", "sequence", "occurredAt",
			"schemaRevision", "dataRevision", "changeSetId", "tableId",
			"recordIds", "operation",
		); err != nil {
			return err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func decodeObject(raw []byte, target any, required ...string) error {
	if err := requireObjectFields(raw, required...); err != nil {
		return err
	}
	return DecodeStrict(raw, target)
}

func requireObjectFields(raw []byte, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	return nil
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}
