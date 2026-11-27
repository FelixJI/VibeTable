package pluginmodel

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"sync"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

const progressCacheStoreKey = "vibetable.pluginmodel.progress-cache"

type progressCacheKey struct{}
type historyScope struct{ project, plugin string }

// Decoding only depends on the payload. A verified non-completed record
// contributes no grant, so it need not retain a parsed model.
type verifiedProgress struct {
	raw       []byte
	completed *State
}

type verifiedHistory struct {
	scope historyScope
	rows  []verifiedProgress
	bytes int
}

type ProgressCacheStats struct {
	Reads, Hits, Decoded, Evicted uint64
	Entries, Bytes                int
}

type progressCache struct {
	mu            sync.Mutex
	entries       map[historyScope]*list.Element
	order         list.List
	bytes, budget int
	stats         ProgressCacheStats
}

func WithProgressCache(ctx context.Context, app core.App) context.Context {
	cache := app.Store().GetOrSet(progressCacheStoreKey, func() any {
		return &progressCache{entries: make(map[historyScope]*list.Element), budget: 64 << 20}
	}).(*progressCache)
	return context.WithValue(ctx, progressCacheKey{}, cache)
}

func ProgressCacheStatsFor(app core.App) ProgressCacheStats {
	cache, _ := app.Store().Get(progressCacheStoreKey).(*progressCache)
	if cache == nil {
		return ProgressCacheStats{}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	stats := cache.stats
	stats.Entries, stats.Bytes = len(cache.entries), cache.bytes
	return stats
}

func (c *progressCache) prior(scope historyScope) *verifiedHistory {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Reads++
	if entry := c.entries[scope]; entry != nil {
		c.order.MoveToFront(entry)
		return entry.Value.(*verifiedHistory)
	}
	return nil
}

// Compare each row with the same position in the previous read, retaining
// only records visible at this read point. Older snapshots, rolls back and
// equal-length replacements therefore cannot substitute another binding.
func (c *progressCache) completedStates(ctx context.Context, app core.App, project, plugin string) ([]State, error) {
	scope := historyScope{project, plugin}
	prior := c.prior(scope)
	next := &verifiedHistory{scope: scope}
	var decoded uint64
	index, reused := 0, 0
	completed := []State{}
	err := listProgress(ctx, app, project, plugin, nil, func(_ string, _ int64, payload any) error {
		raw, comparable := payload.(types.JSONRaw)
		var row verifiedProgress
		matched := comparable && prior != nil && index < len(prior.rows) && bytes.Equal(prior.rows[index].raw, raw)
		index++
		if matched {
			row = prior.rows[index-1]
			reused++
		} else {
			state, err := decodeProgress(payload)
			decoded++
			if err != nil {
				return err
			}
			if state.Contract != Contract {
				return errors.New("plugin model progress contract is invalid")
			}
			if state.Status == "completed" {
				row.completed = &state
			}
		}
		if row.completed != nil {
			completed = append(completed, *row.completed)
		}
		if next != nil {
			// Charge record metadata and an allowance for each parsed completion.
			size := 64 + len(raw)
			if row.completed != nil {
				size += len(raw)
			}
			if !comparable || size > c.budget-next.bytes {
				next = nil
			} else {
				if !matched {
					row.raw = bytes.Clone(raw)
				}
				next.bytes += size
				next.rows = append(next.rows, row)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Decoded += decoded
	if reused > 0 {
		c.stats.Hits++
	}
	if next == nil || (prior != nil && reused == index && len(prior.rows) == index) {
		return completed, nil
	}
	if entry := c.entries[scope]; entry != nil {
		c.remove(entry)
	}
	for len(c.entries) >= 1024 || c.bytes+next.bytes > c.budget {
		c.remove(c.order.Back())
		c.stats.Evicted++
	}
	c.entries[scope] = c.order.PushFront(next)
	c.bytes += next.bytes
	return completed, nil
}

func (c *progressCache) remove(e *list.Element) {
	history := e.Value.(*verifiedHistory)
	delete(c.entries, history.scope)
	c.bytes -= history.bytes
	c.order.Remove(e)
}
