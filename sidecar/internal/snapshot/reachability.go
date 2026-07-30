package snapshot

import (
	"context"
	"errors"
	"sort"

	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

// FileHistoryObjectIDsForHead returns every revision object reachable from an
// immutable file-state head. It intentionally returns object IDs rather than
// bytes so capture pins, retention and maintenance can protect the full
// closure without changing the snapshot catalog wire format.
func FileHistoryObjectIDsForHead(
	ctx context.Context,
	repository objectrepo.Repository,
	workspaceID string,
	fileHeadID objectrepo.ManifestID,
) ([]objectrepo.ObjectID, error) {
	if repository == nil || workspaceID == "" {
		return nil, ErrBundleInvalid
	}
	if fileHeadID == "" {
		return []objectrepo.ObjectID{}, nil
	}
	fileHeadRecord, err := repository.GetManifest(ctx, fileHeadID)
	if err != nil {
		return nil, err
	}
	if err := validateManifestArtifact(
		fileHeadRecord,
		fileHeadID,
		"file-state-head",
		workspaceID,
		"",
	); err != nil {
		return nil, err
	}
	fileHead, err := decodeStrictBundle[fileStateHeadPayload](
		fileHeadRecord.Payload,
	)
	if err != nil ||
		fileHead.FormatVersion != 1 ||
		fileHead.WorkspaceID != workspaceID {
		return nil, ErrBundleInvalid
	}
	if fileHead.HistoryRoot == "" {
		return []objectrepo.ObjectID{}, nil
	}
	historyRecord, err := repository.GetManifest(ctx, fileHead.HistoryRoot)
	if err != nil {
		return nil, err
	}
	if err := validateManifestArtifact(
		historyRecord,
		fileHead.HistoryRoot,
		"filehistory-root",
		workspaceID,
		"",
	); err != nil {
		return nil, err
	}
	history, err := filehistory.ValidateRootPayload(
		historyRecord.Payload,
		workspaceID,
	)
	if err != nil {
		if errors.Is(err, filehistory.ErrResourceLimit) {
			return nil, ErrBundleResourceLimit
		}
		return nil, ErrBundleInvalid
	}
	if len(history) > maxBundleEntries {
		return nil, ErrBundleResourceLimit
	}
	result := make([]objectrepo.ObjectID, 0, len(history))
	for _, revision := range history {
		result = append(result, revision.ObjectID)
	}
	return result, nil
}

// HistoryObjectIDs returns the file-history objects referenced by a catalog
// record without materializing their contents.
func HistoryObjectIDs(
	ctx context.Context,
	repository objectrepo.Repository,
	record Record,
) ([]objectrepo.ObjectID, error) {
	fileStateID := record.ObjectMap["file-state-root"]
	if repository == nil ||
		record.WorkspaceID == "" ||
		fileStateID == "" {
		return nil, ErrBundleInvalid
	}
	raw, err := readBundleObject(ctx, repository, fileStateID)
	if err != nil {
		return nil, err
	}
	if objectIDBundle(raw) != fileStateID {
		return nil, objectrepo.ErrCorrupt
	}
	reference, err := decodeStrictBundle[fileStateRootReference](raw)
	if err != nil ||
		reference.FormatVersion != 1 ||
		reference.SourceRoot == "" {
		return nil, ErrBundleInvalid
	}
	return FileHistoryObjectIDsForHead(
		ctx,
		repository,
		record.WorkspaceID,
		reference.SourceRoot,
	)
}

// ReachabilityObjectIDs returns the stable current-object catalog roots plus
// every history-only revision root. Existing Record JSON remains unchanged.
func ReachabilityObjectIDs(
	ctx context.Context,
	repository objectrepo.Repository,
	record Record,
) ([]objectrepo.ObjectID, error) {
	if repository == nil || record.WorkspaceID == "" {
		return nil, ErrBundleInvalid
	}
	// Legacy/internal records predate typed file-state roots. They have no
	// separately addressable history graph, so their complete flat root set
	// remains the only reachable closure.
	if record.ObjectMap["file-state-root"] == "" {
		return mergeSnapshotObjectIDs(record.Objects), nil
	}
	history, err := HistoryObjectIDs(ctx, repository, record)
	if err != nil {
		return nil, err
	}
	return mergeSnapshotObjectIDs(record.Objects, history), nil
}

func mergeSnapshotObjectIDs(
	groups ...[]objectrepo.ObjectID,
) []objectrepo.ObjectID {
	seen := map[objectrepo.ObjectID]struct{}{}
	for _, group := range groups {
		for _, id := range group {
			if id != "" {
				seen[id] = struct{}{}
			}
		}
	}
	result := make([]objectrepo.ObjectID, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left] < result[right]
	})
	return result
}
