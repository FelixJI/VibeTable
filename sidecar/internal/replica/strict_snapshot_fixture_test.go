package replica

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

func strictSnapshotFixture(
	t *testing.T,
	repository objectrepo.Repository,
	authority objectrepo.Authority,
	snapshotSequence uint64,
	mutationRevision uint64,
	database []byte,
	files map[string][]byte,
	historyRoots ...objectrepo.ManifestID,
) snapshot.Record {
	t.Helper()
	ctx := context.Background()
	ledger, err := auditledger.Open(filepath.Join(t.TempDir(), "audit"))
	if err != nil {
		t.Fatalf("open audit ledger: %v", err)
	}
	defer ledger.Close()
	envelope, err := auditledger.NewEnvelope(
		fmt.Sprintf("event-%d", snapshotSequence),
		"fixture-source",
		1,
		fmt.Sprintf("mutation-%d", mutationRevision),
		json.RawMessage(`{"kind":"snapshot-fixture"}`),
		time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("create audit envelope: %v", err)
	}
	if _, err := ledger.Append(ctx, envelope); err != nil {
		t.Fatalf("append audit envelope: %v", err)
	}
	anchor := ledger.Anchor()
	auditPrefix, _, err := ledger.ExportPrefix(anchor)
	if err != nil {
		t.Fatalf("export audit prefix: %v", err)
	}
	topologyPayload, err := json.Marshal(map[string]any{
		"formatVersion":         1,
		"workspaceId":           authority.WorkspaceID,
		"topologySchemaVersion": 1,
		"businessSchemaVersion": 1,
		"businessDatabaseHash":  strictSnapshotDigest(database),
	})
	if err != nil {
		t.Fatalf("encode topology head: %v", err)
	}
	var historyRoot objectrepo.ManifestID
	var fileRevision uint64
	if len(historyRoots) > 1 {
		t.Fatal("strict snapshot fixture accepts at most one history root")
	}
	if len(historyRoots) == 1 {
		historyRoot = historyRoots[0]
		fileRevision = 1
	}
	filePayload, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"workspaceId":   authority.WorkspaceID,
		"historyRoot":   historyRoot,
		"fileRevision":  fileRevision,
	})
	if err != nil {
		t.Fatalf("encode file-state head: %v", err)
	}
	headReceipt, err := repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: authority,
		Manifests: []objectrepo.ManifestInput{
			{
				Name: "topology-head",
				Labels: map[string]string{
					"type":        "topology-head",
					"workspaceId": authority.WorkspaceID,
				},
				Payload: topologyPayload,
			},
			{
				Name: "file-state-head",
				Labels: map[string]string{
					"type":        "file-state-head",
					"workspaceId": authority.WorkspaceID,
				},
				Payload: filePayload,
			},
		},
	})
	if err != nil {
		t.Fatalf("commit topology and file-state heads: %v", err)
	}
	topologyRoot, err := json.Marshal(map[string]any{
		"manifestId": headReceipt.Manifests["topology-head"],
	})
	if err != nil {
		t.Fatalf("encode topology root: %v", err)
	}
	fileIDs := make(map[string]objectrepo.ObjectID, len(files))
	workspaceSettings := []byte(`{"formatVersion":1,"retention":{` +
		`"snapshotDays":30,"snapshotCount":50,` +
		`"snapshotBuckets":["daily"],"fileRevisionDays":30,` +
		`"fileRevisionCount":100,"fileRevisionBuckets":["daily"],` +
		`"repositoryLimitBytes":null}}`)
	inputs := []objectrepo.ObjectInput{
		{Name: "database", Content: database},
		{Name: "topology-root", Content: topologyRoot},
		{Name: "workspace-settings", Content: workspaceSettings},
		{Name: "audit-prefix", Content: auditPrefix},
	}
	for name, content := range files {
		fileIDs[name] = strictSnapshotObjectID(content)
		inputs = append(inputs, objectrepo.ObjectInput{
			Name: "file:" + name, Content: content,
		})
	}
	fileStateRoot, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"sourceRoot":    headReceipt.Manifests["file-state-head"],
		"files":         fileIDs,
		"attachments":   map[string]objectrepo.ObjectID{},
	})
	if err != nil {
		t.Fatalf("encode file-state root: %v", err)
	}
	inputs = append(inputs, objectrepo.ObjectInput{
		Name: "file-state-root", Content: fileStateRoot,
	})
	snapshotID := fmt.Sprintf(
		"00000000-0000-4000-8000-%012d",
		snapshotSequence,
	)
	auditAnchor := snapshot.AuditAnchor{
		Epoch:     1,
		Sequence:  anchor.SourceSequence,
		ChainHash: anchor.Hash,
	}
	manifest := snapshot.Manifest{
		FormatVersion:             2,
		SnapshotID:                snapshotID,
		WorkspaceID:               authority.WorkspaceID,
		FenceEpoch:                authority.FenceEpoch,
		ClaimID:                   authority.ClaimID,
		MutationRevision:          mutationRevision,
		SnapshotSequence:          snapshotSequence,
		Trigger:                   snapshot.TriggerAutomatic,
		CreatedAt:                 time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
		CreatedByDevice:           authority.ClaimID,
		BusinessDatabaseObjectID:  strictSnapshotObjectID(database),
		TopologyRootObjectID:      strictSnapshotObjectID(topologyRoot),
		FileStateRootObjectID:     strictSnapshotObjectID(fileStateRoot),
		WorkspaceSettingsObjectID: strictSnapshotObjectID(workspaceSettings),
		AuditAnchor:               auditAnchor,
		AuditPrefixObjectID:       strictSnapshotObjectID(auditPrefix),
		MinimumAppVersion:         "2.0.0",
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode snapshot manifest: %v", err)
	}
	commit, err := repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: authority,
		Objects:   inputs,
		Manifests: []objectrepo.ManifestInput{{
			Name: "snapshot",
			Labels: map[string]string{
				"type":        "snapshot",
				"workspaceId": authority.WorkspaceID,
				"snapshotId":  snapshotID,
			},
			Payload: manifestRaw,
		}},
	})
	if err != nil {
		t.Fatalf("commit snapshot closure: %v", err)
	}
	auditAnchorRaw, err := json.Marshal(auditAnchor)
	if err != nil {
		t.Fatalf("encode audit anchor: %v", err)
	}
	sealRaw, err := json.Marshal(snapshot.Seal{
		FormatVersion:     2,
		SnapshotID:        snapshotID,
		ManifestHash:      strictSnapshotDigest(manifestRaw),
		DatabaseHash:      strictSnapshotDigest(database),
		FileStateRootHash: strictSnapshotDigest(fileStateRoot),
		AuditAnchorHash:   strictSnapshotDigest(auditAnchorRaw),
		RepositoryFormat:  "workspace-repository-v2",
		FenceEpoch:        authority.FenceEpoch,
		ClaimID:           authority.ClaimID,
		MutationRevision:  mutationRevision,
		SnapshotSequence:  snapshotSequence,
		Verified:          true,
	})
	if err != nil {
		t.Fatalf("encode snapshot seal: %v", err)
	}
	sealCommit, err := repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: authority,
		Manifests: []objectrepo.ManifestInput{{
			Name: "snapshot-seal",
			Labels: map[string]string{
				"type":        "snapshot-seal",
				"workspaceId": authority.WorkspaceID,
				"snapshotId":  snapshotID,
			},
			Payload: sealRaw,
		}},
	})
	if err != nil {
		t.Fatalf("commit snapshot seal: %v", err)
	}
	roots := make([]objectrepo.ObjectID, 0, len(commit.Objects))
	seen := map[objectrepo.ObjectID]struct{}{}
	for _, id := range commit.Objects {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		roots = append(roots, id)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })
	record := snapshot.Record{
		SnapshotID:       snapshotID,
		WorkspaceID:      authority.WorkspaceID,
		ManifestID:       commit.Manifests["snapshot"],
		SealID:           sealCommit.Manifests["snapshot-seal"],
		SnapshotSequence: snapshotSequence,
		FenceEpoch:       authority.FenceEpoch,
		ClaimID:          authority.ClaimID,
		MutationRevision: mutationRevision,
		SchemaRevision:   1,
		FileRevision:     fileRevision,
		AuditRevision:    anchor.LedgerSequence,
		AuditAnchor:      anchor.Hash,
		Trigger:          snapshot.TriggerAutomatic,
		CreatedAt:        manifest.CreatedAt,
		Objects:          roots,
		ObjectMap:        commit.Objects,
		CatalogRevision:  snapshotSequence,
	}
	if err := snapshot.ValidateSnapshotBundle(ctx, repository, record); err != nil {
		t.Fatalf("strict snapshot fixture validation: %v", err)
	}
	return record
}

func strictSnapshotDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func strictSnapshotDatabase(t *testing.T, marker string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE _collections (
			id TEXT PRIMARY KEY,
			system BOOLEAN NOT NULL,
			type TEXT NOT NULL,
			name TEXT NOT NULL UNIQUE,
			fields JSON NOT NULL,
			indexes JSON NOT NULL,
			options JSON NOT NULL,
			created TEXT NOT NULL,
			updated TEXT NOT NULL
		);
		CREATE TABLE _migrations (
			file TEXT PRIMARY KEY,
			applied INTEGER NOT NULL
		);
		CREATE TABLE _vibetable_sidecar_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated TEXT NOT NULL
		);
		CREATE TABLE vibetable_audit_outbox (
			event_id TEXT PRIMARY KEY,
			source_epoch TEXT NOT NULL,
			source_sequence INTEGER NOT NULL,
			mutation_identity TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			payload_json BLOB NOT NULL,
			occurred_at TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL,
			UNIQUE(source_epoch, source_sequence)
		);
		CREATE TABLE workspace_v2_mutation_receipts (
			mutation_revision INTEGER PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			identity TEXT NOT NULL,
			audit_source_sequence INTEGER NOT NULL,
			committed_at TEXT NOT NULL,
			UNIQUE (
				workspace_id, session_epoch, fence_epoch,
				claim_id, kind, identity
			)
		);
		CREATE TABLE fixture_marker (value TEXT NOT NULL);
		INSERT INTO fixture_marker(value) VALUES (?);
	`, marker)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestStrictSnapshotBundleRejectsSwappedReadableObjects(t *testing.T) {
	ctx := context.Background()
	repository := objectrepo.NewMemory()
	authority := objectrepo.Authority{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		FenceEpoch:  1,
		ClaimID:     "22222222-2222-4222-8222-222222222222",
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	record := strictSnapshotFixture(
		t,
		repository,
		authority,
		1,
		1,
		strictSnapshotDatabase(t, "database-a"),
		map[string][]byte{"table.csv": []byte("file-a")},
	)
	alternates, err := repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: authority,
		Objects: []objectrepo.ObjectInput{
			{
				Name: "alternate-database",
				Content: strictSnapshotDatabase(
					t,
					"database-b",
				),
			},
			{Name: "alternate-file", Content: []byte("file-b")},
			{
				Name:    "alternate-root",
				Content: []byte(`{"formatVersion":1,"sourceRoot":"mf_other","files":{}}`),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		objectKey string
		alternate objectrepo.ObjectID
	}{
		{
			name:      "database",
			objectKey: "database",
			alternate: alternates.Objects["alternate-database"],
		},
		{
			name:      "current file",
			objectKey: "file:table.csv",
			alternate: alternates.Objects["alternate-file"],
		},
		{
			name:      "file state root",
			objectKey: "file-state-root",
			alternate: alternates.Objects["alternate-root"],
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := record
			tampered.ObjectMap = make(
				map[string]objectrepo.ObjectID,
				len(record.ObjectMap),
			)
			for name, id := range record.ObjectMap {
				tampered.ObjectMap[name] = id
			}
			original := tampered.ObjectMap[test.objectKey]
			tampered.ObjectMap[test.objectKey] = test.alternate
			tampered.Objects = append(
				[]objectrepo.ObjectID(nil), record.Objects...,
			)
			for index, id := range tampered.Objects {
				if id == original {
					tampered.Objects[index] = test.alternate
					break
				}
			}
			if err := snapshot.ValidateSnapshotBundle(
				ctx, repository, tampered,
			); !errors.Is(err, snapshot.ErrBundleInvalid) {
				t.Fatalf("swapped readable object error = %v", err)
			}
		})
	}
}

func TestStrictSnapshotBundleCrossChecksCatalogFields(t *testing.T) {
	ctx := context.Background()
	repository := objectrepo.NewMemory()
	authority := objectrepo.Authority{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		FenceEpoch:  1,
		ClaimID:     "22222222-2222-4222-8222-222222222222",
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	record := strictSnapshotFixture(
		t,
		repository,
		authority,
		1,
		1,
		strictSnapshotDatabase(t, "catalog-cross-check"),
		nil,
	)
	tests := []struct {
		name   string
		mutate func(*snapshot.Record)
	}{
		{
			name: "audit anchor",
			mutate: func(value *snapshot.Record) {
				value.AuditAnchor = "sha256:tampered"
			},
		},
		{
			name: "trigger",
			mutate: func(value *snapshot.Record) {
				value.Trigger = snapshot.TriggerManual
			},
		},
		{
			name: "created at",
			mutate: func(value *snapshot.Record) {
				value.CreatedAt = value.CreatedAt.Add(time.Second)
			},
		},
		{
			name: "source snapshot",
			mutate: func(value *snapshot.Record) {
				value.SourceSnapshotID =
					"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := record
			test.mutate(&tampered)
			err := snapshot.ValidateSnapshotBundle(
				ctx,
				repository,
				tampered,
			)
			if !errors.Is(err, snapshot.ErrBundleInvalid) {
				t.Fatalf("tampered catalog error = %v", err)
			}
		})
	}
}

func strictSnapshotObjectID(raw []byte) objectrepo.ObjectID {
	sum := sha256.Sum256(raw)
	return objectrepo.ObjectID("obj_" + hex.EncodeToString(sum[:]))
}
