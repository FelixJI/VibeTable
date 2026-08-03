package snapshotpkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

var (
	ErrInvalidPackage = errors.New("snapshot.package_invalid")
	ErrResourceLimit  = errors.New("snapshot.package_resource_limit")
)

type Limits struct {
	MaxEntries           int
	MaxEntryBytes        int64
	MaxUncompressedBytes int64
	MaxPathBytes         int
	MaxCompressionRatio  float64
	MaxManifestBytes     int64
}

func DefaultLimits() Limits {
	return Limits{
		MaxEntries: 10000, MaxEntryBytes: 4 << 30, MaxUncompressedBytes: 16 << 30,
		MaxPathBytes: 1024, MaxCompressionRatio: 200, MaxManifestBytes: 8 << 20,
	}
}

func (limits Limits) withDefaults() Limits {
	defaults := DefaultLimits()
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = defaults.MaxEntries
	}
	if limits.MaxEntryBytes <= 0 {
		limits.MaxEntryBytes = defaults.MaxEntryBytes
	}
	if limits.MaxUncompressedBytes <= 0 {
		limits.MaxUncompressedBytes = defaults.MaxUncompressedBytes
	}
	if limits.MaxPathBytes <= 0 {
		limits.MaxPathBytes = defaults.MaxPathBytes
	}
	if limits.MaxCompressionRatio <= 0 {
		limits.MaxCompressionRatio = defaults.MaxCompressionRatio
	}
	if limits.MaxManifestBytes <= 0 {
		limits.MaxManifestBytes = defaults.MaxManifestBytes
	}
	return limits
}

type Metadata struct {
	FormatVersion     int    `json:"formatVersion"`
	WorkspaceID       string `json:"workspaceId"`
	SnapshotID        string `json:"snapshotId"`
	WriterVersion     string `json:"writerVersion"`
	MinimumAppVersion string `json:"minimumAppVersion"`
}

type Manifest struct {
	Metadata Metadata          `json:"metadata"`
	Entries  map[string]string `json:"entries"`
}

type Inspection struct {
	Manifest          Manifest `json:"manifest"`
	UncompressedBytes int64    `json:"uncompressedBytes"`
	// PayloadBytes excludes manifest.json so callers can apply the same
	// application payload budget used by Export.
	PayloadBytes int64 `json:"-"`
}

func Export(writer io.Writer, metadata Metadata, entries map[string][]byte) error {
	if metadata.FormatVersion != 2 || metadata.WorkspaceID == "" || metadata.SnapshotID == "" {
		return ErrInvalidPackage
	}
	limits := DefaultLimits()
	// Export writes manifest.json in addition to the caller-provided entries.
	// Apply the same structural limits as Inspect so every emitted package is
	// eligible for inspection by the matching implementation.
	if len(entries) >= limits.MaxEntries {
		return ErrResourceLimit
	}
	hashes := make(map[string]string, len(entries))
	names := make([]string, 0, len(entries))
	for name, content := range entries {
		if !safeName(name) || name == "manifest.json" {
			return fmt.Errorf("%w: unsafe entry %q", ErrInvalidPackage, name)
		}
		if len(name) > limits.MaxPathBytes {
			return ErrResourceLimit
		}
		sum := sha256.Sum256(content)
		hashes[name] = hex.EncodeToString(sum[:])
		names = append(names, name)
	}
	sort.Strings(names)
	manifest := Manifest{Metadata: metadata, Entries: hashes}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(manifestRaw)) > limits.MaxManifestBytes {
		return ErrResourceLimit
	}
	archive := zip.NewWriter(writer)
	for _, name := range append([]string{"manifest.json"}, names...) {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o600)
		part, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		content := entries[name]
		if name == "manifest.json" {
			content = manifestRaw
		}
		if _, err := part.Write(content); err != nil {
			return err
		}
	}
	return archive.Close()
}

func Inspect(reader io.ReaderAt, size int64, limits Limits) (Inspection, error) {
	limits = limits.withDefaults()
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return Inspection{}, ErrInvalidPackage
	}
	if len(archive.File) == 0 || len(archive.File) > limits.MaxEntries {
		return Inspection{}, ErrResourceLimit
	}
	seen := map[string]bool{}
	actual := map[string]string{}
	var manifest Manifest
	var total int64
	var payloadTotal int64
	for _, file := range archive.File {
		if !safeName(file.Name) || len(file.Name) > limits.MaxPathBytes ||
			seen[file.Name] || file.FileInfo().Mode()&0o120000 != 0 {
			return Inspection{}, ErrInvalidPackage
		}
		seen[file.Name] = true
		if file.UncompressedSize64 > uint64(limits.MaxEntryBytes) {
			return Inspection{}, ErrResourceLimit
		}
		if file.UncompressedSize64 > 0 && (file.CompressedSize64 == 0 ||
			float64(file.UncompressedSize64)/float64(file.CompressedSize64) >
				limits.MaxCompressionRatio) {
			return Inspection{}, ErrResourceLimit
		}
		total += int64(file.UncompressedSize64)
		if total > limits.MaxUncompressedBytes {
			return Inspection{}, ErrResourceLimit
		}
		stream, err := file.Open()
		if err != nil {
			return Inspection{}, ErrInvalidPackage
		}
		if file.Name == "manifest.json" {
			content, err := io.ReadAll(io.LimitReader(stream, limits.MaxManifestBytes+1))
			closeErr := stream.Close()
			if int64(len(content)) > limits.MaxManifestBytes {
				return Inspection{}, ErrResourceLimit
			}
			if err != nil || closeErr != nil {
				return Inspection{}, ErrInvalidPackage
			}
			decoder := json.NewDecoder(bytes.NewReader(content))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&manifest); err != nil {
				return Inspection{}, ErrInvalidPackage
			}
			if decoder.Decode(&struct{}{}) != io.EOF {
				return Inspection{}, ErrInvalidPackage
			}
			continue
		}
		payloadTotal += int64(file.UncompressedSize64)
		hasher := sha256.New()
		written, copyErr := io.Copy(hasher, io.LimitReader(stream, limits.MaxEntryBytes+1))
		closeErr := stream.Close()
		if written > limits.MaxEntryBytes {
			return Inspection{}, ErrResourceLimit
		}
		if copyErr != nil || closeErr != nil ||
			written != int64(file.UncompressedSize64) {
			return Inspection{}, ErrInvalidPackage
		}
		actual[file.Name] = hex.EncodeToString(hasher.Sum(nil))
	}
	if manifest.Metadata.FormatVersion != 2 || manifest.Metadata.WorkspaceID == "" ||
		manifest.Metadata.SnapshotID == "" || !seen["manifest.json"] ||
		len(actual) != len(manifest.Entries) {
		return Inspection{}, ErrInvalidPackage
	}
	for name, expected := range manifest.Entries {
		if actual[name] != expected {
			return Inspection{}, ErrInvalidPackage
		}
	}
	return Inspection{
		Manifest:          manifest,
		UncompressedBytes: total,
		PayloadBytes:      payloadTotal,
	}, nil
}

func safeName(name string) bool {
	if name == "" || strings.Contains(name, "\\") || strings.ContainsRune(name, '\x00') {
		return false
	}
	clean := path.Clean(name)
	return clean == name && !strings.HasPrefix(clean, "/") && clean != ".." &&
		!strings.HasPrefix(clean, "../")
}
