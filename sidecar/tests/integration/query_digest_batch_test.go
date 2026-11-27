package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
)

func TestQueryDigestReadsRecordsInBoundedBatches(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	collection := core.NewBaseCollection("query_digest_rows")
	collection.Fields.Add(&core.NumberField{Name: "amount"}, &core.TextField{Name: "private_value"})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	if err := app.RunInTransaction(func(tx core.App) error {
		for i := range 500 {
			record := core.NewRecord(collection)
			record.Id = fmt.Sprintf("%015d", i)
			record.Set("amount", i)
			record.Set("private_value", "private")
			if err := tx.Save(record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source := &staticQuerySource{descriptor: query.TableDescriptor{
		DatabaseID: "local", TableID: "digest", PhysicalName: collection.Name,
		PrimaryKey: "id", SchemaRevision: "schema-1", DataRevision: 1,
		Fields: map[string]query.FieldDescriptor{
			"id":     {PhysicalName: "id", Type: query.FieldTypeText},
			"amount": {PhysicalName: "amount", Type: query.FieldTypeNumber},
		},
		DigestFields: []string{"amount", "private_value"},
		DigestProjector: func(record *core.Record) map[string]any {
			original := record.Original()
			if original.GetString("private_value") != "private" {
				t.Fatal("digest projector lost the original complete record")
			}
			return map[string]any{"amount": original.GetFloat("amount"), "private_value": original.GetString("private_value")}
		},
	}}
	port := query.NewPort(app, source)
	var reads atomic.Int64
	var cancelDuringRead context.CancelFunc
	for _, database := range []*dbx.DB{app.ConcurrentDB().(*dbx.DB), app.NonconcurrentDB().(*dbx.DB)} {
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if strings.Contains(statement, "SELECT `query_digest_rows`.*") {
				reads.Add(1)
				if cancelDuringRead != nil {
					cancelDuringRead()
				}
			}
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
		}
		defer func() { database.QueryLogFunc = previous }()
	}
	for _, size := range []int{0, 4, 100, 500} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			reads.Store(0)
			input := query.TableQuery{Limit: max(1, size), Sorts: []query.SortCondition{{Field: "amount", Direction: query.SortDescending}}}
			if size == 0 {
				input.Filters = []query.FilterExpression{{Field: "amount", Operator: query.OperatorGreater, Value: 500}}
			}
			page, err := port.QueryPage(context.Background(), "digest", input)
			if err != nil || len(page.Rows) != size {
				t.Fatalf("page rows=%d want=%d error=%v", len(page.Rows), size, err)
			}
			for index, row := range page.Rows {
				amount := float64(499 - index)
				want, err := productrow.Digest(map[string]any{"amount": amount, "private_value": "private"})
				if err != nil || row[productrow.DigestField] != want || row["id"] != fmt.Sprintf("%015d", 499-index) {
					t.Fatalf("row %d lost order/digest: %#v error=%v", index, row, err)
				}
				if _, leaked := row["private_value"]; leaked {
					t.Fatal("digest-only value leaked into the query")
				}
			}
			if got, want := reads.Load(), int64((size+255)/256); got != want {
				t.Fatalf("digest record reads=%d want=%d for %d rows", got, want, size)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelDuringRead = cancel
	reads.Store(0)
	page, err := port.QueryPage(ctx, "digest", query.TableQuery{Limit: 500})
	if !errors.Is(err, context.Canceled) || len(page.Rows) != 0 || reads.Load() != 1 {
		t.Fatalf("cancelled digest read returned page=%+v error=%v reads=%d", page, err, reads.Load())
	}

	// Cancelling while digests are being projected must abort the page without
	// partial results: the projector itself cancels after the first row.
	t.Run("cancel-during-projection", func(t *testing.T) {
		cancelDuringRead = nil
		projectionCtx, projectionCancel := context.WithCancel(context.Background())
		defer projectionCancel()
		projected := 0
		originalProjector := source.descriptor.DigestProjector
		source.descriptor.DigestProjector = func(record *core.Record) map[string]any {
			projected++
			projectionCancel()
			return originalProjector(record)
		}
		page, err := port.QueryPage(projectionCtx, "digest", query.TableQuery{Limit: 4})
		if !errors.Is(err, context.Canceled) || len(page.Rows) != 0 || projected != 1 {
			t.Fatalf("cancelled projection returned page=%+v error=%v projected=%d",
				page, err, projected)
		}
	})
}

// AttachRowDigests is the row-guard contract shared by query pages and
// snapshot corpora. Each failure below is a realistic one (a row deleted
// between the page read and the digest read, a projection that cannot be
// encoded, a cancelled caller, a collection that went away) and must propagate
// its error while leaving the caller's rows without a single digest: a page is
// guarded completely or not at all.
func TestAttachRowDigestsFailuresNeverLeavePartialGuards(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	collection := core.NewBaseCollection("query_digest_rows")
	collection.Fields.Add(&core.NumberField{Name: "amount"}, &core.TextField{Name: "private_value"})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	ids := []string{"", "", ""}
	var ghost *core.Record
	if err := app.RunInTransaction(func(tx core.App) error {
		for i := range ids {
			record := core.NewRecord(collection)
			record.Set("amount", i)
			record.Set("private_value", "private")
			if err := tx.Save(record); err != nil {
				return err
			}
			ids[i] = record.Id
		}
		ghost = core.NewRecord(collection)
		ghost.Set("amount", 99)
		ghost.Set("private_value", "private")
		return tx.Save(ghost)
	}); err != nil {
		t.Fatal(err)
	}
	project := func(record *core.Record) map[string]any {
		original := record.Original()
		return map[string]any{"amount": original.GetFloat("amount"), "private_value": original.GetString("private_value")}
	}
	descriptor := query.TableDescriptor{
		DatabaseID: "local", TableID: "digest", PhysicalName: collection.Name,
		PrimaryKey: "id", SchemaRevision: "schema-1", DataRevision: 1,
		Fields: map[string]query.FieldDescriptor{
			"id":     {PhysicalName: "id", Type: query.FieldTypeText},
			"amount": {PhysicalName: "amount", Type: query.FieldTypeNumber},
		},
		DigestFields:    []string{"amount", "private_value"},
		DigestProjector: project,
	}
	page := func(id ...string) []map[string]any {
		rows := []map[string]any{}
		for _, value := range id {
			rows = append(rows, map[string]any{"id": value, "amount": float64(len(rows))})
		}
		return rows
	}
	// failing attaches the digests, requires an error and checks that no row
	// of the page was guarded anyway.
	failing := func(t *testing.T, ctx context.Context, descriptor query.TableDescriptor, rows []map[string]any) error {
		t.Helper()
		err := query.AttachRowDigests(ctx, app, descriptor, rows)
		if err == nil {
			t.Fatal("failure was swallowed")
		}
		for index, row := range rows {
			if _, partial := row[productrow.DigestField]; partial {
				t.Fatalf("row %d guarded after a failed attach: %#v", index, row)
			}
		}
		return err
	}
	// The success contract first: every row guarded with the projected digest.
	rows := page(ids...)
	if err := query.AttachRowDigests(context.Background(), app, descriptor, rows); err != nil {
		t.Fatal(err)
	}
	for index, row := range rows {
		want, err := productrow.Digest(map[string]any{"amount": float64(index), "private_value": "private"})
		if err != nil || row[productrow.DigestField] != want {
			t.Fatalf("row %d digest=%v error=%v", index, row[productrow.DigestField], err)
		}
		if _, leaked := row["private_value"]; leaked {
			t.Fatal("digest-only value leaked into the page")
		}
	}
	t.Run("cancelled before reading", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := failing(t, ctx, descriptor, page(ids...)); !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("row without primary key", func(t *testing.T) {
		if err := failing(t, context.Background(), descriptor, page(ids[0], "", ids[2])); err.Error() != "query row omitted its primary key" {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("record deleted after the page read", func(t *testing.T) {
		if err := app.Delete(ghost); err != nil {
			t.Fatal(err)
		}
		if err := failing(t, context.Background(), descriptor, page(ids[0], ghost.Id, ids[2])); err.Error() != "query row record could not be read" {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("projection cannot be encoded", func(t *testing.T) {
		broken := descriptor
		broken.DigestProjector = func(record *core.Record) map[string]any {
			row := project(record)
			if row["amount"] == float64(1) {
				row["amount"] = math.NaN()
			}
			return row
		}
		var unsupported *json.UnsupportedValueError
		if err := failing(t, context.Background(), broken, page(ids...)); !errors.As(err, &unsupported) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("cancelled during projection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		projected := 0
		slow := descriptor
		slow.DigestProjector = func(record *core.Record) map[string]any {
			projected++
			cancel()
			return project(record)
		}
		// Not even the first row, whose digest was already computed, is
		// published: the page is guarded completely or not at all.
		if err := failing(t, ctx, slow, page(ids...)); !errors.Is(err, context.Canceled) || projected != 1 {
			t.Fatalf("error=%v projected=%d", err, projected)
		}
	})
	t.Run("collection went away", func(t *testing.T) {
		missing := descriptor
		missing.PhysicalName = "query_digest_missing"
		if err := failing(t, context.Background(), missing, page(ids...)); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("records unreadable", func(t *testing.T) {
		// The collection stays cached; only its records table is gone, which
		// is exactly the state a concurrent schema teardown leaves behind.
		if _, err := app.DB().NewQuery("DROP TABLE `query_digest_rows`").Execute(); err != nil {
			t.Fatal(err)
		}
		if err := failing(t, context.Background(), descriptor, page(ids...)); err == nil || !strings.Contains(err.Error(), "no such table") {
			t.Fatalf("error=%v", err)
		}
	})
}

// A page may list the same record more than once (a join fan-out). Every
// occurrence receives the digest of its record, including the ones that fall
// into a later batch than the first occurrence.
func TestAttachRowDigestsGuardsRepeatedIDsAcrossBatches(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	collection := core.NewBaseCollection("query_digest_repeats")
	collection.Fields.Add(&core.NumberField{Name: "amount"})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 150)
	if err := app.RunInTransaction(func(tx core.App) error {
		for i := range 150 {
			record := core.NewRecord(collection)
			record.Set("amount", i)
			if err := tx.Save(record); err != nil {
				return err
			}
			ids = append(ids, record.Id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	for _, database := range []*dbx.DB{app.ConcurrentDB().(*dbx.DB), app.NonconcurrentDB().(*dbx.DB)} {
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if strings.Contains(statement, "SELECT `query_digest_repeats`.*") {
				reads.Add(1)
			}
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
		}
		defer func() { database.QueryLogFunc = previous }()
	}
	descriptor := query.TableDescriptor{
		DatabaseID: "local", TableID: "digest", PhysicalName: collection.Name,
		PrimaryKey: "id", SchemaRevision: "schema-1", DataRevision: 1,
		Fields:       map[string]query.FieldDescriptor{"id": {PhysicalName: "id", Type: query.FieldTypeText}},
		DigestFields: []string{"amount"},
		DigestProjector: func(record *core.Record) map[string]any {
			return map[string]any{"amount": record.Original().GetFloat("amount")}
		},
	}
	// 300 rows: the first 150 distinct records, then the same 150 again in
	// reverse order, so the second batch repeats ids of the first.
	page := make([]map[string]any, 0, 300)
	for step := 0; step < 300; step++ {
		index := step
		if step >= 150 {
			index = 299 - step
		}
		page = append(page, map[string]any{"id": ids[index], "amount": float64(index)})
	}
	if err := query.AttachRowDigests(context.Background(), app, descriptor, page); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 2 {
		t.Fatalf("digest record reads=%d want=2 for 300 rows", reads.Load())
	}
	for position, row := range page {
		index := position
		if position >= 150 {
			index = 299 - position
		}
		want, err := productrow.Digest(map[string]any{"amount": float64(index)})
		if err != nil || row[productrow.DigestField] != want {
			t.Fatalf("row %d digest=%v want=%v error=%v", position, row[productrow.DigestField], want, err)
		}
	}
}
