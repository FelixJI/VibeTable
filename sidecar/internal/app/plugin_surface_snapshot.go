package app

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// snapshot.open / snapshot.validate (#590): one authoritative transaction
// resolves the installation, every source grant, the exact #589 version vector
// and the bounded row projection of up to 64 sources, then materializes the
// whole snapshot in memory. The transaction closes before the Host hands out a
// handle; pages are slices of that materialization. Every page is revalidated
// in Go against the current installation, grants and schema revision, so a
// narrowed grant, a remapped model binding or a schema change fails the page
// instead of serving rows the plugin may no longer read. A narrowed id grant
// alone fails only pages that use id; for the others validate reports per
// source whether row guards keyed by row id may still be served. Data revisions
// and clock periods deliberately stay out of the page check: an open snapshot is a
// fixed read point, later row writes never mix into it and never extend it.
const (
	pluginSnapshotContract    = "vibetable.plugin-snapshot.v1"
	pluginSnapshotMaxSources  = 64
	pluginSnapshotMaxFields   = 64
	pluginSnapshotMaxKeyBytes = 128
	pluginSnapshotMaxIDs      = 200
	pluginSnapshotRows        = 4096
	pluginSnapshotBytes       = 512 * 1024
)

type pluginSnapshotSource struct {
	Query pluginQueryRequest `json:"query"`
}

type pluginSnapshotRequest struct {
	Contract string                 `json:"contract"`
	Sources  []pluginSnapshotSource `json:"sources"`
	// validate only: the open result's versions, decoded strictly so a missing
	// or null member can never pass for a complete known version.
	Versions pluginPriorVersions `json:"versions,omitempty"`
	// validate only: the audit sequence of the completed model record the open
	// reported, or null for none. Decoded strictly (a string or a fraction is
	// rejected); presence is checked on the raw request, because a decoded nil
	// is indistinguishable from a missing member.
	Binding *int64 `json:"binding,omitempty"`
}

func pluginSnapshotWildcard(grant *pluginReadGrant) bool {
	return grant == nil || pluginHas(grant.Fields, "*") || pluginHas(grant.Fields, "$configured")
}

// snapshotUsedFields lists every field ID the query reads with exactly the
// data.query normalization semantics: the projection, the sorts, the leaves of
// the filter tree (an AND/OR group is structure only and contributes no field
// of its own) and, whenever ids is set, "id", because the id filter needs a
// current grant like any other filter field. They all need a current grant at
// open and again before every page.
func snapshotUsedFields(r pluginQueryRequest) []string {
	used := append([]string{}, r.Fields...)
	var visit func([]query.FilterExpression)
	visit = func(filters []query.FilterExpression) {
		for _, f := range filters {
			if len(f.Filters) > 0 {
				visit(f.Filters)
				continue
			}
			used = append(used, f.Field)
		}
	}
	visit(r.Filters)
	for _, sort := range r.Sorts {
		used = append(used, sort.Field)
	}
	if r.IDs != nil {
		used = append(used, "id")
	}
	return used
}

// validatePluginSnapshotRequest runs before any storage read. Open and validate
// are distinguished by the presence of versions; both fail the whole request
// without a single query on duplicates, over-limit batches or malformed keys.
func validatePluginSnapshotRequest(request pluginSnapshotRequest, operation string) (map[string]pluginDatasetVersion, error) {
	invalid := pluginDataError("plugin_capability_invalid")
	if request.Contract != pluginSnapshotContract || len(request.Sources) < 1 || len(request.Sources) > pluginSnapshotMaxSources {
		return nil, invalid
	}
	if operation == "snapshot.open" && request.Versions != nil || operation == "snapshot.validate" && request.Versions == nil {
		return nil, invalid
	}
	seen := map[string]bool{}
	for _, source := range request.Sources {
		r := source.Query
		if r.Contract != "vibetable.plugin-query.v2" || r.Collection == "" || len(r.Collection) > pluginSnapshotMaxKeyBytes || strings.HasPrefix(r.Collection, "$") || seen[r.Collection] || len(r.Fields) < 1 || len(r.Fields) > pluginSnapshotMaxFields || r.PageSize != nil {
			return nil, invalid
		}
		seen[r.Collection] = true
		if r.IDs != nil && len(*r.IDs) > pluginSnapshotMaxIDs {
			return nil, pluginDataError("plugin_query_limit")
		}
		fields := map[string]bool{}
		for _, id := range r.Fields {
			if id == "" || len(id) > pluginSnapshotMaxKeyBytes || fields[id] || strings.Contains(id, ".") {
				return nil, invalid
			}
			fields[id] = true
		}
		for _, id := range snapshotUsedFields(r) {
			if id == "" || len(id) > pluginSnapshotMaxKeyBytes || strings.Contains(id, ".") {
				return nil, invalid
			}
		}
	}
	known := map[string]pluginDatasetVersion{}
	for collection, raw := range request.Versions {
		if !seen[collection] {
			return nil, invalid
		}
		version, err := decodePluginDatasetVersion(raw)
		if err != nil || version.Data < 0 || version.Data > pluginVersionsMaxRevision {
			return nil, invalid
		}
		known[collection] = version
	}
	if request.Versions != nil && len(known) != len(seen) {
		return nil, invalid
	}
	return known, nil
}

// snapshotRequestMalformed rejects the two things the decoded request cannot
// tell apart: explicit nulls exactly where data.query rejects them (ids, filters,
// sorts and pageSize: a decoded nil is indistinguishable from an absent key) and
// the mere presence of versions or binding on an open, null or not, because
// both members belong to validate only.
func snapshotRequestMalformed(raw json.RawMessage, operation string) bool {
	var envelope snapshotRequestEnvelope
	if json.Unmarshal(raw, &envelope) != nil || operation == "snapshot.open" && (len(envelope.Versions) != 0 || len(envelope.Binding) != 0) {
		return true
	}
	for _, source := range envelope.Sources {
		var outer map[string]json.RawMessage
		if json.Unmarshal(source, &outer) != nil {
			return true
		}
		var inner map[string]json.RawMessage
		if json.Unmarshal(outer["query"], &inner) != nil {
			return true
		}
		for _, key := range []string{"ids", "filters", "sorts", "pageSize"} {
			if value, ok := inner[key]; ok && string(value) == "null" {
				return true
			}
		}
	}
	return false
}

// snapshotRequestEnvelope is the raw view of a request for the rules the strict
// decode cannot express: explicit nulls, and members that belong to one
// operation only (presence, null or not).
type snapshotRequestEnvelope struct {
	Sources  []json.RawMessage `json:"sources"`
	Versions json.RawMessage   `json:"versions"`
	Binding  json.RawMessage   `json:"binding"`
}

// snapshotRequestBinding returns the validate-only binding member: the audit
// sequence the open reported, or nil for none. Its presence is checked on the
// raw request, so a dropped member can never pass for "no binding"; the value
// itself was decoded strictly with the request.
func snapshotRequestBinding(request pluginSnapshotRequest, raw json.RawMessage) (*int64, error) {
	var envelope snapshotRequestEnvelope
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Binding) == 0 || request.Binding != nil && *request.Binding < 1 {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	return request.Binding, nil
}

// sameSnapshotBinding holds only when the record the open named still is the
// authoritative one: none equals none, and none never equals a record.
func sameSnapshotBinding(reported, current *int64) bool {
	if reported == nil || current == nil {
		return reported == nil && current == nil
	}
	return *reported == *current
}

func executeSurfaceSnapshot(ctx context.Context, app core.App, source *queryschema.Source, workspace string, input pluginSurfaceAPIRequest) (any, error) {
	operation := input.Method
	var request pluginSnapshotRequest
	if jsonv2.Unmarshal(input.Request, &request, jsonv2.RejectUnknownMembers(true)) != nil || snapshotRequestMalformed(input.Request, operation) {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	known, err := validatePluginSnapshotRequest(request, operation)
	if err != nil {
		return nil, err
	}
	var binding *int64
	if operation == "snapshot.validate" {
		if binding, err = snapshotRequestBinding(request, input.Request); err != nil {
			return nil, err
		}
	}
	// One evaluation instant and one dependency-graph cache for the whole batch:
	// clock periods of all sources are derived from the same instant, exactly
	// like data.versions, so snapshot.versions equals the versions reply. The
	// same-row authorization view and every source description share one full
	// cache for the transaction; nothing is copied per source.
	ctx = relatedcomputation.WithSameRowClockCacheView(pluginVersionsEvaluationTime(ctx))
	var result map[string]any
	// validate: per requested collection, whether the plugin may currently read
	// id, i.e. whether row guards keyed by row id may be served from the corpus.
	var identity map[string]bool
	err = app.RunInTransaction(func(tx core.App) error {
		// Authorization is resolved exactly once and held by this transaction.
		installed, err := surfaceInstallation(ctx, tx, workspace, input)
		if err != nil {
			return err
		}
		if operation == "snapshot.validate" {
			identity, err = validatePluginSnapshot(ctx, tx, installed, input.ActiveCollection, request, known, binding)
			return err
		}
		result, err = openPluginSnapshot(ctx, tx, source, installed, input.ActiveCollection, request)
		return err
	})
	if err != nil {
		return nil, err
	}
	if operation == "snapshot.validate" {
		return map[string]any{"valid": true, "identity": identity}, nil
	}
	return result, nil
}

// openPluginSnapshot materializes every source inside the caller's transaction.
func openPluginSnapshot(ctx context.Context, tx core.App, source *queryschema.Source, installed pluginSurfaceInstallation, active string, request pluginSnapshotRequest) (map[string]any, error) {
	versions := map[string]pluginDatasetVersion{}
	outputs := []any{}
	remainingRows, remainingBytes := pluginSnapshotRows, pluginSnapshotBytes
	for _, item := range request.Sources {
		r := item.Query
		grant := installed.grant(r.Collection, active)
		if pluginSnapshotWildcard(grant) {
			return nil, pluginDataError("plugin_read_denied")
		}
		// The version vector is the #589 vector verbatim: same authorization of
		// the projection (including same-row formula closure), same pinned
		// instant, same completeness rules.
		version, err := pluginSourceDataVersion(ctx, tx, installed, active, pluginVersionsSource{Collection: r.Collection, Fields: r.Fields})
		if err != nil {
			return nil, err
		}
		scoped := &pluginQuerySource{source: source, grant: *grant, request: r, readonly: true}
		d, _, err := scoped.DescribeSelectionTable(ctx, tx, r.Collection)
		if err != nil {
			return nil, err
		}
		// Digest fields stay: row guards are the data.query digest contract. Without
		// the id grant no guard can ever be keyed, so id is neither projected nor
		// digested and the full business records behind the rows stay unread.
		idReadable := scoped.names["id"] != ""
		d.ProjectionFields = []string{}
		if idReadable {
			d.ProjectionFields = append(d.ProjectionFields, "id")
		}
		for _, id := range r.Fields {
			if id != "id" {
				d.ProjectionFields = append(d.ProjectionFields, scoped.names[id])
			}
		}
		rows, count, total, err := query.ReadBounded(ctx, tx, d, scoped.normalized, remainingRows, &remainingBytes)
		if err != nil {
			var product *query.ProductError
			if errors.As(err, &product) && product.Code == "plugin_dataset_limit" {
				return nil, pluginDataError("plugin_snapshot_limit")
			}
			return nil, err
		}
		if idReadable {
			if err := query.AttachRowDigests(ctx, tx, d, rows); err != nil {
				return nil, err
			}
		}
		remainingRows -= len(rows)
		items := []map[string]any{}
		// Row guards are the data.query contract: one digest per row, keyed by
		// row id, for every row the plugin may address (id granted) whether or
		// not id is projected. The corpus keeps them aligned with items as
		// [id, digest] pairs (null where the plugin may not address the row) so
		// the Host slices any offset without the public rows carrying the id;
		// whether they may be served is re-decided by every validate.
		guards := make([]any, 0, len(rows))
		computedFields := []string{}
		complete := true
		for _, id := range r.Fields {
			if field := scoped.computed[scoped.names[id]]; field.ComputedEnvelope {
				computedFields = append(computedFields, id)
				if !field.ComputedReady {
					complete = false
				}
			}
		}
		for _, row := range rows {
			var guard any
			if id, ok := row["id"].(string); ok && idReadable {
				guard = []any{id, row[productrow.DigestField]}
			}
			guards = append(guards, guard)
			projected := map[string]any{}
			for _, id := range r.Fields {
				value := row[scoped.names[id]]
				if field := scoped.computed[scoped.names[id]]; field.ComputedEnvelope {
					state, fresh := "updating", false
					if envelope, ok := value.(map[string]any); ok {
						if status, exists := envelope["state"].(string); exists {
							state, fresh = status, field.ComputedReady && status == "ready"
							value = envelope["value"]
						}
					}
					if !fresh {
						value = nil
						complete = false
					}
					value = map[string]any{"state": state, "fresh": fresh, "value": value}
				}
				projected[id] = value
			}
			items = append(items, projected)
		}
		versions[r.Collection] = version
		outputs = append(outputs, map[string]any{"collection": r.Collection, "fields": r.Fields, "computedFields": computedFields, "items": items, "rowGuards": guards,
			"filteredRows": count, "totalRows": total, "complete": complete})
	}
	result := map[string]any{"contract": pluginSnapshotContract, "sources": outputs, "versions": versions}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > pluginSnapshotBytes {
		return nil, pluginDataError("plugin_snapshot_limit")
	}
	// Host-only: the audit sequence of the completed model record this handle
	// was opened under (null without one), fixed inside this transaction. The
	// Host keeps it with the handle, never shows it to the plugin and echoes it
	// on every validate.
	result["binding"] = installed.ModelBindingSeq
	return result, ctx.Err()
}

// validatePluginSnapshot re-checks every source before a page is served and
// reports, per collection, whether the plugin may currently read id (the
// precondition for serving row guards keyed by row id). The completed model
// record the handle was opened under must still be the authoritative one. Plain
// sources (no computed projection, no model binding, every used field granted
// by its canonical ID) are proven by one batched schema-revision read plus
// in-memory grant checks; anything else falls back to the full Describe +
// pluginAllowed + formula-closure authorization. A missing batch answer never
// fails the page: it only moves sources to the slow path.
func validatePluginSnapshot(ctx context.Context, tx core.App, installed pluginSurfaceInstallation, active string, request pluginSnapshotRequest, known map[string]pluginDatasetVersion, binding *int64) (map[string]bool, error) {
	identity := map[string]bool{}
	plain := pluginSnapshotPlainGrants(installed)
	fast := []string{}
	slow := []pluginSnapshotSource{}
	// One value for the whole handle: the audit sequence of the newest completed
	// model record, fixed by the open inside its authorization transaction and
	// compared here inside this one. Any later completed record (a remap of any
	// table or field, even one this projection never reads, and even A→B→A)
	// carries a new never-reused sequence, and so does none becoming bound; the
	// authority is re-resolved by surfaceInstallation, never replayed here.
	if !sameSnapshotBinding(binding, installed.ModelBindingSeq) {
		return nil, pluginDataError("plugin_snapshot_stale")
	}
	for _, item := range request.Sources {
		r := item.Query
		grant := installed.grant(r.Collection, active)
		if pluginSnapshotWildcard(grant) {
			return nil, pluginDataError("plugin_read_denied")
		}
		version, ok := known[r.Collection]
		if !ok {
			return nil, pluginDataError("plugin_snapshot_stale")
		}
		covered := plain && len(version.Computed) == 0
		for _, id := range snapshotUsedFields(r) {
			covered = covered && pluginHas(grant.Fields, id)
		}
		if covered {
			fast = append(fast, r.Collection)
			identity[r.Collection] = pluginHas(grant.Fields, "id")
		} else {
			slow = append(slow, item)
		}
	}
	revisions, complete := snapshotSchemaRevisions(ctx, tx, fast)
	if !complete {
		slow = append(slow, pluginSnapshotSourcesOf(request, fast)...)
	} else {
		for _, collection := range fast {
			if revisions[collection] != known[collection].Schema {
				return nil, pluginDataError("plugin_snapshot_stale")
			}
		}
	}
	for _, item := range slow {
		readable, err := pluginSnapshotProjectionCurrent(ctx, tx, installed, active, item.Query, known[item.Query.Collection])
		if err != nil {
			return nil, err
		}
		identity[item.Query.Collection] = readable
	}
	return identity, nil
}

func pluginSnapshotPlainGrants(installed pluginSurfaceInstallation) bool {
	return len(installed.ModelGrants) == 0 && len(installed.ModelOutputFields) == 0
}

func pluginSnapshotSourcesOf(request pluginSnapshotRequest, collections []string) []pluginSnapshotSource {
	wanted := map[string]bool{}
	for _, collection := range collections {
		wanted[collection] = true
	}
	sources := []pluginSnapshotSource{}
	for _, item := range request.Sources {
		if wanted[item.Query.Collection] {
			sources = append(sources, item)
		}
	}
	return sources
}

// snapshotSchemaRevisions reads the stored schema revision of several tables in
// one statement. It reports complete=false on any anomaly (missing row, odd
// storage type, unparsable literal) so the caller falls back to Describe,
// which owns the exact metadata error.
func snapshotSchemaRevisions(ctx context.Context, tx core.App, collections []string) (map[string]string, bool) {
	if len(collections) == 0 {
		return map[string]string{}, true
	}
	ids := make([]any, len(collections))
	for i, collection := range collections {
		ids[i] = collection
	}
	var rows []dbx.NullStringMap
	if err := tx.DB().Select("table_id", "schema_revision", "typeof(schema_revision) AS schema_revision_type").From("vibetable_tables").
		Where(dbx.In("table_id", ids...)).WithContext(ctx).All(&rows); err != nil {
		return nil, false
	}
	revisions := make(map[string]string, len(rows))
	for _, row := range rows {
		var revision int64
		var err error
		switch row["schema_revision_type"].String {
		case "integer", "real":
			revision, err = schemaexecution.ParseStoredRevision(json.Number(row["schema_revision"].String), "schema.metadata.invalid_schema_revision", "schemaRevision")
		default:
			err = errPluginVersionIncomplete
		}
		if err != nil {
			return nil, false
		}
		revisions[row["table_id"].String] = v2.FormatSchemaRevision(revision)
	}
	if len(revisions) != len(collections) {
		return nil, false
	}
	return revisions, true
}

// pluginSnapshotProjectionCurrent is the authoritative page check for sources
// the fast path cannot prove: current grant, current schema revision and the
// authorization of every used field including same-row formula closure. It
// also reports whether id is currently readable, i.e. row guards may be served.
func pluginSnapshotProjectionCurrent(ctx context.Context, tx core.App, installed pluginSurfaceInstallation, active string, r pluginQueryRequest, version pluginDatasetVersion) (bool, error) {
	grant := installed.grant(r.Collection, active)
	if pluginSnapshotWildcard(grant) {
		return false, pluginDataError("plugin_read_denied")
	}
	table, err := schemaexecution.Describe(ctx, tx, r.Collection)
	if errors.Is(err, schemaexecution.ErrTableNotFound) {
		// The projection was authorized under a table that no longer exists: a
		// structural change of the corpus, not a failed query. Every other
		// describe failure stays plugin_query_failed.
		return false, pluginDataError("plugin_snapshot_stale")
	}
	if err != nil || table.Snapshot.TableID != r.Collection {
		return false, pluginDataError("plugin_query_failed")
	}
	if table.Snapshot.SchemaRevision != version.Schema {
		return false, pluginDataError("plugin_snapshot_stale")
	}
	allowed := pluginAllowed(*grant, table.Snapshot)
	authorize := pluginComputedAuthorizer(table, allowed, "plugin_query_computed_unsupported", nil)
	for _, id := range snapshotUsedFields(r) {
		if allowed[id] == "" {
			return false, pluginDataError("plugin_read_denied")
		}
		if err := authorize(id); err != nil {
			return false, err
		}
	}
	return allowed["id"] != "", nil
}
