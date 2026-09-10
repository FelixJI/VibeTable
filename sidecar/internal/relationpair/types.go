// Package relationpair inspects persisted pair integrity without changing schema or records.
package relationpair

type Endpoint struct {
	TableID        string `json:"tableId"`
	FieldID        string `json:"fieldId"`
	SchemaRevision string `json:"schemaRevision"`
	DataRevision   int64  `json:"dataRevision"`
}

// Cursor continues a scan against the same two revisions. It is progress, not a
// repair authorization or proof of integrity. Consumers retain each page's findings.
type Cursor struct {
	PairID     string      `json:"pairId"`
	Endpoints  [2]Endpoint `json:"endpoints"`
	After      [2]string   `json:"after"`
	Done       [2]bool     `json:"done"`
	Incomplete bool        `json:"incomplete"`
}

type Request struct {
	TableID string  `json:"tableId"`
	FieldID string  `json:"fieldId"`
	Limit   int     `json:"limit"`
	Cursor  *Cursor `json:"cursor,omitempty"`
}

type Finding struct {
	Code     string `json:"code"`
	Endpoint int    `json:"endpoint"`
	RecordID string `json:"recordId,omitempty"`
	TargetID string `json:"targetId,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type Report struct {
	PairID    string      `json:"pairId"`
	Endpoints [2]Endpoint `json:"endpoints"`
	// Counts and Samples describe this page; Complete describes scan coverage,
	// never health. A completed scan may contain findings from any prior page.
	Counts           map[string]int `json:"counts"`
	Samples          []Finding      `json:"samples"`
	SamplesTruncated bool           `json:"samplesTruncated"`
	RowsScanned      [2]int         `json:"rowsScanned"`
	PageComplete     bool           `json:"pageComplete"`
	Finished         bool           `json:"finished"`
	Complete         bool           `json:"complete"`
	Next             *Cursor        `json:"next,omitempty"`
}
