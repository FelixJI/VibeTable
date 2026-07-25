package relation

import (
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

type Descriptor struct {
	RelationID                   string   `json:"relationId"`
	SourceTableID                string   `json:"sourceTableId"`
	SourceFieldID                string   `json:"sourceFieldId"`
	PhysicalName                 string   `json:"physicalName"`
	Mode                         string   `json:"mode"`
	TargetTableID                string   `json:"targetTableId"`
	Cardinality                  string   `json:"cardinality"`
	DeletePolicy                 string   `json:"deletePolicy"`
	JunctionTableID              *string  `json:"junctionTableId,omitempty"`
	JunctionSourceFieldID        string   `json:"junctionSourceFieldId,omitempty"`
	JunctionTargetFieldID        string   `json:"junctionTargetFieldId,omitempty"`
	JunctionDiscriminatorFieldID string   `json:"junctionDiscriminatorFieldId,omitempty"`
	AllowedTargetTableIDs        []string `json:"allowedTargetTableIds"`
}

type LookupDescriptor struct {
	LookupID        string                 `json:"lookupId"`
	TableID         string                 `json:"tableId"`
	FieldID         string                 `json:"fieldId"`
	PhysicalName    string                 `json:"physicalName"`
	DisplayName     string                 `json:"displayName"`
	RelationFieldID string                 `json:"relationFieldId"`
	Path            []LookupPathDescriptor `json:"path"`
	TargetFieldID   string                 `json:"targetFieldId"`
	JunctionFieldID string                 `json:"junctionFieldId,omitempty"`
	TargetFieldIDs  map[string]string      `json:"targetFieldIds,omitempty"`
	Aggregate       string                 `json:"aggregate"`
	OutputStorage   schema.StorageType     `json:"outputStorage"`
	Revision        int                    `json:"revision"`
}

type LookupPathDescriptor struct {
	RelationID    string `json:"relationId"`
	M2ACollection string `json:"m2aCollection,omitempty"`
}

type CatalogResult struct {
	TableID        string             `json:"tableId"`
	SchemaRevision string             `json:"schemaRevision"`
	Relations      []Descriptor       `json:"relations"`
	Lookups        []LookupDescriptor `json:"lookups"`
}

type SearchRequest struct {
	RelationID    string `json:"relationId"`
	TargetTableID string `json:"targetTableId,omitempty"`
	Query         string `json:"query"`
	Offset        int    `json:"offset"`
	Limit         int    `json:"limit"`
}

type TargetRef struct {
	TableID          string         `json:"tableId"`
	RecordID         string         `json:"recordId"`
	Label            string         `json:"label"`
	JunctionID       string         `json:"junctionId,omitempty"`
	JunctionRevision string         `json:"junctionRevision,omitempty"`
	JunctionValues   map[string]any `json:"junctionValues"`
}

type JunctionUpdate struct {
	JunctionID       string         `json:"junctionId"`
	Values           map[string]any `json:"values"`
	ExpectedRevision *string        `json:"expectedRevision,omitempty"`
	ExpectedDigest   *string        `json:"expectedDigest,omitempty"`
}

type SearchResult struct {
	Items    []TargetRef         `json:"items"`
	Total    int64               `json:"total"`
	Snapshot query.QuerySnapshot `json:"snapshot"`
}

type DeltaRequest struct {
	RelationID     string           `json:"relationId"`
	SourceRecordID string           `json:"sourceRecordId"`
	SchemaRevision string           `json:"schemaRevision"`
	Adds           []TargetRef      `json:"adds"`
	Updates        []JunctionUpdate `json:"updates"`
	Removes        []TargetRef      `json:"removes"`
	RequestID      string           `json:"requestId"`
	IdempotencyKey string           `json:"idempotencyKey"`
	ExpectedDigest *string          `json:"expectedDigest,omitempty"`
	Actor          mutation.Actor   `json:"actor"`
}

type DeltaPreview struct {
	RelationID     string      `json:"relationId"`
	SourceRecordID string      `json:"sourceRecordId"`
	Current        []TargetRef `json:"current"`
	Result         []TargetRef `json:"result"`
	Adds           int         `json:"adds"`
	Removes        int         `json:"removes"`
	CanApply       bool        `json:"canApply"`
}

type DeltaResult struct {
	Current []TargetRef      `json:"current"`
	Receipt mutation.Receipt `json:"receipt"`
}

type LookupQueryRequest struct {
	TableID        string           `json:"tableId"`
	SchemaRevision string           `json:"schemaRevision"`
	Query          query.TableQuery `json:"query"`
}

type LookupPreviewRequest struct {
	Definition schema.TableDefinition `json:"definition"`
	FieldIDs   []string               `json:"fieldIds"`
	Query      query.TableQuery       `json:"query"`
}
