package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestLookupQueryPageSharesTargetReads(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(t, ctx, app, "Targets", "batch_targets")
	name := createV2IntegrationField(t, ctx, app, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "batch_target_name")
	source := createV2IntegrationTable(t, ctx, app, "Sources", "batch_sources")
	title := createV2IntegrationField(t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"), "batch_source_title")
	link := createV2IntegrationRelation(t, ctx, app, source.TableID, title.FieldID,
		target.TableID, name.FieldID, "Target", "Sources", "one", "batch_link")
	var lookupNames []string
	for index := range 4 {
		draft := fieldDraftForIntegration(t, v2.LogicalLookup, fmt.Sprintf("Name %d", index))
		draft.Lookup = &v2.LookupSpec{
			Path: []v2.LookupPathStep{{RelationFieldID: link.FieldID}}, TargetFieldID: name.FieldID,
		}
		field := createV2IntegrationField(t, ctx, app, source.TableID, draft, fmt.Sprintf("batch_lookup_%d", index))
		lookupNames = append(lookupNames, field.Definition.Identity.PhysicalName)
	}
	targetCollection, err := app.FindCollectionByNameOrId(target.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	sourceCollection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	var targetIDs []string
	targetLabels := map[string]string{}
	for index := range 16 {
		record := core.NewRecord(targetCollection)
		record.Set(name.Definition.Identity.PhysicalName, fmt.Sprintf("Target %d", index))
		record.Set(name.Definition.Value.Presence.PhysicalName, true)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		targetIDs = append(targetIDs, record.Id)
		targetLabels[record.Id] = fmt.Sprintf("Target %d", index)
	}
	expectedTargets := map[string]string{}
	for index := range 300 {
		record := core.NewRecord(sourceCollection)
		record.Set(title.Definition.Identity.PhysicalName, fmt.Sprintf("Source %d", index))
		record.Set(link.Definition.Identity.PhysicalName, targetIDs[index%len(targetIDs)])
		record.Set(link.Definition.Value.Presence.PhysicalName, true)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		expectedTargets[record.Id] = targetIDs[index%len(targetIDs)]
	}
	definition, err := schemaapi.New(app).Describe(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(app, query.NewPort(app, querySource), nil)
	var targetReads atomic.Int64
	var sourceReads atomic.Int64
	var cancelOnSourceRead atomic.Bool
	cancelledContext, cancel := context.WithCancel(ctx)
	defer cancel()
	database := app.ConcurrentDB().(*dbx.DB)
	previous := database.QueryLogFunc
	database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
		if strings.Contains(statement, "FROM `"+target.PhysicalName+"`") ||
			strings.Contains(statement, "FROM \""+target.PhysicalName+"\"") {
			targetReads.Add(1)
		}
		if strings.Contains(statement, "FROM `"+source.PhysicalName+"`") ||
			strings.Contains(statement, "FROM \""+source.PhysicalName+"\"") {
			sourceReads.Add(1)
			if cancelOnSourceRead.Load() {
				cancel()
			}
		}
		if previous != nil {
			previous(ctx, elapsed, statement, rows, err)
		}
	}
	defer func() { database.QueryLogFunc = previous }()
	for _, limit := range []int{4, 64, 300} {
		t.Run(fmt.Sprintf("rows_%d", limit), func(t *testing.T) {
			targetReads.Store(0)
			sourceReads.Store(0)
			result, err := service.QueryLookups(ctx, relation.LookupQueryRequest{
				TableID: source.TableID, SchemaRevision: definition.Snapshot.SchemaRevision,
				Query: query.TableQuery{Limit: limit},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Rows) != limit {
				t.Fatalf("rows = %d, want %d", len(result.Rows), limit)
			}
			for _, row := range result.Rows {
				expectedTarget := expectedTargets[row["id"].(string)]
				labels, ok := row[query.RelationLabelsField].(map[string]map[string]string)
				linkName := link.Definition.Identity.PhysicalName
				if !ok || labels[linkName][expectedTarget] != targetLabels[expectedTarget] || row[linkName] != expectedTarget {
					t.Fatalf("lookup projection lost relation label or raw ID: %#v", row)
				}
				for _, physicalName := range lookupNames {
					cell, ok := row[physicalName].(lookup.CellValue)
					if !ok || cell.State != "ok" || len(cell.Provenance) != 1 ||
						cell.Value != targetLabels[expectedTarget] ||
						cell.Provenance[0].Value != cell.Value ||
						cell.Provenance[0].ItemID != expectedTarget ||
						cell.Provenance[0].FieldID != name.FieldID {
						t.Fatalf("lookup cell = %#v", row[physicalName])
					}
				}
			}
			t.Logf("rows=%d fields=4 target SELECTs=%d source SELECTs=%d", limit, targetReads.Load(), sourceReads.Load())
			if got := targetReads.Load(); got < 1 || got > 2 {
				t.Fatalf("target SELECTs = %d: page/path sharing requires 1–2, independent of rows × fields", got)
			}
			if got := sourceReads.Load(); got < 1 || got > 2 {
				t.Fatalf("source SELECTs = %d: source page must be loaded in a batch", got)
			}
		})
	}
	t.Run("cancel_after_source_read", func(t *testing.T) {
		cancelOnSourceRead.Store(true)
		_, err := service.QueryLookups(cancelledContext, relation.LookupQueryRequest{
			TableID: source.TableID, SchemaRevision: definition.Snapshot.SchemaRevision,
			Query: query.TableQuery{Limit: 300},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled source batch error = %v", err)
		}
	})
}
