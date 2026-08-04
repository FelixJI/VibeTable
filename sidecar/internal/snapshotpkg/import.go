package snapshotpkg

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	ErrPathGrantRequired = errors.New("snapshot.path_grant_required")
	ErrTargetModeInvalid = errors.New("snapshot.target_mode_invalid")
	ErrWorkspaceConflict = errors.New("snapshot.workspace_conflict")
)

type TargetMode string

const (
	TargetNewWorkspace     TargetMode = "newWorkspace"
	TargetCurrentWorkspace TargetMode = "currentWorkspace"
)

// PackageReadCapability is an already-authorized, read-only package view. For
// encrypted packages the Desktop implementation decrypts into an ACL-restricted
// staging object and destroys it when Close returns. A raw local path never
// crosses the RPC boundary.
type PackageReadCapability interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

type PathCapabilityResolver interface {
	OpenSnapshotPackage(
		ctx context.Context,
		pathGrant string,
		credential *string,
	) (PackageReadCapability, error)
}

type ImportOperation struct {
	OperationID string     `json:"operationId"`
	WorkspaceID string     `json:"workspaceId"`
	SnapshotID  string     `json:"snapshotId"`
	TargetMode  TargetMode `json:"targetMode"`
}

// ImportTarget owns the private staging area and the single publication point.
// Begin may create staging state, but must not mutate the formal workspace.
type ImportTarget interface {
	Begin(
		ctx context.Context,
		inspection Inspection,
		mode TargetMode,
	) (ImportStaging, error)
}

type ImportStaging interface {
	CreateEntry(
		ctx context.Context,
		name string,
		uncompressedBytes int64,
	) (io.WriteCloser, error)
	Commit(ctx context.Context) (ImportOperation, error)
	Abort(ctx context.Context) error
}

type Importer struct {
	paths  PathCapabilityResolver
	target ImportTarget
	limits Limits
}

func NewImporter(
	paths PathCapabilityResolver,
	target ImportTarget,
	limits Limits,
) (*Importer, error) {
	if paths == nil || target == nil {
		return nil, errors.New("snapshot.import_dependencies_required")
	}
	return &Importer{paths: paths, target: target, limits: limits.withDefaults()}, nil
}

func (importer *Importer) Import(
	ctx context.Context,
	pathGrant string,
	credential *string,
	mode TargetMode,
	currentWorkspaceID string,
) (operation ImportOperation, err error) {
	if strings.TrimSpace(pathGrant) == "" {
		return ImportOperation{}, ErrPathGrantRequired
	}
	if mode != TargetNewWorkspace && mode != TargetCurrentWorkspace {
		return ImportOperation{}, ErrTargetModeInvalid
	}
	source, err := importer.paths.OpenSnapshotPackage(ctx, pathGrant, credential)
	if err != nil {
		return ImportOperation{}, err
	}
	defer func() {
		err = errors.Join(err, source.Close())
	}()

	inspection, err := Inspect(source, source.Size(), importer.limits)
	if err != nil {
		return ImportOperation{}, err
	}
	if mode == TargetCurrentWorkspace {
		if currentWorkspaceID == "" ||
			inspection.Manifest.Metadata.WorkspaceID != currentWorkspaceID {
			return ImportOperation{}, ErrWorkspaceConflict
		}
	}

	staging, err := importer.target.Begin(ctx, inspection, mode)
	if err != nil {
		return ImportOperation{}, err
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, staging.Abort(ctx))
		}
	}()
	if err := extractVerified(ctx, source, inspection.Manifest, staging); err != nil {
		return ImportOperation{}, err
	}
	operation, err = staging.Commit(ctx)
	if err != nil {
		return ImportOperation{}, err
	}
	if operation.OperationID == "" ||
		operation.WorkspaceID != inspection.Manifest.Metadata.WorkspaceID ||
		operation.SnapshotID != inspection.Manifest.Metadata.SnapshotID ||
		operation.TargetMode != mode {
		return ImportOperation{}, errors.New("snapshot.import_receipt_invalid")
	}
	committed = true
	return operation, nil
}

func extractVerified(
	ctx context.Context,
	source PackageReadCapability,
	manifest Manifest,
	staging ImportStaging,
) error {
	archive, err := zip.NewReader(source, source.Size())
	if err != nil {
		return ErrInvalidPackage
	}
	remaining := make(map[string]string, len(manifest.Entries))
	for name, expected := range manifest.Entries {
		remaining[name] = expected
	}
	for _, file := range archive.File {
		expected, wanted := remaining[file.Name]
		if !wanted {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return ErrInvalidPackage
		}
		output, err := staging.CreateEntry(ctx, file.Name, int64(file.UncompressedSize64))
		if err != nil {
			input.Close()
			return err
		}
		hasher := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, hasher), input)
		inputErr := input.Close()
		outputErr := output.Close()
		if copyErr != nil || inputErr != nil || outputErr != nil {
			return errors.Join(copyErr, inputErr, outputErr)
		}
		if written != int64(file.UncompressedSize64) ||
			hex.EncodeToString(hasher.Sum(nil)) != expected {
			return fmt.Errorf("%w: entry changed after inspection: %s", ErrInvalidPackage, file.Name)
		}
		delete(remaining, file.Name)
	}
	if len(remaining) != 0 {
		return ErrInvalidPackage
	}
	return nil
}
