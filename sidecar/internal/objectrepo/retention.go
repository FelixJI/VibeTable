package objectrepo

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	kopiarepo "github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob"
	kopiacontent "github.com/kopia/kopia/repo/content"
	"github.com/kopia/kopia/repo/maintenance"
	kopiamanifest "github.com/kopia/kopia/repo/manifest"
	kopiaobject "github.com/kopia/kopia/repo/object"
)

const (
	retentionJournalManifestType = "vibetable-retention-journal-v2"
	retentionJournalFormat       = 2
)

type RetentionObject struct {
	ID   ObjectID
	Size int64
}

type RetentionInventory struct {
	Revision             uint64
	Objects              []RetentionObject
	CompletedRetirements []RetentionObject
	Pins                 []RootPin
	PendingPublication   bool
	UnknownManifest      bool
	CorruptIndex         bool
}

type RetentionInventorySource interface {
	RetentionInventory(context.Context) (RetentionInventory, error)
}

type RetentionMaintenanceRequest struct {
	Authority        Authority
	ExpectedRevision uint64
	ObjectIDs        []ObjectID
}

type RetentionMaintenanceResult struct {
	DeletedObjects  int
	ReclaimedBytes  int64
	BeforeRevision  uint64
	AfterRevision   uint64
	VerificationRun bool
}

type RetentionMaintainer interface {
	RetireAndMaintain(
		context.Context,
		RetentionMaintenanceRequest,
	) (RetentionMaintenanceResult, error)
}

type retentionJournal struct {
	FormatVersion    int                `json:"formatVersion"`
	JournalID        string             `json:"journalId"`
	WorkspaceID      string             `json:"workspaceId"`
	ExpectedRevision uint64             `json:"expectedRevision"`
	Stage            string             `json:"stage"`
	Objects          []retirementObject `json:"objects"`
	CreatedAt        time.Time          `json:"createdAt"`
}

type retirementObject struct {
	PublicID   ObjectID `json:"publicId"`
	InternalID string   `json:"internalId"`
	Size       int64    `json:"size"`
	ContentIDs []string `json:"contentIds"`
}

type retentionFaultStage string

const (
	retentionBeforeJournal       retentionFaultStage = "before-journal"
	retentionAfterJournal        retentionFaultStage = "after-journal"
	retentionAfterMappingRemoval retentionFaultStage = "after-mapping-removal"
	retentionBeforeContentDelete retentionFaultStage = "before-content-delete"
	retentionAfterContentDelete  retentionFaultStage = "after-content-delete"
	retentionAfterContentFlush   retentionFaultStage = "after-content-flush"
)

type retentionFaultKey struct{}

// withRetentionFault is deliberately package-private: production callers
// cannot weaken retention, while objectrepo fault tests can stop at every
// durable boundary and prove restart replay.
func withRetentionFault(
	ctx context.Context,
	hook func(retentionFaultStage) error,
) context.Context {
	return context.WithValue(ctx, retentionFaultKey{}, hook)
}

func injectRetentionFault(
	ctx context.Context,
	stage retentionFaultStage,
) error {
	hook, _ := ctx.Value(retentionFaultKey{}).(func(retentionFaultStage) error)
	if hook == nil {
		return nil
	}
	return hook(stage)
}

func (repository *KopiaRepository) RetentionInventory(
	ctx context.Context,
) (RetentionInventory, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath)
	if err != nil {
		return RetentionInventory{}, err
	}
	defer release()
	if err := repository.loadState(ctx); err != nil {
		return RetentionInventory{
			CorruptIndex: true,
		}, nil
	}
	direct, ok := repository.repository.(kopiarepo.DirectRepository)
	if !ok {
		return RetentionInventory{}, errors.New(
			"repository.retention_direct_reader_required",
		)
	}
	active, err := direct.ContentReader().ListActiveSessions(ctx)
	if err != nil {
		return RetentionInventory{CorruptIndex: true}, nil
	}
	journals, journalEntries, err := repository.retentionJournals(ctx)
	if err != nil {
		return RetentionInventory{CorruptIndex: true}, nil
	}
	for _, journal := range journals {
		if repository.state.Authority == nil ||
			journal.WorkspaceID != repository.state.Authority.WorkspaceID {
			return RetentionInventory{CorruptIndex: true}, nil
		}
	}
	result := RetentionInventory{
		Revision:           repository.state.Revision,
		Pins:               append([]RootPin(nil), repository.state.Pins...),
		PendingPublication: len(active) != 0,
	}
	publicEntries, err := repository.repository.FindManifests(
		ctx,
		map[string]string{"type": "vibetable-manifest"},
	)
	if err != nil {
		result.CorruptIndex = true
		return result, nil
	}
	expectedManifests := make(map[string]string, len(repository.state.Manifests))
	for publicID, internalID := range repository.state.Manifests {
		expectedManifests[internalID] = publicID
	}
	for _, entry := range publicEntries {
		publicID, expected := expectedManifests[string(entry.ID)]
		if !expected ||
			entry.Labels["vibetable.publicId"] != publicID {
			result.UnknownManifest = true
		}
		delete(expectedManifests, string(entry.ID))
	}
	if len(expectedManifests) != 0 {
		result.UnknownManifest = true
	}
	allJournalEntries, err := repository.repository.FindManifests(
		ctx,
		map[string]string{"type": retentionJournalManifestType},
	)
	if err != nil {
		result.CorruptIndex = true
		return result, nil
	}
	if len(allJournalEntries) != len(journalEntries) {
		result.UnknownManifest = true
	}

	objects := make(map[ObjectID]retirementObject, len(repository.state.Objects))
	for publicID, internalID := range repository.state.Objects {
		objects[ObjectID(publicID)] = retirementObject{
			PublicID: ObjectID(publicID), InternalID: internalID,
		}
	}
	for _, journal := range journals {
		if journal.Stage == "completed" {
			for _, item := range journal.Objects {
				result.CompletedRetirements = append(
					result.CompletedRetirements,
					RetentionObject{ID: item.PublicID, Size: item.Size},
				)
			}
			continue
		}
		for _, item := range journal.Objects {
			if existing, found := objects[item.PublicID]; found &&
				existing.InternalID != item.InternalID {
				result.CorruptIndex = true
				return result, nil
			}
			objects[item.PublicID] = item
		}
	}
	for id, item := range objects {
		internalID, parseErr := kopiaobject.ParseID(item.InternalID)
		if parseErr != nil {
			result.CorruptIndex = true
			continue
		}
		reader, openErr := repository.repository.OpenObject(ctx, internalID)
		if openErr != nil {
			// A replayable retirement journal may legitimately point at
			// contents already deleted before a crash. Its persisted size is
			// still sufficient to keep the logical tombstone visible.
			if item.Size > 0 && journalContains(journals, id) {
				result.Objects = append(result.Objects, RetentionObject{
					ID: id, Size: item.Size,
				})
				continue
			}
			result.CorruptIndex = true
			continue
		}
		size := reader.Length()
		closeErr := reader.Close()
		if size < 0 || closeErr != nil {
			result.CorruptIndex = true
			continue
		}
		if _, verifyErr := repository.repository.VerifyObject(
			ctx,
			internalID,
		); verifyErr != nil {
			result.CorruptIndex = true
			continue
		}
		result.Objects = append(result.Objects, RetentionObject{
			ID: id, Size: size,
		})
	}
	sort.Slice(result.Objects, func(left, right int) bool {
		return result.Objects[left].ID < result.Objects[right].ID
	})
	sort.Slice(result.CompletedRetirements, func(left, right int) bool {
		return result.CompletedRetirements[left].ID <
			result.CompletedRetirements[right].ID
	})
	sort.Slice(result.Pins, func(left, right int) bool {
		return result.Pins[left].PinID < result.Pins[right].PinID
	})
	return result, nil
}

func (repository *KopiaRepository) RetireAndMaintain(
	ctx context.Context,
	request RetentionMaintenanceRequest,
) (RetentionMaintenanceResult, error) {
	if err := validateAuthority(request.Authority); err != nil {
		return RetentionMaintenanceResult{}, err
	}
	ids, err := normalizeRetentionIDs(request.ObjectIDs)
	if err != nil {
		return RetentionMaintenanceResult{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath)
	if err != nil {
		return RetentionMaintenanceResult{}, err
	}
	defer release()
	if err := repository.loadState(ctx); err != nil {
		return RetentionMaintenanceResult{}, err
	}
	if err := repository.requireAuthority(request.Authority); err != nil {
		return RetentionMaintenanceResult{}, err
	}
	if repository.state.Revision != request.ExpectedRevision {
		return RetentionMaintenanceResult{}, errors.New(
			"retention.inventory_changed",
		)
	}
	direct, ok := repository.repository.(kopiarepo.DirectRepository)
	if !ok {
		return RetentionMaintenanceResult{}, errors.New(
			"repository.retention_direct_writer_required",
		)
	}
	active, err := direct.ContentReader().ListActiveSessions(ctx)
	if err != nil || len(active) != 0 {
		return RetentionMaintenanceResult{}, errors.New(
			"retention.pending_publication",
		)
	}
	beforeBytes, err := repositoryBlobBytes(ctx, direct)
	if err != nil {
		return RetentionMaintenanceResult{}, err
	}
	journals, _, err := repository.retentionJournals(ctx)
	if err != nil {
		return RetentionMaintenanceResult{}, err
	}
	journal, found, err := selectRetentionJournal(journals, ids)
	if err != nil {
		return RetentionMaintenanceResult{}, err
	}
	if !found {
		journal, err = repository.prepareRetentionJournal(
			ctx,
			direct,
			request,
			ids,
		)
		if err != nil {
			return RetentionMaintenanceResult{}, err
		}
	}
	if err := repository.advanceRetentionJournal(
		ctx,
		direct,
		&journal,
	); err != nil {
		return RetentionMaintenanceResult{}, err
	}
	afterBytes, err := repositoryBlobBytes(ctx, direct)
	if err != nil {
		return RetentionMaintenanceResult{}, err
	}
	reclaimed := int64(0)
	if beforeBytes > afterBytes {
		reclaimed = beforeBytes - afterBytes
	}
	return RetentionMaintenanceResult{
		DeletedObjects:  len(ids),
		ReclaimedBytes:  reclaimed,
		BeforeRevision:  request.ExpectedRevision,
		AfterRevision:   repository.state.Revision,
		VerificationRun: true,
	}, nil
}

func (repository *KopiaRepository) prepareRetentionJournal(
	ctx context.Context,
	direct kopiarepo.DirectRepository,
	request RetentionMaintenanceRequest,
	ids []ObjectID,
) (retentionJournal, error) {
	if err := injectRetentionFault(ctx, retentionBeforeJournal); err != nil {
		return retentionJournal{}, err
	}
	targets := make(map[ObjectID]struct{}, len(ids))
	for _, id := range ids {
		targets[id] = struct{}{}
	}
	journal := retentionJournal{
		FormatVersion:    retentionJournalFormat,
		JournalID:        uuid.NewString(),
		WorkspaceID:      request.Authority.WorkspaceID,
		ExpectedRevision: request.ExpectedRevision,
		Stage:            "prepared",
		CreatedAt:        repository.now(),
	}
	for _, id := range ids {
		internal, found := repository.state.Objects[string(id)]
		if !found {
			return retentionJournal{}, errors.New(
				"retention.inventory_changed",
			)
		}
		parsed, err := kopiaobject.ParseID(internal)
		if err != nil {
			return retentionJournal{}, ErrCorrupt
		}
		reader, err := repository.repository.OpenObject(ctx, parsed)
		if err != nil {
			return retentionJournal{}, errors.Join(ErrCorrupt, err)
		}
		size := reader.Length()
		closeErr := reader.Close()
		if size < 0 || closeErr != nil {
			return retentionJournal{}, errors.Join(ErrCorrupt, closeErr)
		}
		contentIDs, err := repository.repository.VerifyObject(ctx, parsed)
		if err != nil {
			return retentionJournal{}, errors.Join(ErrCorrupt, err)
		}
		item := retirementObject{
			PublicID: id, InternalID: internal, Size: size,
			ContentIDs: contentIDStrings(contentIDs),
		}
		journal.Objects = append(journal.Objects, item)
	}
	if err := repository.verifyNoSharedLiveContent(
		ctx,
		journal,
		targets,
	); err != nil {
		return retentionJournal{}, err
	}
	if err := repository.writeRetentionJournal(ctx, direct, journal); err != nil {
		return retentionJournal{}, err
	}
	if err := injectRetentionFault(ctx, retentionAfterJournal); err != nil {
		return retentionJournal{}, err
	}
	return journal, nil
}

func (repository *KopiaRepository) advanceRetentionJournal(
	ctx context.Context,
	direct kopiarepo.DirectRepository,
	journal *retentionJournal,
) error {
	targets := make(map[ObjectID]struct{}, len(journal.Objects))
	for _, item := range journal.Objects {
		targets[item.PublicID] = struct{}{}
	}
	if journal.Stage == "prepared" {
		next := cloneKopiaState(repository.state)
		for _, item := range journal.Objects {
			internal, found := next.Objects[string(item.PublicID)]
			if found && internal != item.InternalID {
				return errors.New("retention.inventory_changed")
			}
			delete(next.Objects, string(item.PublicID))
		}
		next.Revision++
		journal.Stage = "mapping-removed"
		sessionCtx, writer, err := direct.NewDirectWriter(
			ctx,
			kopiarepo.WriteSessionOptions{
				Purpose: "VibeTable retention mapping removal",
			},
		)
		if err != nil {
			return err
		}
		if err := putKopiaState(sessionCtx, writer, next); err != nil {
			return err
		}
		if err := putRetentionJournal(sessionCtx, writer, *journal); err != nil {
			return err
		}
		if err := writer.Flush(sessionCtx); err != nil {
			return err
		}
		repository.state = next
		if err := injectRetentionFault(
			ctx,
			retentionAfterMappingRemoval,
		); err != nil {
			return err
		}
	}
	if journal.Stage == "mapping-removed" {
		if err := repository.verifyNoSharedLiveContent(
			ctx,
			*journal,
			targets,
		); err != nil {
			return err
		}
		if err := injectRetentionFault(
			ctx,
			retentionBeforeContentDelete,
		); err != nil {
			return err
		}
		sessionCtx, writer, err := direct.NewDirectWriter(
			ctx,
			kopiarepo.WriteSessionOptions{
				Purpose: "VibeTable retention content retirement",
			},
		)
		if err != nil {
			return err
		}
		for _, item := range journal.Objects {
			for _, rawID := range item.ContentIDs {
				contentID, parseErr := kopiacontent.ParseID(rawID)
				if parseErr != nil {
					return ErrCorrupt
				}
				info, infoErr := writer.ContentManager().ContentInfo(
					sessionCtx,
					contentID,
				)
				if infoErr == nil && !info.Deleted {
					if err := writer.ContentManager().DeleteContent(
						sessionCtx,
						contentID,
					); err != nil {
						return err
					}
				}
			}
		}
		// Make the delete markers durable before the next crash boundary.
		// A raw DeleteContent call can leave an active Kopia write session;
		// abandoning it would correctly block all future destructive work as
		// an unknown pending publication. Flushing the content manager first
		// makes this boundary both crash-testable and replayable.
		if err := writer.ContentManager().Flush(sessionCtx); err != nil {
			return err
		}
		if err := injectRetentionFault(
			ctx,
			retentionAfterContentDelete,
		); err != nil {
			return err
		}
		if err := writer.Flush(sessionCtx); err != nil {
			return err
		}
		if err := injectRetentionFault(
			ctx,
			retentionAfterContentFlush,
		); err != nil {
			return err
		}
		journal.Stage = "content-deleted"
		if err := repository.writeRetentionJournal(
			ctx,
			direct,
			*journal,
		); err != nil {
			return err
		}
	}
	if journal.Stage != "content-deleted" &&
		journal.Stage != "completed" {
		return errors.New("retention.journal_stage_invalid")
	}
	if journal.Stage == "content-deleted" {
		if err := repository.runFullMaintenance(ctx, direct); err != nil {
			return err
		}
	}
	if err := repository.verifyRetentionClosure(
		ctx,
		direct,
		*journal,
	); err != nil {
		return err
	}
	sessionCtx, writer, err := direct.NewDirectWriter(
		ctx,
		kopiarepo.WriteSessionOptions{
			Purpose: "VibeTable retention journal completion",
		},
	)
	if err != nil {
		return err
	}
	// Completing a retirement is itself an inventory publication.  This
	// additional revision is essential when a process restarts after the
	// public mapping was removed: the replay request is bound to that current
	// revision and must still return a strictly newer verified inventory.
	next := cloneKopiaState(repository.state)
	next.Revision++
	if journal.Stage != "completed" {
		journal.Stage = "completed"
		if err := putRetentionJournal(sessionCtx, writer, *journal); err != nil {
			return err
		}
	}
	if err := putKopiaState(sessionCtx, writer, next); err != nil {
		return err
	}
	if err := writer.Flush(sessionCtx); err != nil {
		return err
	}
	repository.state = next
	return nil
}

func (repository *KopiaRepository) verifyNoSharedLiveContent(
	ctx context.Context,
	journal retentionJournal,
	targets map[ObjectID]struct{},
) error {
	retiredContents := map[string]struct{}{}
	for _, item := range journal.Objects {
		for _, id := range item.ContentIDs {
			retiredContents[id] = struct{}{}
		}
	}
	for rawPublicID, rawInternalID := range repository.state.Objects {
		if _, retired := targets[ObjectID(rawPublicID)]; retired {
			continue
		}
		internalID, err := kopiaobject.ParseID(rawInternalID)
		if err != nil {
			return ErrCorrupt
		}
		contentIDs, err := repository.repository.VerifyObject(ctx, internalID)
		if err != nil {
			return errors.Join(ErrCorrupt, err)
		}
		for _, id := range contentIDs {
			if _, shared := retiredContents[id.String()]; shared {
				delete(retiredContents, id.String())
			}
		}
	}
	// Remove shared contents from each journal object. This mutation is
	// persisted before mappings are removed, so replay cannot later delete a
	// backing content still used by a live deduplicated object.
	for index := range journal.Objects {
		filtered := journal.Objects[index].ContentIDs[:0]
		for _, id := range journal.Objects[index].ContentIDs {
			if _, exclusive := retiredContents[id]; exclusive {
				filtered = append(filtered, id)
			}
		}
		journal.Objects[index].ContentIDs = filtered
	}
	return nil
}

func (repository *KopiaRepository) runFullMaintenance(
	ctx context.Context,
	direct kopiarepo.DirectRepository,
) error {
	sessionCtx, writer, err := direct.NewDirectWriter(
		ctx,
		kopiarepo.WriteSessionOptions{
			Purpose: "VibeTable retention full maintenance",
		},
	)
	if err != nil {
		return err
	}
	params, err := maintenance.GetParams(sessionCtx, writer)
	if err != nil {
		return err
	}
	owner := writer.ClientOptions().UsernameAtHost()
	if params.Owner == "" {
		params.Owner = owner
		if err := maintenance.SetParams(sessionCtx, writer, params); err != nil {
			return err
		}
		if err := writer.Flush(sessionCtx); err != nil {
			return err
		}
	} else if params.Owner != owner {
		return errors.New("retention.maintenance_owner_mismatch")
	}
	return maintenance.RunExclusive(
		sessionCtx,
		writer,
		maintenance.ModeFull,
		true,
		func(
			runCtx context.Context,
			parameters maintenance.RunParameters,
		) error {
			return maintenance.Run(
				runCtx,
				parameters,
				maintenance.SafetyFull,
			)
		},
	)
}

func (repository *KopiaRepository) verifyRetentionClosure(
	ctx context.Context,
	direct kopiarepo.DirectRepository,
	journal retentionJournal,
) error {
	if err := repository.repository.Refresh(ctx); err != nil {
		return errors.Join(ErrCorrupt, err)
	}
	for _, rawInternalID := range repository.state.Objects {
		internalID, err := kopiaobject.ParseID(rawInternalID)
		if err != nil {
			return ErrCorrupt
		}
		if _, err := repository.repository.VerifyObject(
			ctx,
			internalID,
		); err != nil {
			return errors.Join(ErrCorrupt, err)
		}
	}
	active, err := direct.ContentReader().ListActiveSessions(ctx)
	if err != nil || len(active) != 0 {
		return errors.Join(
			errors.New("retention.verification_pending_session"),
			err,
		)
	}
	for _, item := range journal.Objects {
		if _, exists := repository.state.Objects[string(item.PublicID)]; exists {
			return errors.New("retention.mapping_removal_unverified")
		}
	}
	return nil
}

func (repository *KopiaRepository) writeRetentionJournal(
	ctx context.Context,
	direct kopiarepo.DirectRepository,
	journal retentionJournal,
) error {
	sessionCtx, writer, err := direct.NewDirectWriter(
		ctx,
		kopiarepo.WriteSessionOptions{
			Purpose: "VibeTable retention journal",
		},
	)
	if err != nil {
		return err
	}
	if err := putRetentionJournal(sessionCtx, writer, journal); err != nil {
		return err
	}
	return writer.Flush(sessionCtx)
}

func putRetentionJournal(
	ctx context.Context,
	writer kopiarepo.RepositoryWriter,
	journal retentionJournal,
) error {
	if err := validateRetentionJournal(journal); err != nil {
		return err
	}
	_, err := writer.ReplaceManifests(
		ctx,
		map[string]string{
			"type":        retentionJournalManifestType,
			"workspaceId": journal.WorkspaceID,
			"journalId":   journal.JournalID,
		},
		journal,
	)
	return err
}

func (repository *KopiaRepository) retentionJournals(
	ctx context.Context,
) ([]retentionJournal, []*kopiamanifest.EntryMetadata, error) {
	entries, err := repository.repository.FindManifests(
		ctx,
		map[string]string{"type": retentionJournalManifestType},
	)
	if err != nil {
		return nil, nil, err
	}
	result := make([]retentionJournal, 0, len(entries))
	for _, entry := range entries {
		var journal retentionJournal
		if _, err := repository.repository.GetManifest(
			ctx,
			entry.ID,
			&journal,
		); err != nil {
			return nil, nil, errors.Join(ErrCorrupt, err)
		}
		if err := validateRetentionJournal(journal); err != nil ||
			entry.Labels["journalId"] != journal.JournalID ||
			entry.Labels["workspaceId"] != journal.WorkspaceID {
			return nil, nil, errors.Join(ErrCorrupt, err)
		}
		result = append(result, journal)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].JournalID < result[right].JournalID
	})
	return result, entries, nil
}

func validateRetentionJournal(journal retentionJournal) error {
	if journal.FormatVersion != retentionJournalFormat ||
		journal.JournalID == "" ||
		journal.WorkspaceID == "" ||
		journal.ExpectedRevision == 0 ||
		journal.CreatedAt.IsZero() ||
		(journal.Stage != "prepared" &&
			journal.Stage != "mapping-removed" &&
			journal.Stage != "content-deleted" &&
			journal.Stage != "completed") ||
		len(journal.Objects) == 0 {
		return errors.New("retention.journal_invalid")
	}
	seen := map[ObjectID]struct{}{}
	for _, item := range journal.Objects {
		if !validateObjectID(item.PublicID) ||
			item.InternalID == "" ||
			item.Size < 0 {
			return errors.New("retention.journal_invalid")
		}
		if _, duplicate := seen[item.PublicID]; duplicate {
			return errors.New("retention.journal_invalid")
		}
		seen[item.PublicID] = struct{}{}
		for _, contentID := range item.ContentIDs {
			if _, err := kopiacontent.ParseID(contentID); err != nil {
				return errors.New("retention.journal_invalid")
			}
		}
	}
	return nil
}

func selectRetentionJournal(
	journals []retentionJournal,
	ids []ObjectID,
) (retentionJournal, bool, error) {
	var found *retentionJournal
	for index := range journals {
		journalIDs := make([]ObjectID, 0, len(journals[index].Objects))
		for _, item := range journals[index].Objects {
			journalIDs = append(journalIDs, item.PublicID)
		}
		sortObjectIDs(journalIDs)
		if equalObjectIDs(journalIDs, ids) {
			if found != nil {
				return retentionJournal{}, false,
					errors.New("retention.journal_ambiguous")
			}
			copy := journals[index]
			found = &copy
		}
	}
	if found == nil {
		for _, journal := range journals {
			if journal.Stage != "completed" {
				return retentionJournal{}, false,
					errors.New("retention.journal_pending")
			}
		}
		if len(journals) != 0 {
			// Completed journals are durable authority receipts and do not
			// serialize unrelated future retirement batches.
			return retentionJournal{}, false, nil
		}
		return retentionJournal{}, false, nil
	}
	return *found, true, nil
}

func normalizeRetentionIDs(ids []ObjectID) ([]ObjectID, error) {
	if len(ids) == 0 {
		return nil, errors.New("retention.object_ids_required")
	}
	result := append([]ObjectID(nil), ids...)
	sortObjectIDs(result)
	for index, id := range result {
		if !validateObjectID(id) ||
			(index > 0 && result[index-1] == id) {
			return nil, errors.New("retention.object_ids_invalid")
		}
	}
	return result, nil
}

func equalObjectIDs(left, right []ObjectID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortObjectIDs(ids []ObjectID) {
	sort.Slice(ids, func(left, right int) bool {
		return ids[left] < ids[right]
	})
}

func contentIDStrings(ids []kopiacontent.ID) []string {
	result := make([]string, len(ids))
	for index, id := range ids {
		result[index] = id.String()
	}
	sort.Strings(result)
	return result
}

func repositoryBlobBytes(
	ctx context.Context,
	direct kopiarepo.DirectRepository,
) (int64, error) {
	blobs, err := blob.ListAllBlobs(ctx, direct.BlobReader(), blob.ID(""))
	if err != nil {
		return 0, err
	}
	var total int64
	for _, metadata := range blobs {
		if metadata.Length < 0 || total > int64(^uint64(0)>>1)-metadata.Length {
			return 0, errors.New("repository.blob_size_overflow")
		}
		total += metadata.Length
	}
	return total, nil
}

func journalContains(journals []retentionJournal, id ObjectID) bool {
	for _, journal := range journals {
		for _, item := range journal.Objects {
			if item.PublicID == id {
				return true
			}
		}
	}
	return false
}
