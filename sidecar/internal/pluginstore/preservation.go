package pluginstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// The #486 Host-owned read-only preservation snapshot. It pages the stored
// vibetable_plugin_records rows of one project/plugin pair — every kind
// (installation, package revision, private setting, audit) — as verbatim raw
// payloads keyed by the stable PocketBase record id. The service performs no
// writes, no migration and no new hashing; per-record revisions reuse the
// existing stored CAS revision (installation/setting payloads) or the stored
// per-project seq (revision/audit rows). The Host fail-closes when the
// installation identity/revision changes between pages.
const (
	PreservationContractVersion = "1.0"
	PreservationChunkBytes      = 256 * 1024
	// RecordOffset is capped at 1 GiB in 256 KiB shard steps, mirroring the
	// frozen legacyLayout.snapshot cursor budget. Stored payloads are bounded
	// by maxPayloadBytes, so the cap never truncates a legitimate record.
	maxPreservationRecordOffset = 1 << 30
)

var preservationRecordIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{15}$`)

// PreservationSnapshotRequest is the closed Host-only request: the four
// identity fields are required; afterRecordId is empty or a 15-character
// alphanumeric record id and recordOffset is a non-negative 256 KiB
// multiple within the cursor budget.
type PreservationSnapshotRequest struct {
	ProjectKey           string `json:"projectKey"`
	PluginID             string `json:"pluginId"`
	InstallationID       string `json:"installationId"`
	InstallationRevision int64  `json:"installationRevision"`
	AfterRecordID        string `json:"afterRecordId"`
	RecordOffset         int64  `json:"recordOffset"`
}

// PreservationSnapshotPage carries one bounded byte shard. Chunk holds the
// next slice of a single JSON document
// {recordId,kind,itemKey,rawPayload,seq}; concatenating the chunk stream of
// one record reassembles that document exactly, enough for the Host to
// compose records.json and resolve the original .vtplugin from its trusted
// workspace cache via packageHash alone. Revision is the record's existing
// revision sequence rendered as a decimal string (payload CAS revision for
// installation/setting snapshots, stored per-project seq for revision/audit
// rows; empty when absent) — no new hash is introduced.
type PreservationSnapshotPage struct {
	ContractVersion   string `json:"contractVersion"`
	RecordID          string `json:"recordId"`
	Kind              string `json:"kind"`
	ItemKey           string `json:"itemKey"`
	Revision          string `json:"revision"`
	Chunk             []byte `json:"chunk"`
	RecordDone        bool   `json:"recordDone"`
	CollectionDone    bool   `json:"collectionDone"`
	NextAfterRecordID string `json:"nextAfterRecordId"`
	NextRecordOffset  int64  `json:"nextRecordOffset"`
}

// preservationRecordDocument is the exact wire shape embedded in the chunk
// stream. RawPayload carries the stored payload_json text verbatim — the
// original bytes, whitespace and unknown keys included, never re-marshaled
// or normalized. The Go service never interprets payload fields for
// package resolution: the Host resolves the original package exclusively
// from its trusted workspace cache (PluginRetainedPackage.PathFor) keyed by
// packageHash.
type preservationRecordDocument struct {
	RecordID   string `json:"recordId"`
	Kind       string `json:"kind"`
	ItemKey    string `json:"itemKey"`
	RawPayload string `json:"rawPayload"`
	Seq        int64  `json:"seq"`
}

func (service *Service) PreservationSnapshot(
	ctx context.Context,
	request PreservationSnapshotRequest,
) (PreservationSnapshotPage, error) {
	page := PreservationSnapshotPage{
		ContractVersion:   PreservationContractVersion,
		NextAfterRecordID: request.AfterRecordID,
		NextRecordOffset:  request.RecordOffset,
		Chunk:             []byte{},
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if request.PluginID == "" || request.InstallationID == "" || request.InstallationRevision < 1 ||
		!validPreservationCursor(request.AfterRecordID, request.RecordOffset) {
		return page, requestError(
			"plugin.request_invalid",
			"preservation identity or cursor is invalid", nil,
		)
	}
	// Every page revalidates the workspace project and the installation
	// identity/revision; a concurrent lifecycle change fails closed here.
	// The installation identity is the authoritative PocketBase record id,
	// never a payload field.
	if err := service.ValidateProject(request.ProjectKey); err != nil {
		return page, err
	}
	var row struct {
		ID      string `db:"id"`
		Kind    string `db:"kind"`
		ItemKey string `db:"item_key"`
		Payload string `db:"payload_json"`
		Seq     int64  `db:"seq"`
	}
	var queryErr error
	// Pin identity validation and the page read to the same database view;
	// uninstall cleanup must not turn a remaining record into a successful EOF.
	err := service.app.RunInTransaction(func(txApp core.App) error {
		stored, err := findRecord(
			txApp, KindInstallation, request.ProjectKey, request.PluginID, "current",
		)
		if err != nil {
			return err
		}
		if stored == nil {
			return notFound()
		}
		installation, err := recordFromStored(stored)
		if err != nil {
			return err
		}
		if stored.Id != request.InstallationID ||
			jsonInt(installation.Payload, "revision") != request.InstallationRevision {
			return revisionConflict(
				KindInstallation, request.InstallationRevision, "installation changed",
			)
		}
		queryErr = txApp.DB().NewQuery(
			"SELECT id, kind, item_key, payload_json, seq FROM {{" + recordsCollection + "}} " +
				"WHERE project_key={:project} AND plugin_id={:plugin} AND id > {:after} " +
				"ORDER BY id LIMIT 1",
		).Bind(dbx.Params{
			"project": request.ProjectKey, "plugin": request.PluginID, "after": request.AfterRecordID,
		}).WithContext(ctx).One(&row)
		if err := ctx.Err(); err != nil {
			return err
		}
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return storageError()
		}
		return nil
	})
	if err != nil {
		return page, err
	}
	if errors.Is(queryErr, sql.ErrNoRows) {
		if request.RecordOffset != 0 {
			return page, requestError(
				"plugin.request_invalid",
				"preservation cursor is beyond the collection", nil,
			)
		}
		page.CollectionDone = true
		return page, nil
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	document, err := json.Marshal(preservationRecordDocument{
		RecordID:   row.ID,
		Kind:       row.Kind,
		ItemKey:    row.ItemKey,
		RawPayload: row.Payload,
		Seq:        row.Seq,
	})
	if err != nil {
		return page, storageError()
	}
	if request.RecordOffset >= int64(len(document)) {
		return page, requestError(
			"plugin.request_invalid",
			"preservation cursor is beyond the record", nil,
		)
	}
	end := request.RecordOffset + PreservationChunkBytes
	if end > int64(len(document)) {
		end = int64(len(document))
	}
	page.RecordID = row.ID
	page.Kind = row.Kind
	page.ItemKey = row.ItemKey
	page.Revision = preservationRecordRevision(row.Kind, row.Payload, row.Seq)
	page.Chunk = document[request.RecordOffset:end]
	page.RecordDone = end == int64(len(document))
	if page.RecordDone {
		page.NextAfterRecordID = row.ID
		page.NextRecordOffset = 0
	} else {
		page.NextRecordOffset = end
	}
	return page, nil
}

// validPreservationCursor enforces the closed keyset/fragment cursor shape
// shared with the frozen legacyLayout.snapshot channel.
func validPreservationCursor(afterRecordID string, recordOffset int64) bool {
	if afterRecordID != "" && !preservationRecordIDPattern.MatchString(afterRecordID) {
		return false
	}
	if recordOffset < 0 || recordOffset%PreservationChunkBytes != 0 ||
		recordOffset > maxPreservationRecordOffset {
		return false
	}
	return true
}

// preservationRecordRevision reuses the record's existing revision identity
// rendered as a decimal string: the payload CAS revision for
// installation/setting snapshots, the stored per-project seq for
// revision/audit rows, and the empty string when neither exists. No new
// hash is introduced.
func preservationRecordRevision(kind, payload string, seq int64) string {
	switch kind {
	case KindInstallation, KindSetting:
		if revision := jsonInt(json.RawMessage(payload), "revision"); revision > 0 {
			return strconv.FormatInt(revision, 10)
		}
	}
	if seq > 0 {
		return strconv.FormatInt(seq, 10)
	}
	return ""
}
