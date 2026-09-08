// Code generated from contracts/workbench/workbench.schema.json; DO NOT EDIT.
package workbench

type ViewFilter struct {
	FieldId  string `json:"fieldId"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

type ViewSort struct {
	FieldId   string `json:"fieldId"`
	Direction string `json:"direction"`
}

type ViewQuery struct {
	ContractVersion string       `json:"contractVersion"`
	TableId         string       `json:"tableId"`
	Fields          []string     `json:"fields"`
	Filters         []ViewFilter `json:"filters"`
	Sorts           []ViewSort   `json:"sorts"`
	Cursor          *string      `json:"cursor"`
	PageSize        int64        `json:"pageSize"`
}

type BindingVariable struct {
	VariableId      string  `json:"variableId"`
	TargetFieldId   string  `json:"targetFieldId"`
	Operator        string  `json:"operator"`
	Source          string  `json:"source"`
	SourceBindingId *string `json:"sourceBindingId"`
	SourceFieldId   *string `json:"sourceFieldId"`
	Value           any     `json:"value"`
}

type DataBinding struct {
	BindingId string            `json:"bindingId"`
	Query     ViewQuery         `json:"query"`
	Variables []BindingVariable `json:"variables"`
}

type InterfaceAction struct {
	ActionId             string  `json:"actionId"`
	Kind                 string  `json:"kind"`
	BindingId            *string `json:"bindingId"`
	TargetPageId         *string `json:"targetPageId"`
	PluginId             *string `json:"pluginId"`
	PluginActionId       *string `json:"pluginActionId"`
	RequiresConfirmation bool    `json:"requiresConfirmation"`
}

type InterfaceElement struct {
	ElementId string             `json:"elementId"`
	Kind      string             `json:"kind"`
	BindingId *string            `json:"bindingId"`
	ActionId  *string            `json:"actionId"`
	Text      *string            `json:"text"`
	Width     string             `json:"width"`
	Children  []InterfaceElement `json:"children"`
}

type InterfacePage struct {
	PageId   string             `json:"pageId"`
	Title    string             `json:"title"`
	Elements []InterfaceElement `json:"elements"`
}

type InterfaceDefinition struct {
	ContractVersion string            `json:"contractVersion"`
	InterfaceId     string            `json:"interfaceId"`
	Name            string            `json:"name"`
	Bindings        []DataBinding     `json:"bindings"`
	Actions         []InterfaceAction `json:"actions"`
	Pages           []InterfacePage   `json:"pages"`
}

type InterfaceSnapshot struct {
	Definition InterfaceDefinition `json:"definition"`
	Revision   string              `json:"revision"`
}

type InterfaceCommitRequest struct {
	Definition       InterfaceDefinition `json:"definition"`
	ExpectedRevision *string             `json:"expectedRevision"`
	IdempotencyKey   string              `json:"idempotencyKey"`
}

type InterfaceListRequest struct {
}

type InterfaceListEntry struct {
	InterfaceId string `json:"interfaceId"`
	Name        string `json:"name"`
	Revision    string `json:"revision"`
}

type InterfaceListResult struct {
	Items []InterfaceListEntry `json:"items"`
}

type InterfaceLoadRequest struct {
	InterfaceId string `json:"interfaceId"`
}

type InterfaceCancelRequest struct {
	TargetRequestId string `json:"targetRequestId"`
}

type InterfaceDeleteRequest struct {
	InterfaceId      string `json:"interfaceId"`
	ExpectedRevision string `json:"expectedRevision"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type InterfaceDeleteResult struct {
	InterfaceId string `json:"interfaceId"`
}

type ContentProfile struct {
	ContractVersion    string   `json:"contractVersion"`
	TableId            string   `json:"tableId"`
	TitleFieldId       string   `json:"titleFieldId"`
	BodyFieldId        string   `json:"bodyFieldId"`
	SummaryFieldId     *string  `json:"summaryFieldId"`
	SearchableFieldIds []string `json:"searchableFieldIds"`
}

type ContentProfileSnapshot struct {
	Profile  ContentProfile `json:"profile"`
	Revision string         `json:"revision"`
}

type ContentProfileLoadRequest struct {
	TableId string `json:"tableId"`
}

type ContentProfileCommitRequest struct {
	Profile          ContentProfile `json:"profile"`
	ExpectedRevision *string        `json:"expectedRevision"`
	IdempotencyKey   string         `json:"idempotencyKey"`
}

type ContentProfileDeleteRequest struct {
	TableId          string `json:"tableId"`
	ExpectedRevision string `json:"expectedRevision"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type ContentProfileDeleteResult struct {
	TableId string `json:"tableId"`
}

type RecordDocumentLink struct {
	ContractVersion string `json:"contractVersion"`
	LinkId          string `json:"linkId"`
	TableId         string `json:"tableId"`
	RecordId        string `json:"recordId"`
	DocumentId      string `json:"documentId"`
	Role            string `json:"role"`
	Order           int64  `json:"order"`
}

type RecordDocumentLinkSnapshot struct {
	Link     RecordDocumentLink `json:"link"`
	Revision string             `json:"revision"`
}

type RecordDocumentLinkListRequest struct {
	TableId  string `json:"tableId"`
	RecordId string `json:"recordId"`
}

type RecordDocumentLinkListResult struct {
	Items []RecordDocumentLinkSnapshot `json:"items"`
}

type RecordDocumentLinkCommitRequest struct {
	Link             RecordDocumentLink `json:"link"`
	ExpectedRevision *string            `json:"expectedRevision"`
	IdempotencyKey   string             `json:"idempotencyKey"`
}

type RecordDocumentLinkRepairRequest struct {
	LinkId           string `json:"linkId"`
	DocumentId       string `json:"documentId"`
	ExpectedRevision string `json:"expectedRevision"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type RecordDocumentLinkDeleteRequest struct {
	LinkId           string `json:"linkId"`
	ExpectedRevision string `json:"expectedRevision"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type RecordDocumentLinkDeleteResult struct {
	LinkId string `json:"linkId"`
}

type SearchFilter struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

type SearchSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type SearchOpenTarget struct {
	Kind       string  `json:"kind"`
	TableId    *string `json:"tableId"`
	RecordId   *string `json:"recordId"`
	FieldId    *string `json:"fieldId"`
	DocumentId *string `json:"documentId"`
}

type SearchMetadataItem struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type SearchRequest struct {
	ContractVersion string         `json:"contractVersion"`
	Query           string         `json:"query"`
	Logic           string         `json:"logic"`
	Filters         []SearchFilter `json:"filters"`
	Sorts           []SearchSort   `json:"sorts"`
	Scope           string         `json:"scope"`
	Cursor          *string        `json:"cursor"`
	Limit           int64          `json:"limit"`
}

type SearchHit struct {
	ContractVersion string               `json:"contractVersion"`
	HitId           string               `json:"hitId"`
	Kind            string               `json:"kind"`
	CanonicalId     string               `json:"canonicalId"`
	Title           string               `json:"title"`
	Snippet         *string              `json:"snippet"`
	Highlights      []string             `json:"highlights"`
	SourceRevision  string               `json:"sourceRevision"`
	Score           float64              `json:"score"`
	RevisionTime    string               `json:"revisionTime"`
	Metadata        []SearchMetadataItem `json:"metadata"`
	OpenTarget      SearchOpenTarget     `json:"openTarget"`
}

type SearchResolveRequest struct {
	ContractVersion string    `json:"contractVersion"`
	Scope           string    `json:"scope"`
	Hit             SearchHit `json:"hit"`
}

type SearchResolveResult struct {
	Status string    `json:"status"`
	Hit    SearchHit `json:"hit"`
}

type SearchStatus struct {
	State      string  `json:"state"`
	Generation int64   `json:"generation"`
	Checkpoint *string `json:"checkpoint"`
	Processed  int64   `json:"processed"`
	Total      *int64  `json:"total"`
	ErrorCode  *string `json:"errorCode"`
}

type FormulaTextPosition struct {
	Line      int64 `json:"line"`
	Character int64 `json:"character"`
}

type FormulaTextRange struct {
	Start FormulaTextPosition `json:"start"`
	End   FormulaTextPosition `json:"end"`
}

type FormulaAuthorToken struct {
	Range           FormulaTextRange `json:"range"`
	Kind            string           `json:"kind"`
	FieldId         string           `json:"fieldId"`
	RelationFieldId *string          `json:"relationFieldId"`
	TargetFieldId   *string          `json:"targetFieldId"`
}

type FormulaAuthorDocument struct {
	DisplaySource    string               `json:"displaySource"`
	Tokens           []FormulaAuthorToken `json:"tokens"`
	DocumentRevision int64                `json:"documentRevision"`
}

type ComputedCellEnvelope struct {
	State               string  `json:"state"`
	Value               any     `json:"value"`
	DefinitionVersion   int64   `json:"definitionVersion"`
	SourceDataRevision  int64   `json:"sourceDataRevision"`
	DependencyWatermark int64   `json:"dependencyWatermark"`
	Diagnostic          *string `json:"diagnostic"`
}

type SchemaAuditEvent struct {
	EventId        string  `json:"eventId"`
	WorkspaceId    string  `json:"workspaceId"`
	TableId        string  `json:"tableId"`
	FieldId        *string `json:"fieldId"`
	Operation      string  `json:"operation"`
	SchemaRevision int64   `json:"schemaRevision"`
	OccurredAt     string  `json:"occurredAt"`
	ActorId        string  `json:"actorId"`
}
