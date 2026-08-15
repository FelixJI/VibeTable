package workspacev2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
)

func (runtime *Runtime) registerWorkspaceSearchHandlers() {
	runtime.dispatcher.Register(
		"workspaceSearch.query", protocolv2.WorkspaceScope, runtime.queryWorkspaceSearch,
	)
	runtime.dispatcher.Register(
		"workspaceSearch.resolveHit", protocolv2.WorkspaceScope, runtime.resolveWorkspaceSearchHit,
	)
	runtime.dispatcher.Register(
		"workspaceSearch.status", protocolv2.WorkspaceScope, runtime.workspaceSearchStatus,
	)
	runtime.dispatcher.Register(
		"workspaceSearch.rebuild", protocolv2.WorkspaceScope, runtime.rebuildWorkspaceSearch,
	)
	runtime.dispatcher.Register(
		"workspaceSearch.cancel", protocolv2.WorkspaceScope, runtime.cancelWorkspaceSearch,
	)
}

func (runtime *Runtime) resolveWorkspaceSearchHit(
	ctx context.Context, _ json.RawMessage, paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[contracts.SearchResolveRequest](paramsRaw)
	if err != nil || !validSearchResolveRequest(params) {
		return nil, errors.New("workspace_search.request_invalid")
	}
	sources, changes, err := runtime.authoritativeSearchSourcesForHit(ctx, params.Hit)
	if err != nil {
		return nil, publicSearchError(err)
	}
	candidate, found := resolveSourceCandidate(params, sources)
	if !found {
		if err := runtime.refreshResolvedSearchProjection(ctx, changes); err != nil {
			return nil, publicSearchError(err)
		}
		return nil, errors.New("workspace_search.hit_missing")
	}
	resolved := workspacesearch.HitFromSource(candidate)
	if authoritativeHitEqual(params.Hit, resolved) {
		return contracts.SearchResolveResult{Status: "current", Hit: params.Hit}, nil
	}
	if err := runtime.refreshResolvedSearchProjection(ctx, changes); err != nil {
		return nil, publicSearchError(err)
	}
	return contracts.SearchResolveResult{Status: "stale", Hit: resolved}, nil
}

func validSearchResolveRequest(params contracts.SearchResolveRequest) bool {
	hit, target := params.Hit, params.Hit.OpenTarget
	if params.ContractVersion != workspacesearch.ContractVersion ||
		(params.Scope != "current" && params.Scope != "history") ||
		hit.ContractVersion != workspacesearch.ContractVersion || hit.Kind != target.Kind {
		return false
	}
	switch hit.Kind {
	case "record":
		return target.TableId != nil && target.RecordId != nil &&
			target.FieldId == nil && target.DocumentId == nil &&
			hit.CanonicalId == *target.TableId+":"+*target.RecordId
	case "attachment":
		return target.TableId != nil && target.RecordId != nil && target.FieldId != nil &&
			target.DocumentId == nil && strings.HasPrefix(
			hit.CanonicalId,
			strings.Join([]string{*target.TableId, *target.RecordId, *target.FieldId}, ":")+":",
		)
	case "file":
		return target.DocumentId != nil && target.TableId == nil &&
			target.RecordId == nil && target.FieldId == nil &&
			hit.CanonicalId == *target.DocumentId
	default:
		return false
	}
}

func (runtime *Runtime) authoritativeSearchSourcesForHit(
	ctx context.Context,
	hit contracts.SearchHit,
) ([]workspacesearch.SourceDocument, workspacesearch.ProjectionChanges, error) {
	target := hit.OpenTarget
	if hit.Kind == "record" || hit.Kind == "attachment" {
		tableID, recordID := *target.TableId, *target.RecordId
		sources, err := runtime.collectRecordSearchSources(ctx, tableID, recordID)
		return sources, workspacesearch.ProjectionChanges{
			Records: []workspacesearch.RecordProjectionKey{{TableID: tableID, RecordID: recordID}},
			Sources: sources,
		}, err
	}
	documentID := *target.DocumentId
	result, err := runtime.history.QueryDocuments(filehistory.DocumentQueryRequest{
		Logic: "and",
		Filters: []filehistory.DocumentFilter{{
			Field: "documentId", Operator: "eq", Value: documentID,
		}},
		Sort:  []filehistory.DocumentSort{{Field: "relativePath", Direction: "asc"}},
		Limit: 1,
	})
	if err != nil {
		return nil, workspacesearch.ProjectionChanges{}, err
	}
	sources, err := runtime.collectFileSearchSourcesFor(
		ctx,
		result.Documents,
		map[string]struct{}{documentID: {}},
	)
	return sources, workspacesearch.ProjectionChanges{
		Documents: []string{documentID}, Sources: sources,
	}, err
}

func resolveSourceCandidate(
	params contracts.SearchResolveRequest,
	sources []workspacesearch.SourceDocument,
) (workspacesearch.SourceDocument, bool) {
	for _, source := range sources {
		if source.Kind != params.Hit.Kind || source.CanonicalID != params.Hit.CanonicalId {
			continue
		}
		if params.Scope == "history" && source.Kind == "file" {
			if source.SourceRevision == params.Hit.SourceRevision {
				return source, true
			}
			continue
		}
		if source.Current {
			return source, true
		}
	}
	return workspacesearch.SourceDocument{}, false
}

func authoritativeHitEqual(indexed contracts.SearchHit, authority contracts.SearchHit) bool {
	return indexed.HitId == authority.HitId &&
		indexed.Kind == authority.Kind &&
		indexed.CanonicalId == authority.CanonicalId &&
		indexed.Title == authority.Title &&
		indexed.SourceRevision == authority.SourceRevision &&
		indexed.RevisionTime == authority.RevisionTime &&
		reflect.DeepEqual(indexed.Metadata, authority.Metadata) &&
		reflect.DeepEqual(indexed.OpenTarget, authority.OpenTarget)
}

func (runtime *Runtime) refreshResolvedSearchProjection(
	ctx context.Context,
	changes workspacesearch.ProjectionChanges,
) error {
	checkpoint, err := runtime.search.ProjectionCheckpoint(ctx)
	if err != nil {
		return err
	}
	return runtime.search.ApplyProjectionChanges(ctx, changes, checkpoint)
}

func (runtime *Runtime) queryWorkspaceSearch(
	ctx context.Context, _ json.RawMessage, paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[contracts.SearchRequest](paramsRaw)
	if err != nil {
		return nil, errors.New("workspace_search.request_invalid")
	}
	runtime.searchMu.Lock()
	status := runtime.searchStatus
	runtime.searchMu.Unlock()
	if status.State == "degraded" || status.State == "failed" {
		if status.ErrorCode != nil {
			return nil, errors.New(*status.ErrorCode)
		}
		return nil, errors.New("workspace_search.unavailable")
	}
	result, err := runtime.search.Query(ctx, params)
	if err != nil {
		return nil, publicSearchError(err)
	}
	return result, nil
}

func (runtime *Runtime) workspaceSearchStatus(
	ctx context.Context, _ json.RawMessage, paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("workspace_search.request_invalid")
	}
	runtime.searchMu.Lock()
	status := runtime.searchStatus
	runtime.searchMu.Unlock()
	if status.State == "building" || status.State == "failed" || status.State == "degraded" {
		return status, nil
	}
	return runtime.search.Status(ctx)
}

func (runtime *Runtime) rebuildWorkspaceSearch(
	ctx context.Context, _ json.RawMessage, paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("workspace_search.request_invalid")
	}
	target, _, err := runtime.searchProjectionSourceTail(ctx)
	if err != nil {
		return nil, publicSearchError(err)
	}
	runtime.searchMu.Lock()
	runtime.searchProjectionPaused = false
	runtime.searchMu.Unlock()
	return runtime.startWorkspaceSearchRebuild(ctx, target, "manual")
}

func (runtime *Runtime) startWorkspaceSearchRebuild(
	ctx context.Context,
	target workspacesearch.ProjectionCheckpoint,
	reason string,
) (contracts.SearchStatus, error) {
	runtime.searchMu.Lock()
	if runtime.searchTaskCancel != nil {
		status := runtime.searchStatus
		runtime.searchMu.Unlock()
		return status, nil
	}
	current, err := runtime.search.Status(ctx)
	if err != nil {
		runtime.searchMu.Unlock()
		return contracts.SearchStatus{}, publicSearchError(err)
	}
	taskContext, cancel := context.WithCancel(context.Background())
	checkpoint := "collecting:" + reason
	runtime.searchStatus = contracts.SearchStatus{
		State: "building", Generation: current.Generation, Checkpoint: &checkpoint,
	}
	runtime.searchTargetCheckpoint = target
	runtime.searchTaskCancel = cancel
	status := runtime.searchStatus
	runtime.searchTaskWG.Add(1)
	runtime.searchMu.Unlock()
	go runtime.runWorkspaceSearchRebuild(taskContext)
	return status, nil
}

func (runtime *Runtime) cancelWorkspaceSearch(
	_ context.Context, _ json.RawMessage, paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("workspace_search.request_invalid")
	}
	runtime.searchMu.Lock()
	defer runtime.searchMu.Unlock()
	if runtime.searchTaskCancel != nil {
		checkpoint := "cancelling"
		runtime.searchStatus.Checkpoint = &checkpoint
		runtime.searchProjectionPaused = true
		runtime.searchTaskCancel()
	}
	return runtime.searchStatus, nil
}

func (runtime *Runtime) runWorkspaceSearchRebuild(ctx context.Context) {
	defer runtime.searchTaskWG.Done()
	defer func() {
		runtime.searchMu.Lock()
		runtime.searchTaskCancel = nil
		runtime.searchMu.Unlock()
	}()
	runtime.searchMu.Lock()
	target := runtime.searchTargetCheckpoint
	runtime.searchMu.Unlock()
	sources, err := runtime.collectSearchSources(ctx)
	if err == nil {
		total := int64(len(sources))
		checkpoint := "indexing"
		runtime.searchMu.Lock()
		runtime.searchStatus.Total = &total
		runtime.searchStatus.Checkpoint = &checkpoint
		runtime.searchMu.Unlock()
		err = runtime.search.RebuildProjection(ctx, sources, target, func(processed, total int) {
			totalValue := int64(total)
			runtime.searchMu.Lock()
			runtime.searchStatus.Processed = int64(processed)
			runtime.searchStatus.Total = &totalValue
			runtime.searchMu.Unlock()
		})
	}
	if err != nil {
		code := publicSearchError(err).Error()
		state := "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			state = "degraded"
		}
		runtime.searchMu.Lock()
		runtime.searchStatus.State = state
		runtime.searchStatus.ErrorCode = &code
		runtime.searchStatus.Checkpoint = nil
		runtime.searchProjectionPaused = true
		runtime.searchMu.Unlock()
		return
	}
	ready, err := runtime.search.Status(context.Background())
	if err != nil {
		code := "workspace_search.status_failed"
		runtime.searchMu.Lock()
		runtime.searchStatus.State = "failed"
		runtime.searchStatus.ErrorCode = &code
		runtime.searchStatus.Checkpoint = nil
		runtime.searchMu.Unlock()
		return
	}
	runtime.searchMu.Lock()
	runtime.searchStatus = ready
	runtime.searchMu.Unlock()
}

type businessOutboxBounds struct {
	Minimum int64 `db:"minimum"`
	Maximum int64 `db:"maximum"`
	Count   int64 `db:"count"`
}

type businessProjectionEventRow struct {
	RowID       int64  `db:"row_id"`
	EventID     string `db:"event_id"`
	PayloadJSON string `db:"payload_json"`
}

func (runtime *Runtime) searchProjectionSourceTail(
	ctx context.Context,
) (workspacesearch.ProjectionCheckpoint, businessOutboxBounds, error) {
	var bounds businessOutboxBounds
	if err := runtime.app.DB().NewQuery(`
		SELECT COALESCE(MIN(rowid), 0) AS minimum,
		       COALESCE(MAX(rowid), 0) AS maximum,
		       COUNT(*) AS count
		FROM vibetable_outbox
		WHERE topic = 'data.changed'
	`).WithContext(ctx).One(&bounds); err != nil {
		return workspacesearch.ProjectionCheckpoint{}, bounds, err
	}
	var fileRevision uint64
	head, found, err := runtime.headStore.Load(ctx, runtime.manifest.WorkspaceID)
	if err != nil {
		return workspacesearch.ProjectionCheckpoint{}, bounds, err
	}
	if found {
		fileRevision = head.Revision
	}
	_, counters := runtime.coordinator.Current()
	return workspacesearch.ProjectionCheckpoint{
		BusinessOutboxRowID: bounds.Maximum,
		FileHeadRevision:    fileRevision,
		MutationRevision:    counters.MutationRevision,
	}, bounds, nil
}

func (runtime *Runtime) startSearchProjectionWorker() {
	if runtime.searchProjectionCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.searchProjectionCancel = cancel
	runtime.searchProjectionWG.Add(1)
	go runtime.runSearchProjectionWorker(ctx)
}

// quiesceWorkspaceSearch cancels and joins both search owners before a restore
// invalidates the derived database or Runtime.Close closes it. Joining is the
// ownership boundary: cancellation alone does not prove an in-flight SQLite
// transaction has released its lock.
func (runtime *Runtime) quiesceWorkspaceSearch() {
	runtime.backgroundMu.Lock()
	defer runtime.backgroundMu.Unlock()

	runtime.searchMu.Lock()
	projectionCancel := runtime.searchProjectionCancel
	runtime.searchProjectionCancel = nil
	rebuildCancel := runtime.searchTaskCancel
	runtime.searchProjectionPaused = true
	runtime.searchMu.Unlock()

	if projectionCancel != nil {
		projectionCancel()
	}
	if rebuildCancel != nil {
		rebuildCancel()
	}
	runtime.searchProjectionWG.Wait()
	runtime.searchTaskWG.Wait()
	runtime.searchMu.Lock()
	runtime.searchTaskCancel = nil
	runtime.searchMu.Unlock()
}

func (runtime *Runtime) runSearchProjectionWorker(ctx context.Context) {
	defer runtime.searchProjectionWG.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := runtime.ensureSearchProjection(ctx); err != nil {
			code := publicSearchError(err).Error()
			runtime.searchMu.Lock()
			if runtime.searchTaskCancel == nil {
				runtime.searchStatus = contracts.SearchStatus{
					State: "degraded", ErrorCode: &code,
				}
			}
			runtime.searchMu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (runtime *Runtime) ensureSearchProjection(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.searchMu.Lock()
	active := runtime.searchTaskCancel != nil
	paused := runtime.searchProjectionPaused
	runtime.searchMu.Unlock()
	if active || paused {
		return nil
	}
	status, err := runtime.search.Status(ctx)
	if err != nil {
		return err
	}
	current, err := runtime.search.ProjectionCheckpoint(ctx)
	if err != nil {
		return err
	}
	target, bounds, err := runtime.searchProjectionSourceTail(ctx)
	if err != nil {
		return err
	}
	if status.Generation != 0 && current == target {
		return nil
	}
	if requiresFullSearchProjection(status, current, target, bounds) {
		_, err = runtime.startWorkspaceSearchRebuild(ctx, target, "full-rebuild")
		return err
	}
	changes, requiresFull, err := runtime.collectSearchProjectionChanges(ctx, current, target)
	if err != nil {
		return err
	}
	if requiresFull {
		_, err = runtime.startWorkspaceSearchRebuild(ctx, target, "source-contract-change")
		return err
	}
	if err := runtime.search.ApplyProjectionChanges(ctx, changes, target); err != nil {
		return err
	}
	ready, err := runtime.search.Status(ctx)
	if err != nil {
		return err
	}
	runtime.searchMu.Lock()
	runtime.searchStatus = ready
	runtime.searchMu.Unlock()
	return nil
}

func requiresFullSearchProjection(
	status contracts.SearchStatus,
	current workspacesearch.ProjectionCheckpoint,
	target workspacesearch.ProjectionCheckpoint,
	bounds businessOutboxBounds,
) bool {
	return status.State == "degraded" || status.State == "failed" ||
		status.Generation == 0 ||
		(bounds.Count > 0 && current.BusinessOutboxRowID < bounds.Minimum) ||
		target.BusinessOutboxRowID < current.BusinessOutboxRowID ||
		target.FileHeadRevision < current.FileHeadRevision ||
		target.MutationRevision < current.MutationRevision
}

func (runtime *Runtime) collectSearchProjectionChanges(
	ctx context.Context,
	current workspacesearch.ProjectionCheckpoint,
	target workspacesearch.ProjectionCheckpoint,
) (workspacesearch.ProjectionChanges, bool, error) {
	changes := workspacesearch.ProjectionChanges{}
	recordKeys := []workspacesearch.RecordProjectionKey{}
	if target.BusinessOutboxRowID != current.BusinessOutboxRowID {
		var rows []businessProjectionEventRow
		if err := runtime.app.DB().NewQuery(`
			SELECT rowid AS row_id, event_id, payload_json
			FROM vibetable_outbox
			WHERE topic = 'data.changed' AND rowid > {:after} AND rowid <= {:target}
			ORDER BY rowid ASC
		`).Bind(dbx.Params{
			"after": current.BusinessOutboxRowID, "target": target.BusinessOutboxRowID,
		}).WithContext(ctx).All(&rows); err != nil {
			return changes, false, err
		}
		if len(rows) == 0 || rows[len(rows)-1].RowID != target.BusinessOutboxRowID {
			return changes, true, nil
		}
		keys, requiresFull, err := recordProjectionKeys(ctx, rows)
		if err != nil || requiresFull {
			return changes, requiresFull, err
		}
		recordKeys = keys
	}
	for _, key := range recordKeys {
		changes.Records = append(changes.Records, key)
		sources, err := runtime.collectRecordSearchSources(ctx, key.TableID, key.RecordID)
		if err != nil {
			return changes, false, err
		}
		changes.Sources = append(changes.Sources, sources...)
	}
	if target.FileHeadRevision != current.FileHeadRevision {
		documents, sources, err := runtime.collectChangedFileSearchSources(ctx)
		if err != nil {
			return changes, false, err
		}
		changes.Documents = append(changes.Documents, documents...)
		changes.Sources = append(changes.Sources, sources...)
	}
	return changes, false, nil
}

func recordProjectionKeys(
	ctx context.Context,
	rows []businessProjectionEventRow,
) ([]workspacesearch.RecordProjectionKey, bool, error) {
	unique := map[workspacesearch.RecordProjectionKey]struct{}{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		var event mutation.DataChangedEvent
		if mutation.DecodeStrict([]byte(row.PayloadJSON), &event) != nil ||
			event.ContractVersion != mutation.ContractVersion ||
			event.Topic != "data.changed" || event.EventID != row.EventID {
			return nil, false, errors.New("workspace_search.outbox_corrupt")
		}
		if event.Operation == mutation.DataChangeSchema ||
			event.TableID == "metadata:content_profiles" ||
			!validIncrementalRecordEvent(event) {
			return nil, true, nil
		}
		for _, recordID := range event.RecordIDs {
			unique[workspacesearch.RecordProjectionKey{
				TableID: event.TableID, RecordID: recordID,
			}] = struct{}{}
		}
	}
	result := make([]workspacesearch.RecordProjectionKey, 0, len(unique))
	for key := range unique {
		result = append(result, key)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].TableID == result[right].TableID {
			return result[left].RecordID < result[right].RecordID
		}
		return result[left].TableID < result[right].TableID
	})
	return result, false, nil
}

func validIncrementalRecordEvent(event mutation.DataChangedEvent) bool {
	if strings.TrimSpace(event.TableID) == "" ||
		strings.HasPrefix(event.TableID, "metadata:") || len(event.RecordIDs) == 0 {
		return false
	}
	for _, recordID := range event.RecordIDs {
		if strings.TrimSpace(recordID) == "" {
			return false
		}
	}
	return true
}

func (runtime *Runtime) collectSearchSources(ctx context.Context) ([]workspacesearch.SourceDocument, error) {
	sources := make([]workspacesearch.SourceDocument, 0)
	records, err := runtime.collectContentProfileSources(ctx)
	if err != nil {
		return nil, err
	}
	sources = append(sources, records...)
	attachments, err := runtime.collectAttachmentSearchSources(ctx)
	if err != nil {
		return nil, err
	}
	sources = append(sources, attachments...)
	files, err := runtime.collectFileSearchSources(ctx)
	if err != nil {
		return nil, err
	}
	sources = append(sources, files...)
	return sources, nil
}

func (runtime *Runtime) collectContentProfileSources(ctx context.Context) ([]workspacesearch.SourceDocument, error) {
	profiles, err := runtime.app.FindRecordsByFilter("vibetable_content_profiles", "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	sources := make([]workspacesearch.SourceDocument, 0)
	for _, profileRecord := range profiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var profile contracts.ContentProfile
		raw, err := json.Marshal(profileRecord.GetRaw("payload_json"))
		if err != nil || json.Unmarshal(raw, &profile) != nil {
			return nil, errors.New("workspace_search.profile_invalid")
		}
		tableMetadata, err := runtime.app.FindFirstRecordByFilter(
			"vibetable_tables", "table_id={:table}", dbx.Params{"table": profile.TableId},
		)
		if err != nil {
			return nil, err
		}
		physicalTable := tableMetadata.GetString("physical_name")
		fieldRecords, err := runtime.app.FindRecordsByFilter(
			"vibetable_fields", "table_id={:table}", "", 0, 0,
			dbx.Params{"table": profile.TableId},
		)
		if err != nil {
			return nil, err
		}
		physicalByID := make(map[string]string, len(fieldRecords))
		for _, field := range fieldRecords {
			physicalByID[field.GetString("field_id")] = field.GetString("physical_name")
		}
		rows, err := runtime.app.FindRecordsByFilter(physicalTable, "", "", 0, 0)
		if err != nil {
			return nil, err
		}
		if len(rows) > 100_000 {
			return nil, errors.New("workspace_search.record_capacity_exceeded")
		}
		for _, row := range rows {
			sources = append(sources, recordSearchSource(profile, physicalByID, row))
		}
	}
	return sources, nil
}

func recordSearchSource(
	profile contracts.ContentProfile,
	physicalByID map[string]string,
	row *core.Record,
) workspacesearch.SourceDocument {
	values := make([]string, 0, len(profile.SearchableFieldIds))
	for _, fieldID := range profile.SearchableFieldIds {
		values = append(values, searchableValue(row.GetRaw(physicalByID[fieldID])))
	}
	title := searchableValue(row.GetRaw(physicalByID[profile.TitleFieldId]))
	if title == "" {
		title = row.Id
	}
	tableID, recordID := profile.TableId, row.Id
	return workspacesearch.SourceDocument{
		Kind: "record", CanonicalID: profile.TableId + ":" + row.Id,
		Title: title, Body: strings.Join(values, "\n"),
		SourceRevision: stableSourceRevision(values, row.Id),
		RevisionTime:   recordTime(row), TableID: &tableID, RecordID: &recordID,
		Status: "active", Current: true,
		Metadata:   []contracts.SearchMetadataItem{{Key: "tableId", Value: profile.TableId}},
		OpenTarget: contracts.SearchOpenTarget{Kind: "record", TableId: &tableID, RecordId: &recordID},
	}
}

func (runtime *Runtime) collectRecordSearchSources(
	ctx context.Context,
	tableID string,
	recordID string,
) ([]workspacesearch.SourceDocument, error) {
	profiles, err := runtime.app.FindRecordsByFilter(
		"vibetable_content_profiles", "", "", 0, 0,
	)
	if err != nil {
		return nil, err
	}
	var selected *contracts.ContentProfile
	for _, profileRecord := range profiles {
		var profile contracts.ContentProfile
		raw, marshalErr := json.Marshal(profileRecord.GetRaw("payload_json"))
		if marshalErr != nil || json.Unmarshal(raw, &profile) != nil {
			return nil, errors.New("workspace_search.profile_invalid")
		}
		if profile.TableId == tableID {
			selected = &profile
			break
		}
	}
	sources := make([]workspacesearch.SourceDocument, 0)
	if selected != nil {
		tableMetadata, findErr := runtime.app.FindFirstRecordByFilter(
			"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
		)
		if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
			return nil, findErr
		}
		if findErr == nil {
			fieldRecords, fieldErr := runtime.app.FindRecordsByFilter(
				"vibetable_fields", "table_id={:table}", "", 0, 0,
				dbx.Params{"table": tableID},
			)
			if fieldErr != nil {
				return nil, fieldErr
			}
			physicalByID := make(map[string]string, len(fieldRecords))
			for _, field := range fieldRecords {
				physicalByID[field.GetString("field_id")] = field.GetString("physical_name")
			}
			row, rowErr := runtime.app.FindRecordById(
				tableMetadata.GetString("physical_name"), recordID,
			)
			if rowErr != nil && !errors.Is(rowErr, sql.ErrNoRows) {
				return nil, rowErr
			}
			if rowErr == nil {
				sources = append(sources, recordSearchSource(*selected, physicalByID, row))
			}
		}
	}
	attachments, err := runtime.collectAttachmentSearchSourcesForRecord(ctx, tableID, recordID)
	if err != nil {
		return nil, err
	}
	return append(sources, attachments...), nil
}

func (runtime *Runtime) collectAttachmentSearchSources(ctx context.Context) ([]workspacesearch.SourceDocument, error) {
	records, err := runtime.app.FindRecordsByFilter("vibetable_attachment_meta", "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	sources := make([]workspacesearch.SourceDocument, 0, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sources = append(sources, runtime.attachmentSearchSource(ctx, record))
	}
	return sources, nil
}

func (runtime *Runtime) collectAttachmentSearchSourcesForRecord(
	ctx context.Context,
	tableID string,
	recordID string,
) ([]workspacesearch.SourceDocument, error) {
	records, err := runtime.app.FindRecordsByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && record_id={:record}", "", 0, 0,
		dbx.Params{"table": tableID, "record": recordID},
	)
	if err != nil {
		return nil, err
	}
	sources := make([]workspacesearch.SourceDocument, 0, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sources = append(sources, runtime.attachmentSearchSource(ctx, record))
	}
	return sources, nil
}

func (runtime *Runtime) attachmentSearchSource(
	ctx context.Context,
	record *core.Record,
) workspacesearch.SourceDocument {
	tableID, recordID, fieldID := record.GetString("table_id"), record.GetString("record_id"), record.GetString("field_id")
	name, mimeType := record.GetString("original_name"), record.GetString("mime")
	storedName := record.GetString("stored_name")
	size := int64(record.GetFloat("size"))
	canonical := strings.Join([]string{tableID, recordID, fieldID, storedName}, ":")
	body := strings.Join([]string{name, mimeType, fieldID}, " ")
	status := "unsupported"
	download, openErr := attachments.OpenForIndex(
		ctx, runtime.app, tableID, recordID, fieldID, storedName,
	)
	if openErr == nil {
		extraction := workspacesearch.Extract(
			ctx, name, mimeType, download.Reader, workspacesearch.DefaultExtractionLimits,
		)
		_ = download.Reader.Close()
		status = string(extraction.Status)
		if extraction.Status == workspacesearch.ExtractionIndexed ||
			extraction.Status == workspacesearch.ExtractionTruncated {
			body += "\n" + extraction.Text
		}
	} else {
		status = "failed"
	}
	return workspacesearch.SourceDocument{
		Kind: "attachment", CanonicalID: canonical, Title: name,
		Body:           body,
		SourceRevision: record.GetString("hash"), RevisionTime: recordTime(record),
		TableID: &tableID, RecordID: &recordID, FieldID: &fieldID,
		MIMEType: &mimeType, SizeBytes: &size, Status: status, Current: true,
		Metadata: []contracts.SearchMetadataItem{
			{Key: "storedName", Value: storedName}, {Key: "extractionStatus", Value: status},
		},
		OpenTarget: contracts.SearchOpenTarget{Kind: "attachment", TableId: &tableID, RecordId: &recordID, FieldId: &fieldID},
	}
}

func (runtime *Runtime) collectFileSearchSources(ctx context.Context) ([]workspacesearch.SourceDocument, error) {
	summaries, err := runtime.queryAllFileSummaries(ctx)
	if err != nil {
		return nil, err
	}
	return runtime.collectFileSearchSourcesFor(ctx, summaries, nil)
}

func (runtime *Runtime) queryAllFileSummaries(
	ctx context.Context,
) ([]filehistory.FileDocumentSummary, error) {
	result, err := runtime.history.QueryDocuments(filehistory.DocumentQueryRequest{
		Logic: "and", Filters: []filehistory.DocumentFilter{},
		Sort: []filehistory.DocumentSort{{Field: "relativePath", Direction: "asc"}}, Limit: 500,
	})
	if err != nil {
		return nil, err
	}
	summaries := result.Documents
	for result.NextCursor != nil {
		if len(summaries) >= 10_000 {
			return nil, errors.New("workspace_search.file_capacity_exceeded")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err = runtime.history.QueryDocuments(filehistory.DocumentQueryRequest{
			Logic: "and", Filters: []filehistory.DocumentFilter{},
			Sort: []filehistory.DocumentSort{{Field: "relativePath", Direction: "asc"}}, Limit: 500,
			Cursor: result.NextCursor,
		})
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, result.Documents...)
	}
	return summaries, nil
}

func (runtime *Runtime) collectFileSearchSourcesFor(
	ctx context.Context,
	summaries []filehistory.FileDocumentSummary,
	selected map[string]struct{},
) ([]workspacesearch.SourceDocument, error) {
	sources := make([]workspacesearch.SourceDocument, 0, len(summaries))
	for _, summary := range summaries {
		if selected != nil {
			if _, found := selected[summary.DocumentID]; !found {
				continue
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		documentID := summary.DocumentID
		document, inspectErr := runtime.history.Inspect(summary.DocumentID)
		if inspectErr != nil {
			return nil, inspectErr
		}
		for _, revision := range document.Revisions {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			mimeType, size := revision.MimeType, revision.Size
			status := "failed"
			body := ""
			_, content, openErr := runtime.history.OpenRevision(ctx, summary.DocumentID, revision.RevisionID)
			if openErr == nil {
				extraction := workspacesearch.Extract(
					ctx, summary.RelativePath, mimeType, content,
					workspacesearch.DefaultExtractionLimits,
				)
				_ = content.Close()
				status = string(extraction.Status)
				if extraction.Status == workspacesearch.ExtractionIndexed ||
					extraction.Status == workspacesearch.ExtractionTruncated {
					body = extraction.Text
				}
			}
			if summary.Status == filehistory.DocumentDeleted {
				status = "deleted"
			}
			metadata := []contracts.SearchMetadataItem{
				{Key: "relativePath", Value: summary.RelativePath},
				{Key: "documentStatus", Value: string(summary.Status)},
				{Key: "extractionStatus", Value: status},
			}
			if revision.FormalVersion != nil {
				metadata = append(metadata, contracts.SearchMetadataItem{
					Key: "formalVersion", Value: float64(*revision.FormalVersion),
				})
			}
			sources = append(sources, workspacesearch.SourceDocument{
				Kind: "file", CanonicalID: summary.DocumentID, Title: summary.DisplayName,
				Body: body, SourceRevision: revision.RevisionID,
				RevisionTime: revision.CreatedAt.UTC().Format(time.RFC3339Nano),
				DocumentID:   &documentID, MIMEType: &mimeType,
				Extension: &summary.Extension, SizeBytes: &size,
				Status: status, Current: revision.RevisionID == summary.EffectiveRevisionID,
				Metadata:   metadata,
				OpenTarget: contracts.SearchOpenTarget{Kind: "file", DocumentId: &documentID},
			})
		}
	}
	return sources, nil
}

func (runtime *Runtime) collectChangedFileSearchSources(
	ctx context.Context,
) ([]string, []workspacesearch.SourceDocument, error) {
	indexed, err := runtime.search.CurrentFileProjectionStates(ctx)
	if err != nil {
		return nil, nil, err
	}
	summaries, err := runtime.queryAllFileSummaries(ctx)
	if err != nil {
		return nil, nil, err
	}
	selected := make(map[string]struct{})
	for _, summary := range summaries {
		state, found := indexed[summary.DocumentID]
		if !found || !sameFileProjectionState(state, summary) {
			selected[summary.DocumentID] = struct{}{}
		}
		delete(indexed, summary.DocumentID)
	}
	for documentID := range indexed {
		selected[documentID] = struct{}{}
	}
	documents := make([]string, 0, len(selected))
	for documentID := range selected {
		documents = append(documents, documentID)
	}
	sort.Strings(documents)
	sources, err := runtime.collectFileSearchSourcesFor(ctx, summaries, selected)
	if err != nil {
		return nil, nil, err
	}
	return documents, sources, nil
}

func sameFileProjectionState(
	state workspacesearch.FileProjectionState,
	summary filehistory.FileDocumentSummary,
) bool {
	return state.SourceRevision == summary.EffectiveRevisionID &&
		state.Title == summary.DisplayName &&
		state.RelativePath == summary.RelativePath &&
		state.DocumentStatus == string(summary.Status) &&
		state.MIMEType == summary.MimeType &&
		state.Extension == summary.Extension &&
		state.SizeBytes == summary.SizeBytes
}

func publicSearchError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("workspace_search.cancelled")
	}
	return errors.New(workspacesearch.PublicErrorCode(err))
}

func searchableValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func stableSourceRevision(values []string, identity string) string {
	digest := sha256.Sum256([]byte(identity + "\x00" + strings.Join(values, "\x00")))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func recordTime(record *core.Record) string {
	value := record.GetDateTime("updated")
	if !value.IsZero() {
		return value.Time().UTC().Format(time.RFC3339Nano)
	}
	return time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
}
