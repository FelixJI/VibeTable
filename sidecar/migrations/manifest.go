package migrations

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
)

//go:embed manifest.json 2026072401_bootstrap.go 2026072402_internal_collections.go 2026072404_realtime_outbox_retention.go 2026072801_field_settings_v2_metadata.go 2026072805_audit_outbox.go 2026080501_relation_pairs.go 2026081201_interfaces.go 2026081202_content_links.go 2026081203_view_v2_metadata.go 2026090601_computation_dependencies.go
var manifestFS embed.FS

var checksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Manifest struct {
	FormatVersion int     `json:"formatVersion"`
	SchemaVersion int     `json:"schemaVersion"`
	Migrations    []Entry `json:"migrations"`
}

type Entry struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
}

func Register(app *pocketbase.PocketBase) {
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate: false,
	})
}

func LoadManifest() (Manifest, error) {
	raw, err := manifestFS.ReadFile("manifest.json")
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	if err := validate(manifest); err != nil {
		return Manifest{}, err
	}
	for _, entry := range manifest.Migrations {
		source, err := manifestFS.ReadFile(entry.Source)
		if err != nil {
			return Manifest{}, fmt.Errorf("read migration %d source: %w", entry.ID, err)
		}
		sum := sha256.Sum256(source)
		if hex.EncodeToString(sum[:]) != entry.SHA256 {
			return Manifest{}, fmt.Errorf("migration %d checksum mismatch", entry.ID)
		}
	}
	return manifest, nil
}

func Hash() string {
	raw, err := manifestFS.ReadFile("manifest.json")
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validate(manifest Manifest) error {
	if manifest.FormatVersion != 1 {
		return errors.New("unsupported migration manifest format")
	}
	if manifest.SchemaVersion < 1 {
		return errors.New("schema version must be positive")
	}
	var previous int64
	seen := make(map[int64]struct{}, len(manifest.Migrations))
	for index, entry := range manifest.Migrations {
		if entry.ID <= previous {
			return errors.New("migration ids must be strictly increasing")
		}
		if _, exists := seen[entry.ID]; exists {
			return errors.New("migration ids must be unique")
		}
		if entry.Name == "" || entry.Source == "" || !checksumPattern.MatchString(entry.SHA256) {
			return fmt.Errorf("migration entry %d is incomplete", index)
		}
		seen[entry.ID] = struct{}{}
		previous = entry.ID
	}
	return nil
}
