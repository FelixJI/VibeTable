package pluginmodel

import (
	"container/list"
	"errors"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

type cachedReadApp struct {
	*progressHistoryApp
	onPage func(offset int) error
}

func (a *cachedReadApp) FindRecordsByFilter(collection any, filter, order string, limit, offset int, params ...dbx.Params) ([]*core.Record, error) {
	if a.onPage != nil {
		if err := a.onPage(offset); err != nil {
			return nil, err
		}
	}
	return a.progressHistoryApp.FindRecordsByFilter(collection, filter, order, limit, offset, params...)
}

func cachedHistoryFixture(payload string, n int) *cachedReadApp {
	collection := core.NewBaseCollection("vibetable_plugin_records")
	app := &cachedReadApp{progressHistoryApp: &progressHistoryApp{collection: collection}}
	for i := range n {
		row := core.NewRecord(collection)
		row.SetRaw("payload_json", types.JSONRaw(payload))
		row.Set("seq", i+1)
		app.records = append(app.records, row)
	}
	return app
}

func TestOversizeVerifiedHistoryPreservesOtherScopes(t *testing.T) {
	cache := &progressCache{entries: make(map[historyScope]*list.Element), budget: 512}
	const raw = `{"details":{"contract":"vibetable.plugin-model.v1","status":"completed"}}`
	small, large := cachedHistoryFixture(raw, 1), cachedHistoryFixture(raw, 8)
	read := func(app *cachedReadApp, scope string) {
		t.Helper()
		got, err := cache.completedProgress(t.Context(), app, "project", scope)
		if err != nil || len(got) != len(app.records) {
			t.Fatalf("%s: %d completions, %v", scope, len(got), err)
		}
		// Reused or decoded, every completion carries its record's audit sequence.
		for i, progress := range got {
			if progress.Seq != int64(i+1) {
				t.Fatalf("%s: completion %d has sequence %d", scope, i, progress.Seq)
			}
		}
	}
	read(small, "small")
	read(large, "large")
	before := cache.stats.Decoded
	read(small, "small")
	if cache.stats.Decoded != before || cache.stats.Evicted != 0 || len(cache.entries) != 1 {
		t.Fatalf("oversize read displaced hot scope: %+v", cache.stats)
	}
}

func TestInvalidProgressStopsBeforeLaterPageError(t *testing.T) {
	cache := &progressCache{entries: make(map[historyScope]*list.Element), budget: 512}
	app := cachedHistoryFixture(`{"details":{"contract":"invalid"}}`, 256)
	app.onPage = func(offset int) error {
		if offset > 0 {
			t.Error("requested next page after invalid progress")
			return errors.New("next page failed")
		}
		return nil
	}
	_, err := cache.completedProgress(t.Context(), app, "project", "plugin")
	if err == nil || err.Error() != "plugin model progress contract is invalid" {
		t.Fatalf("first error: %v", err)
	}
}

func TestConcurrentVerifiedHistoriesUseOwnReadPoint(t *testing.T) {
	cache := &progressCache{entries: make(map[historyScope]*list.Element), budget: 2048}
	old := cachedHistoryFixture(`{"details":{"contract":"vibetable.plugin-model.v1","status":"completed","requestId":"old"}}`, 1)
	newer := cachedHistoryFixture(`{"details":{"contract":"vibetable.plugin-model.v1","status":"completed","requestId":"new"}}`, 1)
	entered, resume := make(chan struct{}), make(chan struct{})
	old.onPage = func(int) error { close(entered); <-resume; return nil }
	type result struct {
		progress []Progress
		err      error
	}
	read := func(app *cachedReadApp) result {
		progress, err := cache.completedProgress(t.Context(), app, "project", "plugin")
		return result{progress, err}
	}
	done := make(chan result, 1)
	go func() { done <- read(old) }()
	<-entered
	fresh := read(newer)
	close(resume)
	stale := <-done
	again := read(newer)
	for _, tc := range []struct {
		result
		want string
	}{{fresh, "new"}, {stale, "old"}, {again, "new"}} {
		if tc.err != nil || len(tc.progress) != 1 || tc.progress[0].State.RequestID != tc.want {
			t.Fatalf("want %s: %+v", tc.want, tc.result)
		}
	}
}
