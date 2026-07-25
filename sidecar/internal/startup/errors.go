// Package startup provides fail-closed startup preflight checks and stable,
// actionable diagnostics. Fault seams stay inside the Go sidecar's internal
// package boundary and are never exposed through HTTP or renderer RPC.
package startup

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

const (
	CodeStorageFull      = "sidecar.storage_full"
	CodeStorageLocked    = "sidecar.storage_locked"
	CodeStorageReadOnly  = "sidecar.storage_read_only"
	CodePortUnavailable  = "sidecar.port_unavailable"
	CodeMigrationCorrupt = "sidecar.migration_corrupt"
	CodeStartFailed      = "sidecar.start_failed"
)

// Error deliberately omits filesystem paths and raw OS errors from Error().
// The wrapped cause remains available to trusted diagnostics via errors.Unwrap.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (err *Error) Error() string {
	return fmt.Sprintf("%s: %s", err.Code, err.Message)
}

func (err *Error) Unwrap() error {
	return err.Cause
}

// CheckDataDirectory performs a real create/write/sync/remove probe before
// PocketBase opens its database. The injected form is intentionally unexported
// and used only by package tests for OS failures that cannot be produced
// portably (for example ENOSPC in a bounded CI sandbox).
func CheckDataDirectory(dataDir string) error {
	return checkDataDirectory(dataDir, probeDataDirectory)
}

func checkDataDirectory(dataDir string, probe func(string) error) error {
	if strings.TrimSpace(dataDir) == "" {
		return &Error{
			Code:    CodeStorageReadOnly,
			Message: "choose a writable local data directory and retry",
			Cause:   errors.New("data directory is required"),
		}
	}
	if probe == nil {
		probe = probeDataDirectory
	}
	if err := probe(dataDir); err != nil {
		return Classify("storage", err)
	}
	return nil
}

func probeDataDirectory(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dataDir, ".vibetable-startup-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if _, err := file.Write([]byte{0x56}); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

// ValidateMigrationManifest prevents business startup when the packaged
// manifest or any embedded source checksum is corrupt.
func ValidateMigrationManifest(load func() error) error {
	if load == nil {
		return &Error{
			Code:    CodeMigrationCorrupt,
			Message: "reinstall the matching application package or restore a known-good backup",
			Cause:   errors.New("migration manifest loader is required"),
		}
	}
	if err := load(); err != nil {
		return &Error{
			Code:    CodeMigrationCorrupt,
			Message: "reinstall the matching application package or restore a known-good backup",
			Cause:   err,
		}
	}
	return nil
}

// Classify converts platform-specific failures into stable operator guidance.
func Classify(operation string, err error) error {
	if err == nil {
		return nil
	}
	var stable *Error
	if errors.As(err, &stable) {
		return stable
	}

	lower := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, syscall.ENOSPC),
		strings.Contains(lower, "no space left"),
		strings.Contains(lower, "disk full"):
		return &Error{
			Code:    CodeStorageFull,
			Message: "free disk space or move the local data directory, then retry",
			Cause:   err,
		}
	case errors.Is(err, syscall.EBUSY),
		strings.Contains(lower, "database is locked"),
		strings.Contains(lower, "sharing violation"),
		strings.Contains(lower, "used by another process"):
		return &Error{
			Code:    CodeStorageLocked,
			Message: "close the other VibeTable instance or process using the data files, then retry",
			Cause:   err,
		}
	case errors.Is(err, os.ErrPermission),
		strings.Contains(lower, "read-only file system"),
		strings.Contains(lower, "access is denied"):
		return &Error{
			Code:    CodeStorageReadOnly,
			Message: "choose a writable local data directory or fix its permissions, then retry",
			Cause:   err,
		}
	case strings.Contains(lower, "address already in use"),
		strings.Contains(lower, "only one usage of each socket address"):
		return &Error{
			Code:    CodePortUnavailable,
			Message: "close the process occupying the loopback port and retry",
			Cause:   err,
		}
	case strings.Contains(lower, "migration"),
		strings.Contains(lower, "checksum mismatch"),
		strings.Contains(lower, "incompatible schema"):
		return &Error{
			Code:    CodeMigrationCorrupt,
			Message: "reinstall the matching application package or restore a known-good backup",
			Cause:   err,
		}
	default:
		label := strings.TrimSpace(operation)
		if label == "" {
			label = "sidecar"
		}
		return &Error{
			Code:    CodeStartFailed,
			Message: label + " failed; inspect the sanitized sidecar diagnostics and retry",
			Cause:   err,
		}
	}
}
