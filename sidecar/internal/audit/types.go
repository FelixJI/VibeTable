package audit

import (
	"fmt"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	Retryable bool           `json:"retryable"`
}

func (value *Error) Error() string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", value.Code, value.Message)
}

type Actor struct {
	UserID      *string `json:"userId"`
	DisplayName *string `json:"displayName"`
}

type ScalarFieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type RelationFieldChange struct {
	Field              string  `json:"field"`
	Kind               string  `json:"kind"`
	RelatedCollection  *string `json:"relatedCollection"`
	RelatedItemID      *string `json:"relatedItemId"`
	DisplayValue       *string `json:"displayValue"`
	BeforeItemID       *string `json:"beforeItemId"`
	AfterItemID        *string `json:"afterItemId"`
	BeforeDisplayValue *string `json:"beforeDisplayValue"`
	AfterDisplayValue  *string `json:"afterDisplayValue"`
	TargetAvailable    bool    `json:"targetAvailable"`
}

type RecordChange struct {
	RevisionID     string                `json:"revisionId"`
	ItemID         string                `json:"itemId"`
	RecordLabel    *string               `json:"recordLabel"`
	Action         string                `json:"action"`
	ScalarChanges  []ScalarFieldChange   `json:"scalarChanges"`
	RelationChange []RelationFieldChange `json:"relationChanges"`
}

type ChangeSet struct {
	RootRevisionID string                `json:"rootRevisionId"`
	ChangeSetID    string                `json:"changeSetId"`
	ActivityID     *string               `json:"activityId"`
	Action         string                `json:"action"`
	Timestamp      string                `json:"timestamp"`
	Actor          *Actor                `json:"actor"`
	ScalarChanges  []ScalarFieldChange   `json:"scalarChanges"`
	RelationChange []RelationFieldChange `json:"relationChanges"`
	ItemID         *string               `json:"itemId"`
	RecordLabel    *string               `json:"recordLabel"`
	RevisionIDs    []string              `json:"revisionIds"`
	AffectedRows   int                   `json:"affectedRecords"`
	RecordChanges  []RecordChange        `json:"recordChanges"`
}

type ReadParams struct {
	TableID  string
	ItemID   *string
	Field    *string
	Search   string
	ActorID  *string
	Actions  []string
	DateFrom *string
	DateTo   *string
	Limit    int
	Offset   int
	Scope    string
	RecordID *string
}

type Page struct {
	Collection                 string            `json:"collection"`
	ItemID                     *string           `json:"itemId"`
	ChangeSets                 []ChangeSet       `json:"changeSets"`
	Total                      int               `json:"total"`
	CapabilityHash             string            `json:"capabilityHash"`
	SchemaRevision             string            `json:"schemaRevision"`
	Scope                      string            `json:"scope"`
	Field                      *string           `json:"field"`
	HasMore                    bool              `json:"hasMore"`
	ArchivedDefaultRevisionIDs map[string]string `json:"archivedDefaultRevisionIds"`
}

type PreviewParams struct {
	TableID        string
	ItemID         string
	TargetRevision string
	Scope          string
	Field          *string
}

type Diagnostic struct {
	Field          string `json:"field"`
	Classification string `json:"classification"`
	Severity       string `json:"severity"`
	Code           string `json:"code"`
	Message        string `json:"message"`
}

type Preview struct {
	Collection      string                `json:"collection"`
	ItemID          string                `json:"itemId"`
	TargetRevision  string                `json:"targetRevision"`
	CurrentHash     string                `json:"currentHash"`
	SchemaRevision  string                `json:"schemaRevision"`
	ScalarChanges   []ScalarFieldChange   `json:"scalarChanges"`
	RelationChanges []RelationFieldChange `json:"relationChanges"`
	Diagnostics     []Diagnostic          `json:"diagnostics"`
	Token           string                `json:"token"`
	ExpiresAt       string                `json:"expiresAt"`
	Scope           string                `json:"scope"`
	Field           *string               `json:"field"`
	CanApply        bool                  `json:"canApply"`
	Restorable      []string              `json:"restorableFields"`
}

type ApplyParams struct {
	TableID string
	ItemID  string
	Token   string
}

type RestoreResult struct {
	Collection         string           `json:"collection"`
	ItemID             string           `json:"itemId"`
	RestoredToRevision string           `json:"restoredToRevision"`
	NewRevisionID      *string          `json:"newRevisionId"`
	Item               map[string]any   `json:"item"`
	Receipt            mutation.Receipt `json:"-"`
}
