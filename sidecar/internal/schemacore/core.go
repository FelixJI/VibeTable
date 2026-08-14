package schemacore

import (
	"context"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// Interface is the only product-facing schema mutation boundary. Implementors
// own schema reads, frozen planning, and application; callers never submit a
// complete provider-shaped table definition.
type Interface interface {
	Describe(ctx context.Context, tableID string) (v2.SchemaSnapshot, error)
	Plan(ctx context.Context, intent v2.FieldChangeIntent) (v2.FieldChangePlan, error)
	Apply(ctx context.Context, request v2.ApplyRequest) (v2.ApplyReceipt, error)
}

type SnapshotSource interface {
	Revisions(ctx context.Context, tableID string) (fieldchange.Revisions, error)
	TableDisplayName(ctx context.Context, tableID string) (string, error)
	Fields(ctx context.Context, tableID string, includeRetired bool) ([]v2.FieldDefinition, error)
}

type Planner interface {
	Plan(ctx context.Context, intent v2.FieldChangeIntent) (v2.FieldChangePlan, error)
}

type Executor interface {
	Apply(ctx context.Context, request v2.ApplyRequest) (v2.ApplyReceipt, error)
}

type Core struct {
	source   SnapshotSource
	planner  Planner
	executor Executor
}

func New(source SnapshotSource, planner Planner, executor Executor) (*Core, error) {
	if source == nil || planner == nil || executor == nil {
		return nil, errors.New("schemacore dependencies are required")
	}
	return &Core{source: source, planner: planner, executor: executor}, nil
}

func (core *Core) Describe(
	ctx context.Context,
	tableID string,
) (v2.SchemaSnapshot, error) {
	revisions, err := core.source.Revisions(ctx, tableID)
	if err != nil {
		return v2.SchemaSnapshot{}, err
	}
	displayName, err := core.source.TableDisplayName(ctx, tableID)
	if err != nil {
		return v2.SchemaSnapshot{}, err
	}
	fields, err := core.source.Fields(ctx, tableID, false)
	if err != nil {
		return v2.SchemaSnapshot{}, err
	}
	capabilities := make([]v2.Capability, 0, len(v2.LogicalTypes))
	for _, logicalType := range v2.LogicalTypes {
		// LogicalTypes is the closed, validated capability registry owned by v2;
		// CapabilityFor can fail only for values outside that registry.
		capability, _ := v2.CapabilityFor(logicalType)
		capabilities = append(capabilities, capability)
	}
	return v2.SchemaSnapshot{
		Contract:       v2.Contract,
		TableID:        tableID,
		DisplayName:    displayName,
		SchemaRevision: revisions.Schema,
		DataRevision:   revisions.Data,
		Fields:         fields,
		Capabilities:   capabilities,
	}, nil
}

func (core *Core) Plan(
	ctx context.Context,
	intent v2.FieldChangeIntent,
) (v2.FieldChangePlan, error) {
	return core.planner.Plan(ctx, intent)
}

func (core *Core) Apply(
	ctx context.Context,
	request v2.ApplyRequest,
) (v2.ApplyReceipt, error) {
	return core.executor.Apply(ctx, request)
}
