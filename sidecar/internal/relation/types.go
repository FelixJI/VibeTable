package relation

import (
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
)

type Descriptor struct {
	RelationID          string `json:"relationId"`
	SourceTableID       string `json:"sourceTableId"`
	SourceFieldID       string `json:"sourceFieldId"`
	PhysicalName        string `json:"physicalName"`
	TargetTableID       string `json:"targetTableId"`
	Cardinality         string `json:"cardinality"`
	DeletePolicy        string `json:"deletePolicy"`
	PairID              string `json:"pairId,omitempty"`
	ReciprocalFieldID   string `json:"reciprocalFieldId,omitempty"`
	QuickCreateEligible bool   `json:"quickCreateEligible"`
	QuickCreateReason   string `json:"quickCreateReason,omitempty"`
}

type LookupDescriptor struct {
	LookupID          string                 `json:"lookupId"`
	TableID           string                 `json:"tableId"`
	FieldID           string                 `json:"fieldId"`
	PhysicalName      string                 `json:"physicalName"`
	DisplayName       string                 `json:"displayName"`
	RelationFieldID   string                 `json:"relationFieldId"`
	Path              []LookupPathDescriptor `json:"path"`
	TargetFieldID     string                 `json:"targetFieldId"`
	ResultCardinality string                 `json:"resultCardinality"`
	OutputStorage     string                 `json:"outputStorage"`
	Revision          int                    `json:"revision"`
}

type LookupPathDescriptor struct {
	RelationID string `json:"relationId"`
}

type CatalogResult struct {
	TableID        string             `json:"tableId"`
	SchemaRevision string             `json:"schemaRevision"`
	LookupMaxDepth int                `json:"lookupMaxDepth"`
	Relations      []Descriptor       `json:"relations"`
	Lookups        []LookupDescriptor `json:"lookups"`
}

type SearchRequest struct {
	RelationID    string `json:"relationId"`
	Query         string `json:"query"`
	Offset        int    `json:"offset"`
	Limit         int    `json:"limit"`
	TargetTableID string `json:"-"`
}

type TargetRef struct {
	TableID        string `json:"tableId"`
	RecordID       string `json:"recordId"`
	Label          string `json:"label"`
	SecondaryLabel string `json:"secondaryLabel,omitempty"`
}

type SearchResult struct {
	Items    []TargetRef         `json:"items"`
	Total    int64               `json:"total"`
	Snapshot query.QuerySnapshot `json:"snapshot"`
}

type CreateTargetRequest struct {
	RelationID     string         `json:"relationId"`
	Label          string         `json:"label"`
	Values         map[string]any `json:"values,omitempty"`
	RequestID      string         `json:"requestId"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Actor          mutation.Actor `json:"actor"`
	TargetTableID  string         `json:"-"`
}

type CreateTargetResult struct {
	Target  TargetRef        `json:"target"`
	Receipt mutation.Receipt `json:"receipt"`
}

type DeltaRequest struct {
	RelationID     string         `json:"relationId"`
	SourceRecordID string         `json:"sourceRecordId"`
	SchemaRevision string         `json:"schemaRevision"`
	Adds           []TargetRef    `json:"adds"`
	Removes        []TargetRef    `json:"removes"`
	RequestID      string         `json:"requestId"`
	IdempotencyKey string         `json:"idempotencyKey"`
	ExpectedDigest *string        `json:"expectedDigest,omitempty"`
	Actor          mutation.Actor `json:"actor"`
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
	TableID        string            `json:"tableId"`
	SchemaRevision string            `json:"schemaRevision"`
	Query          query.TableQuery  `json:"query"`
	Groups         []query.GroupSpec `json:"groups,omitempty"`
	GroupLimit     int               `json:"groupLimit,omitempty"`
}

type LookupQueryResult struct {
	query.Page
	GroupRows     []query.GroupRow `json:"groupRows"`
	GroupOffset   int              `json:"groupOffset"`
	GroupLimit    int              `json:"groupLimit"`
	HasMoreGroups bool             `json:"hasMoreGroups"`
}

type LookupValuePageRequest struct {
	TableID        string `json:"tableId"`
	SchemaRevision string `json:"schemaRevision"`
	SourceRecordID string `json:"sourceRecordId"`
	FieldID        string `json:"fieldId"`
	Offset         int    `json:"offset"`
	Limit          int    `json:"limit"`
}
