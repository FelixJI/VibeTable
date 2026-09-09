package formula

import (
	"container/list"
	"encoding/json"
	"fmt"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const planCacheCapacity = 128

type planCacheKey struct {
	tableID    string
	revision   string
	definition string
}

type planCacheEntry struct {
	key     planCacheKey
	plan    *Plan
	element *list.Element
}

type planCache struct {
	mu      sync.Mutex
	entries map[planCacheKey]*planCacheEntry
	lru     list.List
	flights singleflight.Group
	compile func(schemaexecution.Table) (*Plan, *Error)
	// currentRevision is configured before the cache is shared with callers.
	currentRevision func(string) (string, error)
	epoch           uint64
}

func newPlanCache(compile func(schemaexecution.Table) (*Plan, *Error)) *planCache {
	return &planCache{entries: map[planCacheKey]*planCacheEntry{}, compile: compile}
}

func (cache *planCache) get(definition schemaexecution.Table) (*Plan, error) {
	// Compilation consumes schema fields, not data watermarks or execution
	// statuses. Data-only writes must not invalidate the compiled definition.
	snapshot := definition.Snapshot
	snapshot.DataRevision = 0
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode formula definition: %w", err)
	}
	key := planCacheKey{
		tableID: definition.Snapshot.TableID, revision: definition.Snapshot.SchemaRevision,
		definition: string(raw),
	}
	cache.mu.Lock()
	entry := cache.entries[key]
	if entry == nil {
		epoch := cache.epoch
		cache.mu.Unlock()
		admit := true
		if cache.currentRevision != nil {
			revision, err := cache.currentRevision(key.tableID)
			if err != nil {
				return nil, err
			}
			admit = revision != "" && revision == key.revision
		}
		cache.mu.Lock()
		// A commit during the authority read makes this admission obsolete.
		// Do not retry or retain a per-table history of observed revisions.
		if admit && epoch == cache.epoch {
			entry = cache.entries[key]
			if entry == nil {
				// Pending compilations count toward capacity as well as ready plans.
				entry = &planCacheEntry{key: key}
				entry.element = cache.lru.PushFront(entry)
				cache.entries[key] = entry
				if len(cache.entries) > planCacheCapacity {
					cache.remove(cache.lru.Back().Value.(*planCacheEntry))
				}
			}
		}
	}
	var ready *Plan
	if entry != nil {
		cache.lru.MoveToFront(entry.element)
		ready = entry.plan
	}
	cache.mu.Unlock()
	if ready != nil {
		return ready, nil
	}
	value, err, _ := cache.flights.Do(key.definition, func() (any, error) {
		// A previous flight may have finished between the initial lookup and Do.
		cache.mu.Lock()
		if entry != nil && cache.entries[key] == entry && entry.plan != nil {
			ready := entry.plan
			cache.mu.Unlock()
			return ready, nil
		}
		cache.mu.Unlock()
		compiled, compileErr := cache.compile(definition)
		cache.mu.Lock()
		// A flight may finish after invalidation and an identical re-request.
		// Only the original admitted entry may receive this flight's result.
		if entry != nil && cache.entries[key] == entry {
			if compileErr != nil {
				cache.remove(entry)
			} else {
				entry.plan = compiled
			}
		}
		cache.mu.Unlock()
		if compileErr != nil {
			return nil, compileErr
		}
		return compiled, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*Plan), nil
}

func (cache *planCache) invalidateTable(tableID string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.epoch++
	for key, entry := range cache.entries {
		if key.tableID == tableID {
			cache.remove(entry)
		}
	}
}

func (cache *planCache) refreshTable(tableID string) error {
	cache.mu.Lock()
	cache.epoch++
	entries := make([]*planCacheEntry, 0)
	for key, entry := range cache.entries {
		if key.tableID == tableID {
			entries = append(entries, entry)
		}
	}
	cache.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	// Read current authority, not a possibly delayed commit event's revision.
	// Do not hold the cache lock across a database read, and only remove entries
	// observed before that read: a newer commit can already have cached its plan.
	revision, err := cache.currentRevision(tableID)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, entry := range entries {
		if cache.entries[entry.key] == entry && (err != nil || entry.key.revision != revision) {
			cache.remove(entry)
		}
	}
	return err
}

// remove is called with cache.mu held.
func (cache *planCache) remove(entry *planCacheEntry) {
	delete(cache.entries, entry.key)
	cache.lru.Remove(entry.element)
}
