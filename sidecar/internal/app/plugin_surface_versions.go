package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// data.versions (#589): one authoritative transaction resolves the current
// installation (action/entry binding, installation revision, model binding
// diagnostics) and the exact schema/data version of every authorized source.
// Only metadata is read: no business row, dataset page or cursor is touched,
// and nothing is resolved on the raw app before the transaction opens.
const (
	pluginVersionsContract    = "vibetable.plugin-data-versions.v1"
	pluginVersionsMaxSources  = 64
	pluginVersionsMaxFields   = 64
	pluginVersionsMaxKeyBytes = 128
	// The host revision contract is JS-safe; anything else is corrupt metadata.
	pluginVersionsMaxRevision = int64(1<<53 - 1)
)

// pluginVersionsEvaluationTime pins one evaluation instant per batch so every
// source's clock period derives from the same instant. DescribeProjectionTable
// re-pins per table only when the context carries no instant, so the whole
// batch must pin exactly once here instead of once per source.
var pluginVersionsEvaluationTime = formula.EnsureEvaluationTime

var errPluginVersionIncomplete = errors.New("plugin data version is incomplete")

type pluginVersionsSource struct {
	Collection string   `json:"collection"`
	Fields     []string `json:"fields"`
}

type pluginVersionsRequest struct {
	Contract string                 `json:"contract"`
	Sources  []pluginVersionsSource `json:"sources"`
	// Prior versions stay raw until validated: decoding straight into the
	// dataset version struct would read a missing or null dataRevision as 0
	// and let an incomplete version pass for a complete known one.
	Versions pluginPriorVersions `json:"versions,omitempty"`
}

type pluginPriorVersions map[string]json.RawMessage

func (prior *pluginPriorVersions) UnmarshalJSON(raw []byte) error {
	var versions map[string]json.RawMessage
	if err := json.Unmarshal(raw, &versions); err != nil || versions == nil {
		return errPluginVersionIncomplete
	}
	*prior = versions
	return nil
}

// validatePluginVersionsRequest runs before any storage read: duplicates,
// over-limit and malformed input fail the whole batch without a single query.
// It returns the decoded prior versions keyed by collection.
func validatePluginVersionsRequest(request pluginVersionsRequest) (map[string]pluginDatasetVersion, error) {
	invalid := pluginDataError("plugin_capability_invalid")
	if request.Contract != pluginVersionsContract || len(request.Sources) < 1 || len(request.Sources) > pluginVersionsMaxSources {
		return nil, invalid
	}
	seen := map[string]bool{}
	for _, source := range request.Sources {
		if source.Collection == "" || len(source.Collection) > pluginVersionsMaxKeyBytes || seen[source.Collection] || len(source.Fields) < 1 || len(source.Fields) > pluginVersionsMaxFields {
			return nil, invalid
		}
		seen[source.Collection] = true
		fields := map[string]bool{}
		for _, id := range source.Fields {
			if id == "" || len(id) > pluginVersionsMaxKeyBytes || fields[id] || strings.Contains(id, ".") {
				return nil, invalid
			}
			fields[id] = true
		}
	}
	prior := map[string]pluginDatasetVersion{}
	for collection, raw := range request.Versions {
		// Known versions must be complete: exactly the requested sources, each
		// in the exact shape the host emitted.
		if !seen[collection] {
			return nil, invalid
		}
		version, err := decodePluginDatasetVersion(raw)
		if err != nil || version.Data < 0 || version.Data > pluginVersionsMaxRevision {
			return nil, invalid
		}
		prior[collection] = version
	}
	if request.Versions != nil && len(prior) != len(seen) {
		return nil, invalid
	}
	return prior, nil
}

// decodePluginDatasetVersion accepts a prior version only as the host emitted
// it: every key present, non-null and nothing else, recursively for computed
// entries. The result reuses the dataset version struct so both contracts
// compare the same exact vector.
func decodePluginDatasetVersion(raw json.RawMessage) (pluginDatasetVersion, error) {
	var version pluginDatasetVersion
	object, err := pluginStrictObject(raw, "schemaRevision", "dataRevision", "clockPeriod", "computed")
	if err != nil {
		return version, err
	}
	var computed map[string]json.RawMessage
	if json.Unmarshal(object["computed"], &computed) != nil {
		return version, errPluginVersionIncomplete
	}
	for id, entry := range computed {
		if id == "" {
			return version, errPluginVersionIncomplete
		}
		if _, err = pluginStrictObject(entry, "definition", "watermark", "ready", "status"); err != nil {
			return version, err
		}
	}
	// The clock period may legitimately be empty (no clock reference); only a
	// missing or null key is rejected, which pluginStrictObject already did.
	if json.Unmarshal(raw, &version) != nil || version.Schema == "" || version.Computed == nil {
		return version, errPluginVersionIncomplete
	}
	return version, nil
}

// pluginStrictObject requires exactly the given keys and rejects null values.
func pluginStrictObject(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, error) {
	object := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &object) != nil || len(object) != len(keys) {
		return nil, errPluginVersionIncomplete
	}
	for _, key := range keys {
		value, ok := object[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errPluginVersionIncomplete
		}
	}
	return object, nil
}

func executeSurfaceDataVersions(ctx context.Context, app core.App, source *queryschema.Source, workspace string, input pluginSurfaceAPIRequest) (any, error) {
	var request pluginVersionsRequest
	if decodePluginParams(input.Request, &request) != nil {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	prior, err := validatePluginVersionsRequest(request)
	if err != nil {
		return nil, err
	}
	// One evaluation instant and one dependency-graph cache for the whole batch:
	// clock periods of all sources are derived from the same instant.
	ctx = relatedcomputation.WithClockCache(pluginVersionsEvaluationTime(ctx))
	var result map[string]any
	err = app.RunInTransaction(func(tx core.App) error {
		// Authorization is resolved exactly once and held by this transaction.
		installed, err := surfaceInstallation(ctx, tx, workspace, input)
		if err != nil {
			return err
		}
		versions := make(map[string]pluginDatasetVersion, len(request.Sources))
		stale := []string{}
		for _, item := range request.Sources {
			version, err := pluginSourceDataVersion(ctx, tx, source, installed, input.ActiveCollection, item)
			if err != nil {
				return err
			}
			versions[item.Collection] = version
			if known, ok := prior[item.Collection]; ok && !reflect.DeepEqual(known, version) {
				stale = append(stale, item.Collection)
			}
		}
		result = map[string]any{"contract": pluginVersionsContract, "versions": versions}
		if request.Versions != nil {
			result["stale"] = stale
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// pluginSourceDataVersion checks the source's current grant before any existence
// lookup, so an unauthorized collection is indistinguishable from a missing one,
// and binds the version to the explicit field projection the plugin reuses: a
// narrowed grant (for example only `id` left) fails the batch instead of letting
// previously authorized columns be reused.
func pluginSourceDataVersion(ctx context.Context, tx core.App, source *queryschema.Source, installed pluginSurfaceInstallation, active string, item pluginVersionsSource) (pluginDatasetVersion, error) {
	var version pluginDatasetVersion
	grant := installed.grant(item.Collection, active)
	if grant == nil {
		return version, pluginDataError("plugin_read_denied")
	}
	table, err := schemaexecution.Describe(ctx, tx, item.Collection)
	if err != nil || table.Snapshot.TableID != item.Collection {
		return version, pluginDataError("plugin_query_failed")
	}
	allowed := pluginAllowed(*grant, table.Snapshot)
	for _, id := range item.Fields {
		if _, ok := allowed[id]; !ok {
			return version, pluginDataError("plugin_read_denied")
		}
	}
	descriptor, _, err := source.DescribeResolvedProjectionTable(ctx, tx, table, item.Fields)
	if err != nil || descriptor.SchemaRevision == "" || descriptor.DataRevision < 0 || descriptor.DataRevision > pluginVersionsMaxRevision {
		return version, pluginDataError("plugin_query_failed")
	}
	// Computed entries are keyed by canonical field ID; only projected computed
	// fields appear and the map is always emitted so versions compare exactly.
	computed := map[string]pluginDatasetComputedRevision{}
	for _, id := range item.Fields {
		field, ok := descriptor.Fields[allowed[id]]
		if !ok {
			return version, pluginDataError("plugin_query_failed")
		}
		if field.ComputedEnvelope {
			computed[id] = pluginDatasetComputedRevision{Definition: field.ComputedDefinitionVersion, Watermark: field.ComputedDependencyWatermark, Ready: field.ComputedReady, Status: field.ComputedStatus}
		}
	}
	return pluginDatasetVersion{Schema: descriptor.SchemaRevision, Data: descriptor.DataRevision, Clock: descriptor.ClockPeriod, Computed: computed}, nil
}
