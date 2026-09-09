package formula

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	_ "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/hook"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestAppCompilerInvalidatesOnlyCommittedSchemaChanges(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	collection := core.NewBaseCollection("vibetable_tables")
	collection.Fields.Add(&core.TextField{Name: "table_id"},
		&core.NumberField{Name: "schema_revision", OnlyInt: true},
		&core.NumberField{Name: "data_revision", OnlyInt: true})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	definition := formulaTable(formulaField("value_id", "value", integerType, "1"))
	definition.Snapshot.SchemaRevision = v2.FormatSchemaRevision(1)
	record := core.NewRecord(collection)
	record.Set("table_id", definition.Snapshot.TableID)
	record.Set("schema_revision", 1)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	compiler := NewAppCompiler(app)
	initial, err := compiler.CompileExecutionTable(definition)
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback schema change")
	if err := app.RunInTransaction(func(tx core.App) error {
		if CompilerFor(tx) != compiler {
			t.Error("transaction clone does not share application compiler")
		}
		pending, err := tx.FindRecordById(collection, record.Id)
		if err != nil {
			return err
		}
		pending.Set("schema_revision", 2)
		if err := tx.Save(pending); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rollback: %v", err)
	}
	if plan, err := compiler.CompileExecutionTable(definition); err != nil || plan != initial {
		t.Fatalf("rollback invalidated cached plan: %p/%p, %v", initial, plan, err)
	}
	record.Set("data_revision", 1)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	if plan, err := compiler.CompileExecutionTable(definition); err != nil || plan != initial {
		t.Fatalf("data-only change invalidated cached plan: %p/%p, %v", initial, plan, err)
	}
	if err := app.RunInTransaction(func(tx core.App) error {
		pending, err := tx.FindRecordById(collection, record.Id)
		if err != nil {
			return err
		}
		pending.Set("schema_revision", 2)
		return tx.Save(pending)
	}); err != nil {
		t.Fatal(err)
	}
	if len(compiler.cache.entries) != 0 {
		t.Fatal("schema commit did not proactively invalidate cache")
	}
	newer := definition
	newer.Snapshot.SchemaRevision = v2.FormatSchemaRevision(2)
	current, err := compiler.CompileExecutionTable(newer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compiler.CompileExecutionTable(definition); err != nil {
		t.Fatal(err)
	}
	if plan, err := compiler.CompileExecutionTable(newer); err != nil || plan != current || len(compiler.cache.entries) != 1 {
		t.Fatalf("late old request disturbed current plan: %p/%p, %v, entries=%d", current, plan, err, len(compiler.cache.entries))
	}
	// After-success callbacks run after the SQL writer is released. An older
	// callback must not invalidate a newer transaction's already-cached plan.
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseOld := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseOld)
	app.OnRecordAfterUpdateSuccess("vibetable_tables").Bind(&hook.Handler[*core.RecordEvent]{
		Priority: -10,
		Func: func(event *core.RecordEvent) error {
			if event.Record.GetInt("schema_revision") == 3 {
				close(entered)
				<-release
			}
			return event.Next()
		},
	})
	saveRevision := func(revision int) error {
		return app.RunInTransaction(func(tx core.App) error {
			pending, err := tx.FindRecordById(collection, record.Id)
			if err != nil {
				return err
			}
			pending.Set("schema_revision", revision)
			return tx.Save(pending)
		})
	}
	done := make(chan error, 1)
	go func() { done <- saveRevision(3) }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("old transaction returned before callback barrier: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("old transaction did not reach callback barrier")
	}
	if err := saveRevision(4); err != nil {
		t.Fatal(err)
	}
	newer.Snapshot.SchemaRevision = v2.FormatSchemaRevision(4)
	current, err = compiler.CompileExecutionTable(newer)
	if err != nil {
		t.Fatal(err)
	}
	releaseOld()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if plan, err := compiler.CompileExecutionTable(newer); err != nil || plan != current {
		t.Fatalf("late commit callback evicted new plan: %p/%p, %v", current, plan, err)
	}
	t.Run("uncommitted table compiles without cache admission", func(t *testing.T) {
		pendingDefinition := definition
		pendingDefinition.Snapshot.TableID = "pending_table"
		var pendingPlan *Plan
		if err := app.RunInTransaction(func(tx core.App) error {
			pending := core.NewRecord(collection)
			pending.Set("table_id", pendingDefinition.Snapshot.TableID)
			pending.Set("schema_revision", 1)
			if err := tx.Save(pending); err != nil {
				return err
			}
			var compileErr *Error
			pendingPlan, compileErr = CompilerFor(tx).CompileExecutionTable(pendingDefinition)
			if compileErr != nil {
				return compileErr
			}
			if len(compiler.cache.entries) != 1 {
				t.Error("uncommitted definition entered shared cache")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		committedPlan, err := compiler.CompileExecutionTable(pendingDefinition)
		if err != nil || committedPlan == pendingPlan || len(compiler.cache.entries) != 2 {
			t.Fatalf("committed table did not get its own cached plan: %p/%p, %v", pendingPlan, committedPlan, err)
		}
		compiler.cache.invalidateTable(pendingDefinition.Snapshot.TableID)
	})
	t.Run("rolled back new table leaves no shared plan", func(t *testing.T) {
		pendingDefinition := definition
		pendingDefinition.Snapshot.TableID = "rolled_back_table"
		if err := app.RunInTransaction(func(tx core.App) error {
			pending := core.NewRecord(collection)
			pending.Set("table_id", pendingDefinition.Snapshot.TableID)
			pending.Set("schema_revision", 1)
			if err := tx.Save(pending); err != nil {
				return err
			}
			if _, err := CompilerFor(tx).CompileExecutionTable(pendingDefinition); err != nil {
				return err
			}
			return rollback
		}); !errors.Is(err, rollback) {
			t.Fatalf("new table rollback: %v", err)
		}
		if len(compiler.cache.entries) != 1 {
			t.Fatal("rolled back definition left a shared plan")
		}
		if _, err := app.FindFirstRecordByData(collection, "table_id", pendingDefinition.Snapshot.TableID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("rolled back table remained in authority: %v", err)
		}
	})
	if err := app.Delete(record); err != nil {
		t.Fatal(err)
	}
	if len(compiler.cache.entries) != 0 {
		t.Fatal("table deletion did not invalidate cache")
	}
}

func TestAppCompilerRefreshFailureDoesNotChangeCommittedSave(t *testing.T) {
	for _, transactional := range []bool{false, true} {
		name := "direct"
		if transactional {
			name = "transaction"
		}
		t.Run(name, func(t *testing.T) {
			app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
			t.Cleanup(func() {
				if err := app.ResetBootstrapState(); err != nil {
					t.Error(err)
				}
			})
			if err := app.Bootstrap(); err != nil {
				t.Fatal(err)
			}
			collection := core.NewBaseCollection("vibetable_tables")
			collection.Fields.Add(&core.TextField{Name: "table_id"},
				&core.NumberField{Name: "schema_revision", OnlyInt: true})
			if err := app.Save(collection); err != nil {
				t.Fatal(err)
			}
			definition := formulaTable(formulaField("value_id", "value", integerType, "1"))
			definition.Snapshot.SchemaRevision = v2.FormatSchemaRevision(1)
			record := core.NewRecord(collection)
			record.Set("table_id", definition.Snapshot.TableID)
			record.Set("schema_revision", 1)
			if err := app.Save(record); err != nil {
				t.Fatal(err)
			}
			compiler := NewAppCompiler(app)
			if _, err := compiler.CompileExecutionTable(definition); err != nil {
				t.Fatal(err)
			}
			readFailure := errors.New("injected cache authority read failure")
			failedRead := func(string) (string, error) { return "", readFailure }
			compiler.cache.currentRevision = failedRead
			continued := false
			var downstreamErr error
			app.OnRecordAfterUpdateSuccess("vibetable_tables").Bind(&hook.Handler[*core.RecordEvent]{
				Priority: 10,
				Func: func(event *core.RecordEvent) error {
					continued = true
					if downstreamErr != nil {
						return downstreamErr
					}
					return event.Next()
				},
			})
			record.Set("schema_revision", 2)
			save := func(target core.App) error { return target.Save(record) }
			var saveErr error
			if transactional {
				saveErr = app.RunInTransaction(save)
			} else {
				saveErr = save(app)
			}
			persisted, readErr := app.FindRecordById(collection, record.Id)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if persisted.GetInt("schema_revision") != 2 {
				t.Fatal("authoritative update did not commit")
			}
			if saveErr != nil {
				t.Errorf("already committed update returned cache failure: %v", saveErr)
			}
			if !continued {
				t.Error("cache read failure stopped the after-success hook chain")
			}
			if len(compiler.cache.entries) != 0 {
				t.Error("cache retained plans after authority refresh failed")
			}
			definition.Snapshot.SchemaRevision = v2.FormatSchemaRevision(2)
			compiler.cache.currentRevision = func(string) (string, error) {
				return definition.Snapshot.SchemaRevision, nil
			}
			if _, err := compiler.CompileExecutionTable(definition); err != nil {
				t.Fatal(err)
			}
			compiler.cache.currentRevision = failedRead
			downstreamErr = errors.New("downstream after-success failure")
			record.Set("schema_revision", 3)
			if transactional {
				saveErr = app.RunInTransaction(save)
			} else {
				saveErr = save(app)
			}
			if !errors.Is(saveErr, downstreamErr) || errors.Is(saveErr, readFailure) {
				t.Fatalf("downstream failure was swallowed or replaced by cache failure: %v", saveErr)
			}
		})
	}
}
