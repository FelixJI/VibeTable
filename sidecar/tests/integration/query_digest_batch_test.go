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
}
