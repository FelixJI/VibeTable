package workspacev2

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/snapshotpkg"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func (runtime *Runtime) registerSnapshotExportHandler() {
	runtime.dispatcher.Register(
		"snapshot.export",
		protocolv2.WorkspaceScope,
		runtime.exportSnapshot,
	)
	runtime.dispatcher.Register(
		"snapshot.inspectPackage",
		protocolv2.GlobalScope,
		runtime.inspectSnapshotPackage,
	)
	runtime.dispatcher.Register(
		"snapshot.import",
		protocolv2.GlobalScope,
		runtime.importSnapshotPackage,
	)
}

type exportSnapshotParams struct {
	SnapshotID string          `json:"snapshotId"`
	PathGrant  string          `json:"pathGrant"`
	Encryption string          `json:"encryption"`
	Recipients []string        `json:"recipients"`
	Credential json.RawMessage `json:"credential"`
}

func (runtime *Runtime) exportSnapshot(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, err := decodeStrictWorkspaceWire(wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	params, err := decodeStrict[exportSnapshotParams](paramsRaw)
	if err != nil ||
		!validUUID(params.SnapshotID) ||
		params.PathGrant == "" ||
		params.Recipients == nil ||
		(params.Encryption != "none" && params.Encryption != "age") {
		return nil, errors.New("snapshot.request_invalid")
	}
	credential, credentialPresent, err := decodeNullableCredential(
		params.Credential,
	)
	if err != nil ||
		(params.Encryption == "none" &&
			(len(params.Recipients) != 0 || credentialPresent)) ||
		(params.Encryption == "age" &&
			((len(params.Recipients) == 0) == !credentialPresent ||
				len(params.Recipients) > 16)) {
		return nil, errors.New("snapshot.request_invalid")
	}
	target, err := consumePathGrant(
		ctx,
		params.PathGrant,
		"snapshot.export",
		wire.OperationID,
		"snapshot-export",
	)
	if err != nil {
		return nil, err
	}
	record, err := runtime.snapshotRecord(ctx, params.SnapshotID)
	if err != nil {
		return nil, err
	}
	token, _ := runtime.coordinator.Current()
	exportPinExpiry := time.Now().UTC().Add(2 * time.Hour)
	exportPin, err := runtime.repository.Pin(
		ctx,
		token.Authority(),
		record.Objects,
		"snapshot-export:"+wire.OperationID,
		&exportPinExpiry,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = runtime.repository.ReleasePin(
			context.WithoutCancel(ctx),
			token.Authority(),
			exportPin.PinID,
		)
	}()
	entries, manifest, err := runtime.snapshotPackageEntries(ctx, record)
	if err != nil {
		return nil, err
	}
	key, err := runtime.snapshotPackageKey(ctx)
	if err != nil {
		return nil, err
	}
	defer clearBytes(key)
	if err := validateExportTarget(target); err != nil {
		return nil, err
	}
	output, err := os.CreateTemp(
		filepath.Dir(target),
		"."+filepath.Base(target)+".*.tmp",
	)
	if err != nil {
		return nil, err
	}
	outputPath := output.Name()
	defer os.Remove(outputPath)
	if err := output.Chmod(0o600); err != nil {
		_ = output.Close()
		return nil, err
	}
	hasher := sha256.New()
	metadata := snapshotpkg.Metadata{
		FormatVersion:     2,
		WorkspaceID:       record.WorkspaceID,
		SnapshotID:        record.SnapshotID,
		WriterVersion:     "2.0.0",
		MinimumAppVersion: manifest.MinimumAppVersion,
	}
	var exportErr error
	if params.Encryption == "none" {
		exportErr = snapshotpkg.Export(
			io.MultiWriter(output, hasher), metadata, entries, key,
		)
	} else {
		exportErr = runtime.exportAgeSnapshot(
			output,
			hasher,
			metadata,
			entries,
			key,
			params.Recipients,
			credential,
		)
	}
	syncErr := output.Sync()
	closeErr := output.Close()
	if err := errors.Join(exportErr, syncErr, closeErr); err != nil {
		return nil, err
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return nil, err
	}
	contentHash := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	result := map[string]any{
		"displayName": filepath.Base(target),
		"sha256":      contentHash,
	}
	var externalOperation *externalFileOperation
	if operation, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		receipt, receiptErr :=
			protocolv2.BuildContextOperationReceipt(ctx, result)
		if receiptErr != nil {
			return nil, receiptErr
		}
		prepared := externalFileOperation{
			Receipt:     receipt,
			Session:     operation.Session,
			Staging:     filepath.Clean(outputPath),
			Target:      filepath.Clean(target),
			ContentHash: contentHash,
			ContentSize: outputInfo.Size(),
		}
		if err := runtime.state.prepareExternalFileOperation(
			ctx,
			prepared,
		); err != nil {
			return nil, err
		}
		externalOperation = &prepared
	}
	if err := replaceGrantedFile(outputPath, target); err != nil {
		return nil, err
	}
	if externalOperation != nil {
		externalOperation.State = "prepared"
		if err := runtime.state.completeExternalFileOperation(
			context.WithoutCancel(ctx),
			*externalOperation,
		); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (runtime *Runtime) exportAgeSnapshot(
	output *os.File,
	hasher io.Writer,
	metadata snapshotpkg.Metadata,
	entries map[string][]byte,
	key []byte,
	recipients []string,
	credential string,
) error {
	return streamAgeSnapshotPackage(
		io.MultiWriter(output, hasher),
		metadata,
		entries,
		key,
		recipients,
		credential,
	)
}

func decodeNullableCredential(
	raw json.RawMessage,
) (string, bool, error) {
	if bytes.Equal(raw, []byte("null")) {
		return "", false, nil
	}
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil ||
		strings.TrimSpace(value) == "" {
		return "", false, errors.New("snapshot.credential_invalid")
	}
	return value, true, nil
}

func validateExportTarget(target string) error {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("workspace.path_grant_path_invalid")
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		parent, parentErr := os.Stat(filepath.Dir(target))
		if parentErr != nil || !parent.IsDir() {
			return errors.Join(
				errors.New("workspace.path_grant_path_invalid"),
				parentErr,
			)
		}
		return nil
	}
	if err != nil || !info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(
			errors.New("workspace.path_grant_path_invalid"),
			err,
		)
	}
	return nil
}

type inspectSnapshotPackageParams struct {
	PathGrant  string          `json:"pathGrant"`
	Credential json.RawMessage `json:"credential"`
}

func (runtime *Runtime) inspectSnapshotPackage(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, err := decodeStrict[contractsv2.GlobalWireScope](wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	params, err := decodeStrict[inspectSnapshotPackageParams](paramsRaw)
	if err != nil || params.PathGrant == "" {
		return nil, errors.New("snapshot.request_invalid")
	}
	credential, credentialPresent, err := decodeNullableCredential(
		params.Credential,
	)
	if err != nil {
		return nil, errors.New("snapshot.request_invalid")
	}
	var credentialPointer *string
	if credentialPresent {
		credentialPointer = &credential
	}
	source, err := consumePathGrant(
		ctx,
		params.PathGrant,
		"snapshot.inspectPackage",
		wire.OperationID,
		"snapshot-import",
	)
	if err != nil {
		return nil, err
	}
	sourceHash, _, err := hashRestoreFile(source)
	if err != nil {
		return nil, err
	}
	planID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	plan := snapshotImportPlan{
		PlanID:     planID,
		SourcePath: source,
		SourceHash: sourceHash,
		ExpiresAt:  expiresAt.Format(time.RFC3339Nano),
	}
	persistPlan := func(result any) error {
		if operation, dispatched :=
			protocolv2.OperationFromContext(ctx); dispatched {
			receipt, receiptErr :=
				protocolv2.BuildContextOperationReceipt(ctx, result)
			if receiptErr != nil {
				return receiptErr
			}
			return runtime.state.
				putSnapshotImportPlanWithOperationReceipt(
					ctx,
					plan,
					operation.Session,
					receipt,
				)
		}
		return runtime.state.putSnapshotImportPlan(ctx, plan)
	}
	prepared, encrypted, err := prepareSnapshotPackageSource(
		source, credentialPointer,
	)
	if err != nil {
		if encrypted && credentialPointer == nil {
			result := map[string]any{
				"planId":           planID,
				"trusted":          false,
				"workspaceId":      "",
				"sourceSnapshotId": nil,
				"snapshotCount":    0,
				"encrypted":        true,
				"verified":         false,
				"expiresAt":        expiresAt.Format(time.RFC3339),
			}
			if persistErr := persistPlan(result); persistErr != nil {
				return nil, persistErr
			}
			return result, nil
		}
		return nil, err
	}
	defer prepared.Close()
	key, err := runtime.snapshotPackageKey(ctx)
	if err != nil {
		return nil, err
	}
	defer clearBytes(key)
	inspection, _, err := inspectPreparedPackage(prepared, key, false)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"planId":           planID,
		"trusted":          inspection.TrustedForOriginalWorkspace,
		"workspaceId":      inspection.Manifest.Metadata.WorkspaceID,
		"sourceSnapshotId": inspection.Manifest.Metadata.SnapshotID,
		"snapshotCount":    1,
		"encrypted":        encrypted,
		"verified":         true,
		"expiresAt":        expiresAt.Format(time.RFC3339),
	}
	if err := persistPlan(result); err != nil {
		return nil, err
	}
	return result, nil
}

type importSnapshotPackageParams struct {
	PlanID            string                 `json:"planId"`
	Credential        json.RawMessage        `json:"credential"`
	TargetMode        snapshotpkg.TargetMode `json:"targetMode"`
	TargetWorkspaceID *string                `json:"targetWorkspaceId"`
}

func (runtime *Runtime) importSnapshotPackage(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, err := decodeStrict[contractsv2.GlobalWireScope](wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	params, err := decodeStrict[importSnapshotPackageParams](paramsRaw)
	if err != nil ||
		!validUUID(params.PlanID) ||
		(params.TargetMode != snapshotpkg.TargetCurrentWorkspace &&
			params.TargetMode != snapshotpkg.TargetNewWorkspace) {
		return nil, errors.New("snapshot.request_invalid")
	}
	credential, credentialPresent, err := decodeNullableCredential(
		params.Credential,
	)
	if err != nil {
		return nil, errors.New("snapshot.request_invalid")
	}
	var credentialPointer *string
	if credentialPresent {
		credentialPointer = &credential
	}
	if params.TargetWorkspaceID == nil ||
		!validUUID(*params.TargetWorkspaceID) ||
		*params.TargetWorkspaceID != runtime.manifest.WorkspaceID {
		return nil, snapshotpkg.ErrWorkspaceConflict
	}
	plan, err := runtime.state.snapshotImportPlan(ctx, params.PlanID)
	if err != nil {
		return nil, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if err != nil {
		return nil, errors.New("snapshot.import_plan_corrupt")
	}
	if !time.Now().UTC().Before(expiresAt) {
		_ = runtime.state.deleteSnapshotImportPlan(
			context.WithoutCancel(ctx),
			plan.PlanID,
		)
		return nil, errors.New("snapshot.import_plan_expired")
	}
	if !filepath.IsAbs(plan.SourcePath) ||
		filepath.Clean(plan.SourcePath) != plan.SourcePath {
		return nil, errors.New("snapshot.import_plan_corrupt")
	}
	sourceHash, _, err := hashRestoreFile(plan.SourcePath)
	if err != nil || sourceHash != plan.SourceHash {
		return nil, errors.Join(
			errors.New("snapshot.import_plan_stale"),
			err,
		)
	}
	sourcePath := plan.SourcePath
	prepared, _, err := prepareSnapshotPackageSource(
		sourcePath, credentialPointer,
	)
	if err != nil {
		return nil, err
	}
	defer prepared.Close()
	key, err := runtime.snapshotPackageKey(ctx)
	if err != nil {
		return nil, err
	}
	defer clearBytes(key)
	inspection, entries, err := inspectPreparedPackage(prepared, key, true)
	if err != nil {
		return nil, err
	}
	sourceWorkspaceID := inspection.Manifest.Metadata.WorkspaceID
	sourceSnapshotID := inspection.Manifest.Metadata.SnapshotID
	if !validUUID(sourceWorkspaceID) ||
		!validUUID(sourceSnapshotID) {
		return nil, snapshotpkg.ErrInvalidPackage
	}
	switch params.TargetMode {
	case snapshotpkg.TargetCurrentWorkspace:
		if sourceWorkspaceID != runtime.manifest.WorkspaceID {
			return nil, snapshotpkg.ErrWorkspaceConflict
		}
		if err := snapshotpkg.RequireOriginalWorkspaceTrust(inspection); err != nil {
			return nil, err
		}
	case snapshotpkg.TargetNewWorkspace:
		if sourceWorkspaceID == runtime.manifest.WorkspaceID {
			return nil, snapshotpkg.ErrWorkspaceConflict
		}
	}
	source, err := decodeImportedSnapshot(ctx, entries, inspection)
	if err != nil {
		return nil, err
	}
	source.repository = runtime.repository
	if params.TargetMode == snapshotpkg.TargetNewWorkspace {
		if err := source.rewriteWorkspace(
			sourceWorkspaceID,
			runtime.manifest.WorkspaceID,
		); err != nil {
			return nil, err
		}
	}
	token, _ := runtime.coordinator.Current()
	barrier, err := snapshot.NewCoordinatedBarrier(
		runtime.coordinator,
		token,
		&source,
	)
	if err != nil {
		return nil, err
	}
	importer := snapshot.NewCoordinator(
		runtime.repository,
		barrier,
		runtime.catalog,
	)
	state := "published"
	if params.TargetMode == snapshotpkg.TargetNewWorkspace {
		// The Desktop workspace broker must immediately route this published
		// target snapshot through current-workspace restore before presenting
		// the newly created workspace as ready.
		state = "restoreRequired"
	}
	captureContext := ctx
	if _, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		captureContext, err = snapshot.WithOperationReceiptBuilder(
			ctx,
			func(
				record snapshot.Record,
			) (protocolv2.OperationReceipt, error) {
				return protocolv2.BuildContextOperationReceipt(
					ctx,
					map[string]any{
						"operationId":       wire.OperationID,
						"snapshotId":        record.SnapshotID,
						"sourceWorkspaceId": sourceWorkspaceID,
						"sourceSnapshotId":  sourceSnapshotID,
						"state":             state,
					},
				)
			},
		)
		if err != nil {
			return nil, err
		}
	}
	record, created, err := importer.Capture(
		captureContext,
		snapshot.CaptureRequest{
			WorkspaceID: runtime.manifest.WorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerImport,
			Pinned:      true,
		},
	)
	if err != nil {
		return nil, err
	}
	if !created || record.SnapshotID == "" {
		return nil, errors.New("snapshot.import_publication_failed")
	}
	_ = runtime.state.deleteSnapshotImportPlan(
		context.WithoutCancel(ctx),
		plan.PlanID,
	)
	return map[string]any{
		"operationId":       wire.OperationID,
		"snapshotId":        record.SnapshotID,
		"sourceWorkspaceId": sourceWorkspaceID,
		"sourceSnapshotId":  sourceSnapshotID,
		"state":             state,
	}, nil
}

func (runtime *Runtime) snapshotRecord(
	ctx context.Context,
	snapshotID string,
) (snapshot.Record, error) {
	tombstoned, err := runtime.retention.store.TombstonedSnapshotIDs(ctx)
	if err != nil {
		return snapshot.Record{}, err
	}
	if _, deleted := tombstoned[snapshotID]; deleted {
		return snapshot.Record{}, errors.New("snapshot.not_found")
	}
	records, err := runtime.catalog.List(ctx, runtime.manifest.WorkspaceID)
	if err != nil {
		return snapshot.Record{}, err
	}
	for _, record := range records {
		if record.SnapshotID == snapshotID {
			return record, nil
		}
	}
	return snapshot.Record{}, errors.New("snapshot.not_found")
}

func (runtime *Runtime) snapshotPackageEntries(
	ctx context.Context,
	record snapshot.Record,
) (map[string][]byte, snapshot.Manifest, error) {
	bundle, err := snapshot.LoadSnapshotBundle(
		ctx, runtime.repository, record,
	)
	if err != nil {
		return nil, snapshot.Manifest{}, err
	}
	manifest, err := decodeStrict[snapshot.Manifest](bundle.Manifest.Payload)
	if err != nil {
		return nil, snapshot.Manifest{}, err
	}
	entries := map[string][]byte{
		"snapshot/catalog.json":  append([]byte(nil), mustJSON(record)...),
		"snapshot/manifest.json": append([]byte(nil), bundle.Manifest.Payload...),
		"snapshot/seal.json":     append([]byte(nil), bundle.Seal.Payload...),
		"roots/topology-head.json": append(
			[]byte(nil), bundle.TopologyHead.Payload...,
		),
		"roots/file-state-head.json": append(
			[]byte(nil), bundle.FileStateHead.Payload...,
		),
	}
	var total int64
	for name, content := range bundle.Objects {
		total += int64(len(content))
		if total > maxSnapshotWorkingSet {
			return nil, snapshot.Manifest{}, errors.New(
				"snapshot.package_resource_limit",
			)
		}
		entries["objects/"+base64.RawURLEncoding.EncodeToString(
			[]byte(name),
		)] = content
	}
	if bundle.HistoryRoot != nil {
		entries["roots/filehistory-root.json"] = append(
			[]byte(nil), bundle.HistoryRoot.Payload...,
		)
	}
	for id, content := range bundle.HistoryObjects {
		total += int64(len(content))
		if total > maxSnapshotWorkingSet {
			return nil, snapshot.Manifest{}, errors.New(
				"snapshot.package_resource_limit",
			)
		}
		entries[historyObjectPackageEntry(id)] = append(
			[]byte(nil), content...,
		)
	}
	return entries, manifest, nil
}

func (runtime *Runtime) snapshotPackageKey(
	ctx context.Context,
) ([]byte, error) {
	mode := objectrepo.EncryptionMode(runtime.manifest.EncryptionMode)
	if mode != objectrepo.EncryptionProtected {
		return nil, nil
	}
	keys, err := objectrepo.NewKeyProvider(
		objectrepo.WindowsCredentialVault{},
	).Open(ctx, runtime.manifest.WorkspaceID, mode)
	if err != nil {
		return nil, err
	}
	return keys.Password, nil
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func inspectPreparedPackage(
	prepared *preparedSnapshotPackage,
	key []byte,
	readEntries bool,
) (snapshotpkg.Inspection, map[string][]byte, error) {
	if prepared == nil ||
		prepared.ReaderAt() == nil ||
		prepared.Size() <= 0 {
		return snapshotpkg.Inspection{}, nil, errors.New(
			"snapshot.package_invalid",
		)
	}
	inspection, err := snapshotpkg.Inspect(
		prepared.ReaderAt(),
		prepared.Size(),
		snapshotpkg.DefaultLimits(),
		key,
	)
	if err != nil || !readEntries {
		if err != nil {
			return inspection, nil, err
		}
		if err := validatePackageCompatibility(
			inspection.Manifest.Metadata,
		); err != nil {
			return snapshotpkg.Inspection{}, nil, err
		}
		return inspection, nil, nil
	}
	if err := validatePackageCompatibility(
		inspection.Manifest.Metadata,
	); err != nil {
		return snapshotpkg.Inspection{}, nil, err
	}
	archive, err := zip.NewReader(
		prepared.ReaderAt(),
		prepared.Size(),
	)
	if err != nil {
		return snapshotpkg.Inspection{}, nil, snapshotpkg.ErrInvalidPackage
	}
	entries := make(map[string][]byte, len(archive.File))
	var total int64
	for _, item := range archive.File {
		if item.Name == "manifest.json" {
			continue
		}
		total += int64(item.UncompressedSize64)
		if total > maxSnapshotWorkingSet {
			return snapshotpkg.Inspection{}, nil,
				snapshotpkg.ErrResourceLimit
		}
		reader, err := item.Open()
		if err != nil {
			return snapshotpkg.Inspection{}, nil,
				snapshotpkg.ErrInvalidPackage
		}
		raw, readErr := io.ReadAll(io.LimitReader(
			reader,
			int64(item.UncompressedSize64)+1,
		))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil ||
			len(raw) != int(item.UncompressedSize64) {
			return snapshotpkg.Inspection{}, nil,
				snapshotpkg.ErrInvalidPackage
		}
		entries[item.Name] = raw
	}
	return inspection, entries, nil
}

func validatePackageCompatibility(metadata snapshotpkg.Metadata) error {
	writer, writerErr := parsePackageVersion(metadata.WriterVersion)
	minimum, minimumErr := parsePackageVersion(metadata.MinimumAppVersion)
	supported := [3]uint64{2, 0, 0}
	if writerErr != nil || minimumErr != nil ||
		comparePackageVersion(writer, supported) > 0 ||
		comparePackageVersion(minimum, supported) > 0 {
		return errors.New("snapshot.package_version_unsupported")
	}
	return nil
}

func parsePackageVersion(value string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(value, ".")
	if len(parts) != len(result) {
		return result, errors.New("snapshot.package_version_invalid")
	}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, errors.New("snapshot.package_version_invalid")
		}
		parsed, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return result, errors.New("snapshot.package_version_invalid")
		}
		result[index] = parsed
	}
	return result, nil
}

func comparePackageVersion(left, right [3]uint64) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

type importedSnapshotSource struct {
	workspaceID       string
	sourceWorkspaceID string
	sourceSnapshotID  string
	record            snapshot.Record
	manifest          snapshot.Manifest
	database          []byte
	files             map[string][]byte
	attachments       map[string][]byte
	settings          []byte
	auditPrefix       []byte
	topologyPayload   []byte
	filePayload       []byte
	historyPayload    []byte
	historyObjects    map[objectrepo.ObjectID][]byte
	repository        objectrepo.Repository
}

func decodeImportedSnapshot(
	ctx context.Context,
	entries map[string][]byte,
	inspection snapshotpkg.Inspection,
) (importedSnapshotSource, error) {
	record, err := decodeStrict[snapshot.Record](
		entries["snapshot/catalog.json"],
	)
	if err != nil {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	manifest, err := decodeStrict[snapshot.Manifest](
		entries["snapshot/manifest.json"],
	)
	if err != nil {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	var seal snapshot.Seal
	seal, err = decodeStrict[snapshot.Seal](
		entries["snapshot/seal.json"],
	)
	if err != nil ||
		record.WorkspaceID != inspection.Manifest.Metadata.WorkspaceID ||
		record.SnapshotID != inspection.Manifest.Metadata.SnapshotID ||
		manifest.WorkspaceID != record.WorkspaceID ||
		manifest.SnapshotID != record.SnapshotID ||
		seal.SnapshotID != record.SnapshotID ||
		!seal.Verified {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	objects := make(map[string][]byte, len(record.ObjectMap))
	for name := range record.ObjectMap {
		raw, ok := entries["objects/"+base64.RawURLEncoding.EncodeToString(
			[]byte(name),
		)]
		if !ok {
			return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
		}
		objects[name] = raw
	}
	required := []string{
		"database",
		"workspace-settings",
		"audit-prefix",
		"topology-root",
		"file-state-root",
	}
	for _, name := range required {
		if _, ok := objects[name]; !ok {
			return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
		}
	}
	files := map[string][]byte{}
	attachments := map[string][]byte{}
	for name, raw := range objects {
		if strings.HasPrefix(name, "file:") {
			files[strings.TrimPrefix(name, "file:")] = raw
		}
		if strings.HasPrefix(name, "attachment:") {
			attachments[strings.TrimPrefix(name, "attachment:")] = raw
		}
	}
	topologyPayload := entries["roots/topology-head.json"]
	filePayload := entries["roots/file-state-head.json"]
	if len(topologyPayload) == 0 || len(filePayload) == 0 {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	var topologyReference struct {
		ManifestID objectrepo.ManifestID `json:"manifestId"`
	}
	var fileReference struct {
		SourceRoot objectrepo.ManifestID `json:"sourceRoot"`
	}
	var fileHead struct {
		HistoryRoot objectrepo.ManifestID `json:"historyRoot"`
	}
	if err := json.Unmarshal(
		objects["topology-root"], &topologyReference,
	); err != nil || topologyReference.ManifestID == "" {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	if err := json.Unmarshal(
		objects["file-state-root"], &fileReference,
	); err != nil || fileReference.SourceRoot == "" {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	if err := json.Unmarshal(filePayload, &fileHead); err != nil {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	historyPayload := entries["roots/filehistory-root.json"]
	historyObjects := map[objectrepo.ObjectID][]byte{}
	var historyRecord *objectrepo.ManifestRecord
	if fileHead.HistoryRoot != "" {
		if len(historyPayload) == 0 {
			return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
		}
		ids, err := snapshot.FileHistoryObjectIDs(historyPayload)
		if err != nil {
			return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
		}
		for _, id := range ids {
			if _, alreadyCurrent := objectBytesByID(
				record.ObjectMap, objects, id,
			); alreadyCurrent {
				continue
			}
			raw, ok := entries[historyObjectPackageEntry(id)]
			if !ok {
				return importedSnapshotSource{},
					snapshotpkg.ErrInvalidPackage
			}
			historyObjects[id] = raw
		}
		value := snapshot.NewPackageManifestRecord(
			fileHead.HistoryRoot,
			"filehistory-root",
			record.WorkspaceID,
			"",
			historyPayload,
		)
		historyRecord = &value
	} else if len(historyPayload) != 0 {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	bundle := snapshot.SnapshotBundle{
		Record: record,
		Manifest: snapshot.NewPackageManifestRecord(
			record.ManifestID,
			"snapshot",
			record.WorkspaceID,
			record.SnapshotID,
			entries["snapshot/manifest.json"],
		),
		Seal: snapshot.NewPackageManifestRecord(
			record.SealID,
			"snapshot-seal",
			record.WorkspaceID,
			record.SnapshotID,
			entries["snapshot/seal.json"],
		),
		Objects: objects,
		TopologyHead: snapshot.NewPackageManifestRecord(
			topologyReference.ManifestID,
			"topology-head",
			record.WorkspaceID,
			"",
			topologyPayload,
		),
		FileStateHead: snapshot.NewPackageManifestRecord(
			fileReference.SourceRoot,
			"file-state-head",
			record.WorkspaceID,
			"",
			filePayload,
		),
		HistoryRoot:    historyRecord,
		HistoryObjects: historyObjects,
	}
	if err := snapshot.ValidateSnapshotBundleData(bundle); err != nil {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	if err := validateImportedSQLite(ctx, objects["database"]); err != nil {
		return importedSnapshotSource{}, snapshotpkg.ErrInvalidPackage
	}
	return importedSnapshotSource{
		workspaceID:       record.WorkspaceID,
		sourceWorkspaceID: inspection.Manifest.Metadata.WorkspaceID,
		sourceSnapshotID:  inspection.Manifest.Metadata.SnapshotID,
		record:            record,
		manifest:          manifest,
		database: append(
			[]byte(nil),
			objects["database"]...,
		),
		files:           files,
		attachments:     attachments,
		settings:        append([]byte(nil), objects["workspace-settings"]...),
		auditPrefix:     append([]byte(nil), objects["audit-prefix"]...),
		topologyPayload: append([]byte(nil), topologyPayload...),
		filePayload:     append([]byte(nil), filePayload...),
		historyPayload:  append([]byte(nil), historyPayload...),
		historyObjects:  cloneHistoryObjects(historyObjects),
	}, nil
}

type sqliteDeserializer interface {
	Deserialize([]byte) error
}

func validateImportedSQLite(ctx context.Context, raw []byte) error {
	if len(raw) == 0 {
		return snapshotpkg.ErrInvalidPackage
	}
	database, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return err
	}
	defer database.Close()
	deserializeConnection, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer deserializeConnection.Close()
	if err := deserializeConnection.Raw(func(driverConnection any) error {
		deserializer, ok := driverConnection.(sqliteDeserializer)
		if !ok {
			return errors.New("snapshot.sqlite_deserialize_unavailable")
		}
		return deserializer.Deserialize(raw)
	}); err != nil {
		return err
	}
	var quickCheck string
	if err := deserializeConnection.QueryRowContext(
		ctx, "PRAGMA quick_check",
	).Scan(&quickCheck); err != nil || quickCheck != "ok" {
		return fmt.Errorf(
			"%w: sqlite quick_check=%q: %v",
			snapshotpkg.ErrInvalidPackage,
			quickCheck,
			err,
		)
	}
	foreignKeys, err := deserializeConnection.QueryContext(
		ctx, "PRAGMA foreign_key_check",
	)
	if err != nil {
		return err
	}
	defer foreignKeys.Close()
	if foreignKeys.Next() {
		return fmt.Errorf(
			"%w: sqlite foreign_key_check failed",
			snapshotpkg.ErrInvalidPackage,
		)
	}
	if err := foreignKeys.Err(); err != nil {
		return err
	}
	var collectionTable int
	if err := deserializeConnection.QueryRowContext(
		ctx,
		`SELECT count(*) FROM sqlite_schema
		 WHERE type = 'table' AND name = '_collections'`,
	).Scan(&collectionTable); err != nil || collectionTable != 1 {
		return fmt.Errorf(
			"%w: PocketBase _collections table count=%d: %v",
			snapshotpkg.ErrInvalidPackage,
			collectionTable,
			err,
		)
	}
	return nil
}

func (source *importedSnapshotSource) Freeze(
	ctx context.Context,
	intent writecoordinator.CaptureIntent,
) (snapshot.BarrierView, writecoordinator.FrozenRoots, error) {
	if source.repository == nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
			errors.New("snapshot.import_repository_required")
	}
	filePayload := append([]byte(nil), source.filePayload...)
	if len(source.historyPayload) != 0 {
		objectIDs := make(
			[]objectrepo.ObjectID, 0, len(source.historyObjects),
		)
		for id := range source.historyObjects {
			objectIDs = append(objectIDs, id)
		}
		sort.Slice(objectIDs, func(i, j int) bool {
			return objectIDs[i] < objectIDs[j]
		})
		inputs := make([]objectrepo.ObjectInput, 0, len(objectIDs))
		for _, id := range objectIDs {
			inputs = append(inputs, objectrepo.ObjectInput{
				Name: "history:" + string(id),
				Content: append(
					[]byte(nil), source.historyObjects[id]...,
				),
			})
		}
		historyReceipt, err := source.repository.Commit(
			ctx,
			objectrepo.CommitRequest{
				Authority: intent.Token.Authority(),
				Objects:   inputs,
				Manifests: []objectrepo.ManifestInput{{
					Name: "filehistory-root",
					Labels: map[string]string{
						"type":        "filehistory-root",
						"workspaceId": source.workspaceID,
					},
					Payload: source.historyPayload,
				}},
			},
		)
		if err != nil {
			return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
				err
		}
		historyRoot := historyReceipt.Manifests["filehistory-root"]
		if !historyReceipt.Durable || historyRoot == "" {
			return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
				errors.New("snapshot.import_history_receipt_invalid")
		}
		for _, id := range objectIDs {
			if historyReceipt.Objects["history:"+string(id)] != id {
				return snapshot.BarrierView{},
					writecoordinator.FrozenRoots{},
					errors.New("snapshot.import_history_receipt_invalid")
			}
		}
		filePayload, err = replaceHistoryRoot(
			filePayload, historyRoot,
		)
		if err != nil {
			return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
				err
		}
	}
	receipt, err := source.repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: intent.Token.Authority(),
		Manifests: []objectrepo.ManifestInput{
			{
				Name: "topology-head",
				Labels: map[string]string{
					"type": "topology-head", "workspaceId": source.workspaceID,
				},
				Payload: source.topologyPayload,
			},
			{
				Name: "file-state-head",
				Labels: map[string]string{
					"type": "file-state-head", "workspaceId": source.workspaceID,
				},
				Payload: filePayload,
			},
		},
	})
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	topologyRoot := receipt.Manifests["topology-head"]
	fileRoot := receipt.Manifests["file-state-head"]
	if !receipt.Durable || topologyRoot == "" || fileRoot == "" {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
			errors.New("snapshot.import_root_receipt_invalid")
	}
	return snapshot.BarrierView{
			SchemaRevision:    source.record.SchemaRevision,
			FileRevision:      source.record.FileRevision,
			AuditRevision:     source.record.AuditRevision,
			AuditAnchor:       source.manifest.AuditAnchor.ChainHash,
			AuditEpoch:        source.manifest.AuditAnchor.Epoch,
			AuditSequence:     source.manifest.AuditAnchor.Sequence,
			Database:          append([]byte(nil), source.database...),
			Files:             cloneFiles(source.files),
			Attachments:       cloneFiles(source.attachments),
			WorkspaceSettings: append([]byte(nil), source.settings...),
			AuditPrefix:       append([]byte(nil), source.auditPrefix...),
			CreatedByDevice:   intent.Token.ClaimID,
			MinimumAppVersion: source.manifest.MinimumAppVersion,
			SourceWorkspaceID: source.sourceWorkspaceID,
			SourceSnapshotID:  source.sourceSnapshotID,
		},
		writecoordinator.FrozenRoots{
			DatabaseView: "import:" + digestBytes(source.database),
			TopologyRoot: topologyRoot,
			FileRoot:     fileRoot,
			AuditAnchor:  source.manifest.AuditAnchor.ChainHash,
		},
		nil
}

func (source *importedSnapshotSource) rewriteWorkspace(
	from string,
	to string,
) error {
	if !validUUID(from) || !validUUID(to) || from == to {
		return errors.New("snapshot.import_workspace_identity_invalid")
	}
	var err error
	source.topologyPayload, err = rewriteWorkspaceJSON(
		source.topologyPayload, from, to,
	)
	if err != nil {
		return err
	}
	source.filePayload, err = rewriteWorkspaceJSON(
		source.filePayload, from, to,
	)
	if err != nil {
		return err
	}
	if len(source.historyPayload) != 0 {
		source.historyPayload, err = rewriteWorkspaceJSON(
			source.historyPayload, from, to,
		)
		if err != nil {
			return err
		}
	}
	source.settings, err = rewriteWorkspaceJSON(
		source.settings, from, to,
	)
	if err != nil {
		return err
	}
	if bytes.Contains(source.database, []byte(from)) {
		return errors.New("snapshot.import_workspace_identity_conflict")
	}
	source.workspaceID = to
	source.record.WorkspaceID = to
	source.manifest.WorkspaceID = to
	return nil
}

func rewriteWorkspaceJSON(
	raw []byte,
	from string,
	to string,
) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, snapshotpkg.ErrInvalidPackage
	}
	if err := rewriteWorkspaceValue(value, from, to); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func rewriteWorkspaceValue(value any, from string, to string) error {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if err := rewriteWorkspaceValue(item, from, to); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range typed {
			if key == "workspaceId" {
				text, ok := item.(string)
				if !ok || (text != from && text != to) {
					return errors.New(
						"snapshot.import_workspace_identity_conflict",
					)
				}
				typed[key] = to
				continue
			}
			if err := rewriteWorkspaceValue(item, from, to); err != nil {
				return err
			}
		}
	}
	return nil
}

func cloneFiles(source map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(source))
	for name, content := range source {
		result[name] = append([]byte(nil), content...)
	}
	return result
}

func historyObjectPackageEntry(id objectrepo.ObjectID) string {
	return "history-objects/" + base64.RawURLEncoding.EncodeToString(
		[]byte(id),
	)
}

func objectBytesByID(
	objectMap map[string]objectrepo.ObjectID,
	objects map[string][]byte,
	target objectrepo.ObjectID,
) ([]byte, bool) {
	for name, id := range objectMap {
		if id == target {
			raw, found := objects[name]
			return raw, found
		}
	}
	return nil, false
}

func cloneHistoryObjects(
	source map[objectrepo.ObjectID][]byte,
) map[objectrepo.ObjectID][]byte {
	result := make(map[objectrepo.ObjectID][]byte, len(source))
	for id, content := range source {
		result[id] = append([]byte(nil), content...)
	}
	return result
}

func replaceHistoryRoot(
	raw []byte,
	historyRoot objectrepo.ManifestID,
) ([]byte, error) {
	var head struct {
		FormatVersion uint64                `json:"formatVersion"`
		WorkspaceID   string                `json:"workspaceId"`
		HistoryRoot   objectrepo.ManifestID `json:"historyRoot"`
		FileRevision  uint64                `json:"fileRevision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&head); err != nil ||
		head.FormatVersion == 0 ||
		head.WorkspaceID == "" ||
		historyRoot == "" {
		return nil, snapshotpkg.ErrInvalidPackage
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, snapshotpkg.ErrInvalidPackage
	}
	head.HistoryRoot = historyRoot
	return json.Marshal(head)
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func decodeStrictWorkspaceWire(
	raw json.RawMessage,
) (wire workspaceWireScopeAlias, err error) {
	return decodeStrict[workspaceWireScopeAlias](raw)
}

type workspaceWireScopeAlias = struct {
	Scope        string `json:"scope"`
	WorkspaceID  string `json:"workspaceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	OperationID  string `json:"operationId"`
	Sequence     uint64 `json:"sequence"`
}
