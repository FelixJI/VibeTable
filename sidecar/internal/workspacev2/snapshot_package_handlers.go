package workspacev2

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	plaintext, err := os.CreateTemp(
		runtime.packageStagingRoot, "snapshot-export-*.zip",
	)
	if err != nil {
		return err
	}
	plaintextPath := plaintext.Name()
	defer os.Remove(plaintextPath)
	if err := plaintext.Chmod(0o600); err != nil {
		_ = plaintext.Close()
		return err
	}
	exportErr := snapshotpkg.Export(
		plaintext, metadata, entries, key,
	)
	syncErr := plaintext.Sync()
	closeErr := plaintext.Close()
	if err := errors.Join(exportErr, syncErr, closeErr); err != nil {
		return err
	}
	input, err := os.Open(plaintextPath)
	if err != nil {
		return err
	}
	var encryptErr error
	if len(recipients) > 0 {
		encryptErr = (snapshotpkg.AgeNative{}).EncryptRecipients(
			recipients,
			input,
			io.MultiWriter(output, hasher),
		)
	} else {
		encryptErr = (snapshotpkg.AgeNative{}).EncryptPassphrase(
			credential,
			input,
			io.MultiWriter(output, hasher),
		)
	}
	closeErr = input.Close()
	return errors.Join(encryptErr, closeErr)
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

func (runtime *Runtime) prepareSnapshotPackage(
	source string,
	credential *string,
) (string, bool, func(), error) {
	file, err := os.Open(source)
	if err != nil {
		return "", false, nil, err
	}
	prefix := make([]byte, len("age-encryption.org/v1"))
	count, readErr := io.ReadFull(file, prefix)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", false, nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return "", false, nil, closeErr
	}
	encrypted := count == len(prefix) &&
		string(prefix) == "age-encryption.org/v1"
	if !encrypted {
		if credential != nil {
			return "", false, nil, errors.New(
				"snapshot.credential_not_applicable",
			)
		}
		return source, false, nil, nil
	}
	if credential == nil || strings.TrimSpace(*credential) == "" {
		return "", true, nil, errors.New(
			"snapshot.credential_required",
		)
	}
	input, err := os.Open(source)
	if err != nil {
		return "", true, nil, err
	}
	output, err := os.CreateTemp(
		runtime.packageStagingRoot, "snapshot-import-*.zip",
	)
	if err != nil {
		_ = input.Close()
		return "", true, nil, err
	}
	outputPath := output.Name()
	cleanup := func() { _ = os.Remove(outputPath) }
	if err := output.Chmod(0o600); err != nil {
		_ = input.Close()
		_ = output.Close()
		cleanup()
		return "", true, nil, err
	}
	writer := &boundedPackageWriter{
		writer: output,
		limit:  maxSnapshotWorkingSet,
	}
	var decryptErr error
	if strings.HasPrefix(
		strings.TrimSpace(*credential), "AGE-SECRET-KEY-",
	) {
		decryptErr = (snapshotpkg.AgeNative{}).Decrypt(
			strings.TrimSpace(*credential), input, writer,
		)
	} else {
		decryptErr = (snapshotpkg.AgeNative{}).DecryptPassphrase(
			*credential, input, writer,
		)
	}
	inputErr := input.Close()
	syncErr := output.Sync()
	outputErr := output.Close()
	if err := errors.Join(
		decryptErr, inputErr, syncErr, outputErr,
	); err != nil {
		cleanup()
		return "", true, nil, err
	}
	return outputPath, true, cleanup, nil
}

type boundedPackageWriter struct {
	writer io.Writer
	limit  int64
	wrote  int64
}

func (writer *boundedPackageWriter) Write(raw []byte) (int, error) {
	if int64(len(raw)) > writer.limit-writer.wrote {
		return 0, snapshotpkg.ErrResourceLimit
	}
	count, err := writer.writer.Write(raw)
	writer.wrote += int64(count)
	return count, err
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
	plaintext, encrypted, cleanup, err := runtime.prepareSnapshotPackage(
		source, credentialPointer,
	)
	if cleanup != nil {
		defer cleanup()
	}
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
	key, err := runtime.snapshotPackageKey(ctx)
	if err != nil {
		return nil, err
	}
	defer clearBytes(key)
	inspection, _, err := inspectPackageFile(plaintext, key, false)
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
	plaintext, _, cleanup, err := runtime.prepareSnapshotPackage(
		sourcePath, credentialPointer,
	)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return nil, err
	}
	key, err := runtime.snapshotPackageKey(ctx)
	if err != nil {
		return nil, err
	}
	defer clearBytes(key)
	inspection, entries, err := inspectPackageFile(plaintext, key, true)
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
	source, err := decodeImportedSnapshot(entries, inspection)
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
	if valid, err := runtime.snapshotIntegrity(ctx, record); err != nil {
		return nil, snapshot.Manifest{}, err
	} else if !valid {
		return nil, snapshot.Manifest{}, errors.New("snapshot.integrity_failed")
	}
	manifestRecord, err := runtime.repository.GetManifest(ctx, record.ManifestID)
	if err != nil {
		return nil, snapshot.Manifest{}, err
	}
	sealRecord, err := runtime.repository.GetManifest(ctx, record.SealID)
	if err != nil {
		return nil, snapshot.Manifest{}, err
	}
	manifest, err := decodeStrict[snapshot.Manifest](manifestRecord.Payload)
	if err != nil {
		return nil, snapshot.Manifest{}, err
	}
	entries := map[string][]byte{
		"snapshot/catalog.json":  append([]byte(nil), mustJSON(record)...),
		"snapshot/manifest.json": append([]byte(nil), manifestRecord.Payload...),
		"snapshot/seal.json":     append([]byte(nil), sealRecord.Payload...),
	}
	var total int64
	objects := make(map[string][]byte, len(record.ObjectMap))
	for name, id := range record.ObjectMap {
		reader, err := runtime.repository.Open(ctx, id)
		if err != nil {
			return nil, snapshot.Manifest{}, err
		}
		content, readErr := io.ReadAll(io.LimitReader(
			reader,
			maxSnapshotWorkingSet-total+1,
		))
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, snapshot.Manifest{}, err
		}
		total += int64(len(content))
		if total > maxSnapshotWorkingSet {
			return nil, snapshot.Manifest{}, errors.New(
				"snapshot.package_resource_limit",
			)
		}
		objects[name] = content
		entries["objects/"+base64.RawURLEncoding.EncodeToString(
			[]byte(name),
		)] = content
	}
	topologyRoot, fileRoot, err := referencedSnapshotRoots(objects)
	if err != nil {
		return nil, snapshot.Manifest{}, err
	}
	topology, err := runtime.repository.GetManifest(ctx, topologyRoot)
	if err != nil {
		return nil, snapshot.Manifest{}, err
	}
	files, err := runtime.repository.GetManifest(ctx, fileRoot)
	if err != nil {
		return nil, snapshot.Manifest{}, err
	}
	entries["roots/topology-head.json"] = append(
		[]byte(nil),
		topology.Payload...,
	)
	entries["roots/file-state-head.json"] = append(
		[]byte(nil),
		files.Payload...,
	)
	return entries, manifest, nil
}

func referencedSnapshotRoots(
	objects map[string][]byte,
) (objectrepo.ManifestID, objectrepo.ManifestID, error) {
	topology, ok := objects["topology-root"]
	if !ok {
		return "", "", errors.New("snapshot.topology_root_missing")
	}
	fileState, ok := objects["file-state-root"]
	if !ok {
		return "", "", errors.New("snapshot.file_root_missing")
	}
	var topologyRef struct {
		ManifestID objectrepo.ManifestID `json:"manifestId"`
	}
	var fileRef struct {
		SourceRoot objectrepo.ManifestID `json:"sourceRoot"`
	}
	if err := json.Unmarshal(topology, &topologyRef); err != nil ||
		topologyRef.ManifestID == "" {
		return "", "", errors.New("snapshot.topology_root_invalid")
	}
	if err := json.Unmarshal(fileState, &fileRef); err != nil ||
		fileRef.SourceRoot == "" {
		return "", "", errors.New("snapshot.file_root_invalid")
	}
	return topologyRef.ManifestID, fileRef.SourceRoot, nil
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

func inspectPackageFile(
	path string,
	key []byte,
	readEntries bool,
) (snapshotpkg.Inspection, map[string][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return snapshotpkg.Inspection{}, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return snapshotpkg.Inspection{}, nil, errors.New(
			"snapshot.package_invalid",
		)
	}
	inspection, err := snapshotpkg.Inspect(
		file,
		info.Size(),
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
	archive, err := zip.NewReader(file, info.Size())
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
	settings          []byte
	auditPrefix       []byte
	topologyPayload   []byte
	filePayload       []byte
	repository        objectrepo.Repository
}

func decodeImportedSnapshot(
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
	for name, raw := range objects {
		if strings.HasPrefix(name, "file:") {
			files[strings.TrimPrefix(name, "file:")] = raw
		}
	}
	topologyPayload := entries["roots/topology-head.json"]
	filePayload := entries["roots/file-state-head.json"]
	if len(topologyPayload) == 0 || len(filePayload) == 0 {
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
		settings:        append([]byte(nil), objects["workspace-settings"]...),
		auditPrefix:     append([]byte(nil), objects["audit-prefix"]...),
		topologyPayload: append([]byte(nil), topologyPayload...),
		filePayload:     append([]byte(nil), filePayload...),
	}, nil
}

func (source *importedSnapshotSource) Freeze(
	ctx context.Context,
	intent writecoordinator.CaptureIntent,
) (snapshot.BarrierView, writecoordinator.FrozenRoots, error) {
	if source.repository == nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
			errors.New("snapshot.import_repository_required")
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
				Payload: source.filePayload,
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
