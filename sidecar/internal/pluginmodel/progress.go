package pluginmodel

import (
	"bytes"
	"container/list"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"sync"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

type Binding struct {
	TableID string            `json:"tableId"`
	Fields  map[string]string `json:"fields"`
}
type State struct {
	Contract             string             `json:"contract"`
	RequestID            string             `json:"requestId"`
	InstallationID       string             `json:"installationId"`
	InstallationRevision int64              `json:"installationRevision"`
	Model                Declaration        `json:"model"`
	Status               string             `json:"status"`
	Completed            int                `json:"completed"`
	Total                int                `json:"total"`
	Bindings             map[string]Binding `json:"bindings"`
	Revisions            map[string]string  `json:"revisions"`
	Diagnostics          []v2.Diagnostic    `json:"diagnostics,omitempty"`
	ErrorCode            string             `json:"errorCode,omitempty"`
}

// States reads only platform model progress in the existing immutable audit log.
// It deliberately survives disable, uninstall and private-storage cleanup.
func States(ctx context.Context, app core.App, project, plugin string) ([]State, error) {
	history, err := History(ctx, app, project, plugin)
	if err != nil {
		return nil, err
	}
	states := make([]State, 0, len(history))
	for _, progress := range history {
		states = append(states, progress.State)
	}
	return states, nil
}

// Progress is one validated model progress state and the audit sequence of its record.
// The item key is deliberately not part of it: authority never depends on key spelling.
type Progress struct {
	Seq   int64 `json:"-"`
	State State
}

// History reads all model progress, oldest first, with each record's audit sequence.
func History(ctx context.Context, app core.App, project, plugin string) ([]Progress, error) {
	return readProgress(ctx, app, project, plugin, nil)
}

// StatesAfter reads the model progress recorded after an audit sequence, oldest first.
// Audit rows are appended with a per-project sequence, so the plugin event index
// serves the range directly: a caller that already validated the history up to
// `after` pays only for newer records. Validation is identical to History and no
// record inside the range is skipped, so a corrupt row still fails the read.
func StatesAfter(ctx context.Context, app core.App, project, plugin string, after int64) ([]Progress, error) {
	return readProgress(ctx, app, project, plugin, &after)
}

// CompletedStates reads the completed model progress, oldest first, validating
// every listed record exactly like History. In a context scoped by
// WithProgressCache it reuses the result of an unchanged history.
func CompletedStates(ctx context.Context, app core.App, project, plugin string) ([]State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := app.FindCollectionByNameOrId("vibetable_plugin_records"); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []State{}, nil
		}
		return nil, err
	}
	if cache, ok := ctx.Value(progressCacheKey{}).(*ProgressCache); ok {
		return cache.completedStates(ctx, app, project, plugin)
	}
	completed := []State{}
	err := listProgress(ctx, app, project, plugin, nil, func(_ string, _ int64, payload any) error {
		state, err := decodeProgress(payload)
		if err != nil {
			return err
		}
		if state.Contract != Contract {
			return errors.New("plugin model progress contract is invalid")
		}
		if state.Status == "completed" {
			completed = append(completed, state)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return completed, nil
}

// ProgressCache bounds the verified model histories one host app keeps.
//
// The unit of the cache is the model progress of one plugin in one project.
// An entry holds the completed states of that history and the exact bytes of
// every record they were derived from: id, sequence and payload. A read lists
// the current records first and compares them with those bytes, so a hit means
// the result was derived from exactly the records the database lists now.
// Progress of a rolled back transaction and records only a newer snapshot lists
// never reach an older read, and a record that changed or disappeared in place
// invalidates the whole history; no digest is trusted. Records appended after an
// unchanged prefix are decoded alone. A history with a corrupt record or a
// foreign contract is never cached and keeps failing every read until it is
// repaired. A history is kept or evicted as a whole, so a working set above the
// byte bound is not cached at all instead of being rescanned record by record.
// Cached states are shared between reads and must be treated as read-only.
// ProgressCacheMaxBytes bounds the bytes the cache keeps: the record bytes of
// every cached history plus the payload bytes of its completed records as the
// allowance for their parsed form. It has to exceed the UTF-8 bytes of the
// histories a host reads continuously; a history above it is never cached.
var ProgressCacheMaxBytes = 128 << 20

const (
	ProgressCacheMaxEntries = 1024
	progressCacheStoreKey   = "vibetable.pluginmodel.progress-cache"
)

type progressCacheKey struct{}

type listedProgress struct {
	payload any
	bytes   int
}

type progressCacheEntry struct {
	scope          string
	witness        []byte // id, sequence and payload bytes of every listed record, oldest first
	records        int
	completed      []State
	completedBytes int // payload bytes of the completed records, the allowance for their parsed form
}

// ProgressCacheStats reports the decode work of the cached histories of one
// app. Tests read it around reads to observe the decode count.
type ProgressCacheStats struct {
	Reads   uint64 // cached history reads
	Hits    uint64 // reads served by an unchanged history or its unchanged prefix
	Decoded uint64 // records decoded
	Evicted uint64 // histories removed by the capacity bound
	Entries int
	Bytes   int
}

// ProgressCache is a least-recently-used cache of histories; transaction
// clones of an app share the cache of their app.
type ProgressCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
	bytes   int
	stats   ProgressCacheStats
}

func newProgressCache() *ProgressCache {
	return &ProgressCache{entries: make(map[string]*list.Element)}
}

// ProgressCacheFor returns the history cache of this host app. It lives in the
// app runtime store, so it is released together with the app.
func ProgressCacheFor(app core.App) *ProgressCache {
	if app == nil {
		return nil
	}
	cache, _ := app.Store().GetOrSet(progressCacheStoreKey, func() any { return newProgressCache() }).(*ProgressCache)
	return cache
}

// WithProgressCache scopes the history cache of app to one read method. Reads
// outside such a context keep decoding every listed record.
func WithProgressCache(ctx context.Context, app core.App) context.Context {
	if ctx == nil {
		return nil
	}
	if cache := ProgressCacheFor(app); cache != nil {
		return context.WithValue(ctx, progressCacheKey{}, cache)
	}
	return ctx
}

// ProgressCacheStatsFor returns the decode statistics of the app's history cache.
func ProgressCacheStatsFor(app core.App) ProgressCacheStats {
	if app == nil {
		return ProgressCacheStats{}
	}
	if cache, _ := app.Store().Get(progressCacheStoreKey).(*ProgressCache); cache != nil {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		stats := cache.stats
		stats.Entries, stats.Bytes = len(cache.entries), cache.bytes
		return stats
	}
	return ProgressCacheStats{}
}

const progressWitnessMaxPooled = 64 << 20

var progressWitnessPool = sync.Pool{New: func() any { return make([]byte, 0, 64<<10) }}

func progressCacheScope(project, plugin string) string {
	return project + "\x00" + plugin
}

func appendProgressWitness(witness []byte, id string, seq int64, payload []byte) []byte {
	witness = binary.BigEndian.AppendUint16(witness, uint16(len(id)))
	witness = append(witness, id...)
	witness = binary.BigEndian.AppendUint64(witness, uint64(seq))
	witness = binary.BigEndian.AppendUint64(witness, uint64(len(payload)))
	return append(witness, payload...)
}

// completedStates lists the current history and compares it byte for byte with
// the cached one before any of the cached states is returned.
func (c *ProgressCache) completedStates(ctx context.Context, app core.App, project, plugin string) ([]State, error) {
	scope := progressCacheScope(project, plugin)
	c.mu.Lock()
	var prior progressCacheEntry
	cached := false
	if element, ok := c.entries[scope]; ok {
		prior, cached = *element.Value.(*progressCacheEntry), true
	}
	c.stats.Reads++
	c.mu.Unlock()

	listed := []listedProgress{}
	witness := progressWitnessPool.Get().([]byte)[:0]
	defer func() {
		if cap(witness) <= progressWitnessMaxPooled {
			progressWitnessPool.Put(witness[:0])
		}
	}()
	err := listProgress(ctx, app, project, plugin, nil, func(id string, seq int64, payload any) error {
		raw, ok := payload.(types.JSONRaw)
		if !ok {
			marshaled, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			raw = types.JSONRaw(marshaled)
		}
		witness = appendProgressWitness(witness, id, seq, raw)
		listed = append(listed, listedProgress{payload: payload, bytes: len(raw)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if cached && bytes.Equal(prior.witness, witness) {
		c.mu.Lock()
		if element, ok := c.entries[scope]; ok {
			c.order.MoveToFront(element)
		}
		c.stats.Hits++
		c.mu.Unlock()
		return prior.completed, nil
	}
	// Records appended after an unchanged prefix are the only ones to decode.
	from, completedBytes := 0, 0
	var base []State
	if cached && len(witness) >= len(prior.witness) && bytes.Equal(witness[:len(prior.witness)], prior.witness) {
		// The listed records start with exactly the records of the cached history.
		from, base, completedBytes = prior.records, prior.completed, prior.completedBytes
	}
	completed := make([]State, 0, len(base)+len(listed)-from)
	completed = append(completed, base...)
	for i := from; i < len(listed); i++ {
		state, err := decodeProgress(listed[i].payload)
		if err != nil {
			return nil, err
		}
		if state.Contract != Contract {
			return nil, errors.New("plugin model progress contract is invalid")
		}
		if state.Status == "completed" {
			completed = append(completed, state)
			completedBytes += listed[i].bytes
		}
	}
	c.keep(&progressCacheEntry{
		scope:          scope,
		witness:        append([]byte(nil), witness...),
		records:        len(listed),
		completed:      completed,
		completedBytes: completedBytes,
	}, len(listed)-from, from > 0)
	return completed, nil
}

// keep stores a decoded history. decoded of its records were decoded in this
// read; the others were served by the unchanged prefix of the cached history.
func (c *ProgressCache) keep(entry *progressCacheEntry, decoded int, reused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Decoded += uint64(decoded)
	if reused {
		c.stats.Hits++
	}
	if element, ok := c.entries[entry.scope]; ok {
		c.remove(element)
	}
	c.entries[entry.scope] = c.order.PushFront(entry)
	c.bytes += len(entry.witness) + entry.completedBytes
	for len(c.entries) > ProgressCacheMaxEntries || c.bytes > ProgressCacheMaxBytes {
		c.remove(c.order.Back())
		c.stats.Evicted++
	}
}

func (c *ProgressCache) remove(element *list.Element) {
	entry := element.Value.(*progressCacheEntry)
	c.order.Remove(element)
	delete(c.entries, entry.scope)
	c.bytes -= len(entry.witness) + entry.completedBytes
}

func readProgress(ctx context.Context, app core.App, project, plugin string, after *int64) ([]Progress, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := app.FindCollectionByNameOrId("vibetable_plugin_records"); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	result := []Progress{}
	err := listProgress(ctx, app, project, plugin, after, func(_ string, seq int64, payload any) error {
		state, err := decodeProgress(payload)
		if err != nil {
			return err
		}
		if state.Contract != Contract {
			return errors.New("plugin model progress contract is invalid")
		}
		result = append(result, Progress{Seq: seq, State: state})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// listProgress pages through the model progress records the current snapshot
// lists, oldest first, and visits each with its record id, sequence and payload.
func listProgress(ctx context.Context, app core.App, project, plugin string, after *int64, visit func(id string, seq int64, payload any) error) error {
	filter := "kind='audit' && payload_json.eventType='model.progress'"
	params := dbx.Params{}
	if after != nil {
		filter += " && seq>{:after}"
		params["after"] = *after
	}
	if project != "" {
		filter += " && project_key={:project}"
		params["project"] = project
	}
	if plugin != "" {
		filter += " && plugin_id={:plugin}"
		params["plugin"] = plugin
	}
	for offset := 0; ; offset += 256 {
		rows, err := app.FindRecordsByFilter("vibetable_plugin_records", filter, "seq", 256, offset, params)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(row.Id, int64(row.GetInt("seq")), row.GetRaw("payload_json")); err != nil {
				return err
			}
		}
		if len(rows) < 256 {
			return nil
		}
	}
}

func decodeProgress(payload any) (State, error) {
	var event struct {
		Details State `json:"details"`
	}
	if stored, ok := payload.(types.JSONRaw); ok {
		raw, err := stored.MarshalJSON()
		if err == nil && jsonv2.Unmarshal(raw, &event, json.DefaultOptionsV1(), json.ReportErrorsWithLegacySemantics(false)) == nil {
			return event.Details, nil
		}
		event.Details = State{}
	}
	// Preserve legacy errors and accepted values for rejected or non-PocketBase payloads.
	raw, err := json.Marshal(payload)
	if err != nil {
		return State{}, err
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return State{}, err
	}
	return event.Details, nil
}

func ProtectTable(ctx context.Context, app core.App, table string) error {
	states, err := States(ctx, app, "", "")
	if err != nil {
		return err
	}
	for _, s := range states {
		for _, b := range s.Bindings {
			if b.TableID == table {
				return protected("tableId")
			}
		}
	}
	return nil
}

func ProtectField(ctx context.Context, app core.App, table, field string, before, after *v2.FieldDefinition) error {
	if field == "" || before == nil {
		return nil
	}
	states, err := States(ctx, app, "", "")
	if err != nil {
		return err
	}
	for _, s := range states {
		for _, b := range s.Bindings {
			if b.TableID != table {
				continue
			}
			for _, id := range b.Fields {
				if id != field {
					continue
				}
				if after == nil {
					return protected("fieldId")
				}
				// Display and help remain user-editable; value/storage/relations stay compatible.
				a, z := *before, *after
				a.DisplayName, a.Help, a.Display = z.DisplayName, z.Help, z.Display
				if !reflect.DeepEqual(a, z) {
					return protected("fieldId")
				}
			}
		}
	}
	return nil
}
func protected(path string) error {
	return &schemaerror.ProductError{Code: "plugin.model.core_protected", Path: path, Message: "field or table is bound to a plugin model; rename and user extension fields remain editable"}
}
