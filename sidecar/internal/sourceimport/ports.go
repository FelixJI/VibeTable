package sourceimport

import (
	"context"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// Authority adapts the already instantiated schema domain and mutation kernel.
// Commit projections run in their transaction; no direct business row/schema
// writes, second planner, or nested whole-job transaction are permitted.
type Authority interface {
	Describe(context.Context, string) (v2.SchemaSnapshot, error)
	CreateTable(context.Context, v2.TableCreateIntent, func(core.App, v2.TableCreateReceipt) error) (v2.TableCreateReceipt, error)
	ChangeField(context.Context, v2.FieldChangeIntent, string, func(core.App, v2.ApplyReceipt) error) (v2.ApplyReceipt, error)
	Mutate(context.Context, mutation.Request, func(core.App, mutation.Receipt) error) (mutation.Receipt, error)
}

// AttachmentSource is the controlled Host staging port. It supplies an upload
// handle for real local bytes owned by this job, never a remote URL. The Host
// authenticates/fences every request, binds stable batch keys, and cleans only
// its own staging resources. Providers cannot mutate PocketBase themselves.
type AttachmentSource interface {
	Stage(context.Context, string, string, string, Attachment) (string, error)
	Cleanup(context.Context, string) error
}

type Mapping struct {
	Source     Key    `json:"source"`
	Kind       string `json:"kind"`
	LocalID    string `json:"localId"`
	TableID    string `json:"tableId"`
	Name       string `json:"name,omitempty"`
	Collection string `json:"collection,omitempty"`
}

type Batch struct {
	JobID            string            `json:"jobId"`
	ID               string            `json:"batchId"`
	Stage            string            `json:"stage"`
	TableID          string            `json:"tableId"`
	Mappings         []Mapping         `json:"mappings"`
	Created          int               `json:"created"`
	RelationWrites   int               `json:"relationWrites"`
	AttachmentWrites int               `json:"attachmentWrites"`
	SchemaRevisions  map[string]string `json:"schemaRevisions"`
}

type Target struct {
	SourceTableID string `json:"sourceTableId"`
	TableID       string `json:"tableId"`
	Name          string `json:"name"`
	Collection    string `json:"collection"`
}

type Result struct {
	Contract       string         `json:"contract"`
	JobID          string         `json:"jobId"`
	Provider       string         `json:"provider"`
	ContainerID    string         `json:"containerId"`
	SourceName     string         `json:"sourceName"`
	State          string         `json:"state"`
	Stage          string         `json:"stage"`
	Created        int            `json:"created"`
	Total          int            `json:"total"`
	NotSubmitted   int            `json:"notSubmitted"`
	UnknownRecords int            `json:"unknownRecords"`
	UnknownBatch   string         `json:"unknownBatch,omitempty"`
	Targets        []Target       `json:"targets"`
	Batches        []Batch        `json:"batches"`
	Diagnostics    []Diagnostic   `json:"diagnostics"`
	StartedAt      string         `json:"startedAt"`
	FinishedAt     string         `json:"finishedAt,omitempty"`
	SessionEpoch   uint64         `json:"sessionEpoch"`
	ReadWindow     ReadWindow     `json:"readWindow"`
	Fields         []FieldSummary `json:"fields"`
}

type FieldSummary struct {
	Source     Key    `json:"source"`
	Kind       string `json:"kind"`
	Policy     string `json:"policy"`
	Definition string `json:"definition"`
}

// Journal stores facts, not restartable tokens. Confirmed mappings and batches
// are committed in the same authority transaction as their business effects.
type Journal interface {
	Start(context.Context, Result) error
	Read(context.Context, string) (Result, error)
	FindBatch(context.Context, string, string) (Batch, bool, error)
	Prepare(context.Context, string, string, string, int) error
	Commit(core.App, Batch) error
	Finish(context.Context, Result) error
}
